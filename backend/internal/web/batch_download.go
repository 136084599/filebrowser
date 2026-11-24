package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/adapters/fs/files"
	"github.com/gtsteffaniak/filebrowser/backend/internal/state"
	"github.com/gtsteffaniak/filebrowser/backend/internal/utils"
)

type batchSelection struct {
	Source string `json:"source"`
	Path   string `json:"path"`
}
type batchFile struct {
	ID           int    `json:"id"`
	Source       string `json:"source"`
	Path         string `json:"path"`
	RelativePath string `json:"relativePath"`
	Size         int64  `json:"size"`
	Modified     int64  `json:"modified,string"`
}
type batchPlan struct {
	Version     int         `json:"version"`
	Ticket      string      `json:"ticket"`
	Expires     int64       `json:"expires"`
	Files       []batchFile `json:"files"`
	Directories []string    `json:"directories"`
	Username    string      `json:"-"`
	UserID      uint64      `json:"-"`
}

var batchPlans = struct {
	sync.Mutex
	plans map[string]*batchPlan
}{plans: map[string]*batchPlan{}}

// Plans contain metadata only. No archive is built, no file contents are buffered.
// A ticket can only read the exact files selected by its owner; every request
// rechecks the owner's current permissions, scope, and file identity.
func batchDownloadPlanHandler(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	var body struct {
		Selections []batchSelection `json:"selections"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024)).Decode(&body); err != nil {
		return http.StatusBadRequest, err
	}
	if len(body.Selections) == 0 || len(body.Selections) > 1000 {
		return http.StatusBadRequest, fmt.Errorf("select between 1 and 1000 items")
	}
	plan := &batchPlan{Version: 1, Username: d.User.Username, UserID: d.User.ID, Files: []batchFile{}, Directories: []string{}, Expires: time.Now().Add(24 * time.Hour).Unix()}
	seen := map[string]bool{}
	visited := map[string]bool{}
	relativeNames := map[string]string{}
	var visit func(batchSelection, string, int) error
	visit = func(sel batchSelection, relative string, depth int) error {
		if err := r.Context().Err(); err != nil {
			return err
		}
		if depth > 256 {
			return fmt.Errorf("directory nesting exceeds 256 levels")
		}
		key := sel.Source + "\x00" + sel.Path
		if seen[key] {
			return nil
		}
		perms, err := effectiveFilePerms(d, sel.Source)
		if err != nil || !perms.Download {
			return fmt.Errorf("download permission required for source %q", sel.Source)
		}
		info, err := files.FileInfoFaster(utils.FileOptions{Source: sel.Source, Path: sel.Path, Expand: true, FollowSymlinks: true, ShowHidden: true, SkipExtendedAttrs: true}, d.User)
		if err != nil {
			return err
		}
		stat, err := os.Lstat(info.RealPath)
		if err != nil {
			return err
		}
		if stat.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic link %q must be downloaded separately", sel.Path)
		}
		if previous, ok := relativeNames[relative]; ok && previous != key {
			return fmt.Errorf("selected items have the same local path %q; download them in separate batches", relative)
		}
		relativeNames[relative] = key
		seen[key] = true
		if stat.IsDir() {
			if visited[info.RealPath] {
				return nil
			}
			visited[info.RealPath] = true
			plan.Directories = append(plan.Directories, relative)
			for _, folder := range info.Folders {
				if err := visit(batchSelection{sel.Source, path.Join(sel.Path, folder.Name)}, path.Join(relative, folder.Name), depth+1); err != nil {
					return err
				}
			}
			for _, file := range info.Files {
				if strings.HasPrefix(file.Name, ".fbchunk-") || strings.HasSuffix(file.Name, ".uploading.tmp") {
					continue
				}
				if err := visit(batchSelection{sel.Source, path.Join(sel.Path, file.Name)}, path.Join(relative, file.Name), depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		if !stat.Mode().IsRegular() {
			return fmt.Errorf("only regular files can be downloaded")
		}
		if len(plan.Files) >= 100000 {
			return fmt.Errorf("batch exceeds 100000 files; select smaller batches")
		}
		plan.Files = append(plan.Files, batchFile{ID: len(plan.Files), Source: sel.Source, Path: sel.Path, RelativePath: relative, Size: stat.Size(), Modified: stat.ModTime().UnixNano()})
		return nil
	}
	sort.SliceStable(body.Selections, func(i, j int) bool { return len(body.Selections[i].Path) < len(body.Selections[j].Path) })
	for _, sel := range body.Selections {
		perms, err := effectiveFilePerms(d, sel.Source)
		if err != nil || !perms.Download {
			return http.StatusForbidden, fmt.Errorf("download permission required")
		}
		clean, err := utils.SanitizePath(sel.Path)
		if err != nil {
			return http.StatusBadRequest, err
		}
		sel.Path = clean
		name := path.Base(clean)
		if name == "/" || name == "." {
			name = sel.Source
		}
		if err := visit(sel, name, 0); err != nil {
			return ErrToStatus(err), err
		}
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return http.StatusInternalServerError, err
	}
	plan.Ticket = hex.EncodeToString(token[:])
	batchPlans.Lock()
	for key, p := range batchPlans.plans {
		if p.Expires < time.Now().Unix() {
			delete(batchPlans.plans, key)
		}
	}
	if len(batchPlans.plans) >= 128 {
		batchPlans.Unlock()
		return http.StatusTooManyRequests, fmt.Errorf("too many active download batches; try again later")
	}
	batchPlans.plans[plan.Ticket] = plan
	batchPlans.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	return RenderJSON(w, r, plan)
}

func batchDownloadFileHandler(w http.ResponseWriter, r *http.Request, d *Context) (int, error) {
	batchPlans.Lock()
	plan := batchPlans.plans[r.PathValue("ticket")]
	batchPlans.Unlock()
	if plan == nil || plan.Expires < time.Now().Unix() {
		return http.StatusUnauthorized, fmt.Errorf("download task expired; create a new task from FileBrowser")
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 0 || id >= len(plan.Files) {
		return http.StatusNotFound, fmt.Errorf("file is not in this download task")
	}
	user, err := state.GetUserByUsername(plan.Username)
	if err != nil || user.ID != plan.UserID {
		return http.StatusForbidden, fmt.Errorf("download owner no longer exists")
	}
	d.User = &user
	file := plan.Files[id]
	perms, err := effectiveFilePerms(d, file.Source)
	if err != nil || !perms.Download {
		return http.StatusForbidden, fmt.Errorf("download permission revoked")
	}
	info, err := files.FileInfoFaster(utils.FileOptions{Source: file.Source, Path: file.Path, FollowSymlinks: true, SkipExtendedAttrs: true}, &user)
	if err != nil {
		return ErrToStatus(err), err
	}
	stat, err := os.Stat(info.RealPath)
	if err != nil {
		return ErrToStatus(err), err
	}
	if !stat.Mode().IsRegular() || stat.Size() != file.Size || stat.ModTime().UnixNano() != file.Modified {
		return http.StatusConflict, fmt.Errorf("file changed since selection; create a new download task")
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("checksum") == "sha256" {
		return streamBatchChecksum(w, r, info.RealPath, file)
	}
	q := r.URL.Query()
	q.Set("source", file.Source)
	q.Set("file", file.Path)
	q.Del("inline")
	r.URL.RawQuery = q.Encode()
	return downloadHandler(w, r, d)
}

// Large files may take minutes to hash on a hard drive. Flush JSON whitespace
// while hashing so connection/header/idle timeouts cannot stall finalization.
func streamBatchChecksum(w http.ResponseWriter, r *http.Request, realPath string, file batchFile) (int, error) {
	input, err := os.Open(realPath)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	defer input.Close()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "\n")
	_ = http.NewResponseController(w).Flush()
	digest := sha256.New()
	buffer := make([]byte, 1024*1024)
	lastFlush := time.Now()
	for {
		if err := r.Context().Err(); err != nil {
			return http.StatusOK, err
		}
		n, readErr := input.Read(buffer)
		if n > 0 {
			digest.Write(buffer[:n])
		}
		if time.Since(lastFlush) > time.Second {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return http.StatusOK, err
			}
			_ = http.NewResponseController(w).Flush()
			lastFlush = time.Now()
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = json.NewEncoder(w).Encode(map[string]string{"message": readErr.Error()})
			return http.StatusOK, nil
		}
	}
	after, err := input.Stat()
	if err != nil || after.Size() != file.Size || after.ModTime().UnixNano() != file.Modified {
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "file changed during verification"})
		return http.StatusOK, nil
	}
	return http.StatusOK, json.NewEncoder(w).Encode(map[string]interface{}{"sha256": hex.EncodeToString(digest.Sum(nil)), "size": file.Size})
}
