package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	"github.com/gtsteffaniak/filebrowser/backend/internal/state"
	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

// Resolve the existing ancestor first, then append only missing path segments.
// Unlike resolving the final file alone, this also bounds new upload targets.
func resolveMissingUploadPath(path string) (string, error) {
	ancestor, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var missing []string
	for {
		_, statErr := os.Lstat(ancestor)
		if statErr == nil {
			resolved, err := filepath.EvalSymlinks(ancestor)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", statErr
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
}

func resolveUploadPath(idx *indexing.Index, scope, indexPath string) (string, error) {
	root, err := filepath.EvalSymlinks(idx.Path)
	if err != nil {
		return "", err
	}
	bound, err := resolveMissingUploadPath(utils.JoinUnderSourceRoot(idx.Path, scope))
	if err != nil {
		return "", err
	}
	candidate, err := resolveMissingUploadPath(utils.JoinUnderSourceRoot(idx.Path, indexPath))
	if err != nil {
		return "", err
	}
	for _, prefix := range []string{root, bound} {
		relative, err := filepath.Rel(prefix, candidate)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return "", fmt.Errorf("upload path escapes its source or user scope")
		}
	}
	return candidate, nil
}

type uploadLifecycle struct {
	actor, path, session string
	cancelled, published bool
	directory            os.FileInfo
	touched              time.Time
	readers              map[*http.Request]func()
}

var uploadLifecycles = struct {
	sync.Mutex
	entries map[string]*uploadLifecycle
}{entries: make(map[string]*uploadLifecycle)}

func uploadLifecycleKey(path, actor, session string) string {
	return canonicalUploadPath(path) + "\x00" + actor + "\x00" + session
}

// Include the authenticated principal in the on-disk name as well as the lease.
// Cleanup remains restricted to that account even after a server restart.
func uploadActorTempPath(path, session, actor string) string {
	sum := sha256.Sum256([]byte(actor))
	return uploadTempPath(path, session+"_"+hex.EncodeToString(sum[:16]))
}

func pruneUploadLifecycles() {
	if len(uploadLifecycles.entries) < 50000 {
		return
	}
	for key, entry := range uploadLifecycles.entries {
		if len(entry.readers) == 0 && time.Since(entry.touched) > uploadSessionTTL {
			delete(uploadLifecycles.entries, key)
		}
	}
}

func beginUploadLifecycle(w http.ResponseWriter, r *http.Request, path, actor, session string) (*uploadLifecycle, func(), error) {
	key := uploadLifecycleKey(path, actor, session)
	uploadLifecycles.Lock()
	pruneUploadLifecycles()
	entry := uploadLifecycles.entries[key]
	if entry == nil {
		entry = &uploadLifecycle{actor: actor, path: path, session: session, readers: make(map[*http.Request]func())}
		uploadLifecycles.entries[key] = entry
	}
	if entry.cancelled {
		uploadLifecycles.Unlock()
		return nil, nil, fmt.Errorf("upload was cancelled; start a new upload session")
	}
	entry.touched = time.Now()
	entry.readers[r] = func() {
		// A context cancellation alone does not interrupt a blocked request-body read.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now())
		go r.Body.Close()
	}
	uploadLifecycles.Unlock()
	return entry, func() {
		uploadLifecycles.Lock()
		defer uploadLifecycles.Unlock()
		delete(entry.readers, r)
		entry.touched = time.Now()
		if entry.published && !entry.cancelled && entry.directory == nil {
			delete(uploadLifecycles.entries, key)
		}
	}, nil
}

func uploadCancelled(entry *uploadLifecycle) bool {
	if entry == nil {
		return false
	}
	uploadLifecycles.Lock()
	defer uploadLifecycles.Unlock()
	return entry.cancelled
}

func uploadPublished(entry *uploadLifecycle) {
	if entry == nil {
		return
	}
	uploadLifecycles.Lock()
	defer uploadLifecycles.Unlock()
	entry.published = true
}

func uploadDirectoryCreated(entry *uploadLifecycle, path string) {
	if entry == nil {
		return
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return
	}
	uploadLifecycles.Lock()
	defer uploadLifecycles.Unlock()
	entry.directory = info
}

func resourceCancelUploadHandler(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	source := r.URL.Query().Get("source")
	user := d.User
	if d.Share.Hash != "" {
		user = d.ShareUser
	}
	if user == nil {
		return http.StatusForbidden, fmt.Errorf("upload owner unavailable")
	}
	if d.Share.Hash != "" {
		if d.Share.ShareType != "upload" && !d.Share.AllowCreate {
			return http.StatusForbidden, fmt.Errorf("uploading is disabled for this share")
		}
	} else {
		perms, err := effectiveFilePerms(d, source)
		if err != nil || (!perms.Create && !perms.Modify) {
			return http.StatusForbidden, fmt.Errorf("upload permission required")
		}
	}
	clean, err := utils.SanitizePath(r.URL.Query().Get("path"))
	if err != nil {
		return http.StatusBadRequest, err
	}
	idx := indexing.GetIndex(source)
	if idx == nil {
		return http.StatusNotFound, fmt.Errorf("source not found")
	}
	scope, err := user.GetScopeForSourceName(source)
	if err != nil {
		return http.StatusForbidden, err
	}
	indexPath := utils.JoinScopedIndexPath(scope, clean)
	if !state.AccessPermitted(idx.Path, utils.IndexPathFromNormalized(indexPath, true), user.Username) {
		return http.StatusForbidden, fmt.Errorf("access denied")
	}
	realPath, err := resolveUploadPath(idx, scope, indexPath)
	if err != nil {
		return http.StatusForbidden, err
	}
	session, err := parseUploadSession(r)
	if err != nil {
		return http.StatusBadRequest, err
	}
	actor := fmt.Sprintf("%d:%s", user.ID, d.Share.Hash)
	if activeUploadSessions.foreignOwner(realPath, session, actor) {
		return http.StatusForbidden, fmt.Errorf("this upload belongs to another account")
	}
	key := uploadLifecycleKey(realPath, actor, session)
	uploadLifecycles.Lock()
	pruneUploadLifecycles()
	entry := uploadLifecycles.entries[key]
	if entry == nil {
		entry = &uploadLifecycle{actor: actor, path: realPath, session: session, readers: make(map[*http.Request]func())}
		uploadLifecycles.entries[key] = entry
	}
	entry.cancelled = true
	entry.touched = time.Now()
	stop := make([]func(), 0, len(entry.readers))
	for _, fn := range entry.readers {
		stop = append(stop, fn)
	}
	uploadLifecycles.Unlock()
	for _, fn := range stop {
		fn()
	}
	unlock, err := lockUploadWrite(r.Context(), realPath)
	if err != nil {
		return http.StatusRequestTimeout, err
	}
	defer unlock()
	temp := uploadActorTempPath(realPath, session, actor)
	removed := false
	if err := os.Remove(temp); err == nil {
		removed = true
	} else if !os.IsNotExist(err) {
		return http.StatusInternalServerError, fmt.Errorf("could not remove this upload's temporary file: %w", err)
	}
	activeUploadSessions.release(realPath, session, actor)
	uploadLifecycles.Lock()
	directory := entry.directory
	published := entry.published
	uploadLifecycles.Unlock()
	directoryRemoved := false
	if directory != nil && canonicalUploadPath(realPath) != canonicalUploadPath(idx.Path) {
		current, statErr := os.Lstat(realPath)
		if statErr != nil && !os.IsNotExist(statErr) {
			return http.StatusInternalServerError, statErr
		}
		if statErr == nil && current.IsDir() && os.SameFile(directory, current) {
			contents, readErr := os.ReadDir(realPath)
			if readErr != nil {
				return http.StatusInternalServerError, readErr
			}
			if len(contents) == 0 {
				if err = os.Remove(realPath); err == nil {
					directoryRemoved = true
					if !idx.Config.ResolvedRules.IndexingDisabled {
						idx.DeleteMetadata(indexPath, true, false)
						_ = files.RefreshIndex(source, filepath.Dir(indexPath), true, false)
					}
				} else if now, readErr := os.ReadDir(realPath); readErr != nil || len(now) == 0 {
					return http.StatusInternalServerError, err
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	return http.StatusOK, json.NewEncoder(w).Encode(map[string]bool{"cancelled": true, "temporaryRemoved": removed, "directoryRemoved": directoryRemoved, "ownedDirectory": directory != nil, "completedFileKept": published && directory == nil})
}

func publicCancelUploadHandler(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	source, ok := settings.Config.Server.SourceMap[d.Share.SourcePath]
	if !ok {
		return http.StatusNotFound, fmt.Errorf("source not found")
	}
	q := r.URL.Query()
	q.Set("source", source.Name)
	q.Set("path", d.IndexPath)
	r.URL.RawQuery = q.Encode()
	return resourceCancelUploadHandler(w, r, d)
}
