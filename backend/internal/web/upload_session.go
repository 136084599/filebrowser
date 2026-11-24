package web

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const uploadSessionHeader = "X-File-Upload-Session"

// uploadSessionTTL is how long an incomplete upload may keep exclusive claim on a path.
// Successful chunks refresh the lease; abandoned sessions expire and can be replaced.
var uploadSessionTTL = 30 * time.Minute

var errUploadSessionConflict = errors.New("conflicting upload session")

type uploadSessionEntry struct {
	id       string
	actor    string
	lastSeen time.Time
}

type uploadSessionRegistry struct {
	mu       sync.Mutex
	sessions map[string]uploadSessionEntry // realPath -> active session
}

var activeUploadSessions uploadSessionRegistry

func canonicalUploadPath(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func (r *uploadSessionRegistry) acquire(realPath, sessionID string, actors ...string) error {
	actor := ""
	if len(actors) > 0 {
		actor = actors[0]
	}
	realPath = canonicalUploadPath(realPath)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions == nil {
		r.sessions = make(map[string]uploadSessionEntry)
	}
	now := time.Now()
	if active, ok := r.sessions[realPath]; ok {
		if now.Sub(active.lastSeen) > uploadSessionTTL {
			delete(r.sessions, realPath)
		} else if active.id != sessionID || active.actor != actor {
			return fmt.Errorf("%w for %q", errUploadSessionConflict, realPath)
		}
	}
	r.sessions[realPath] = uploadSessionEntry{id: sessionID, actor: actor, lastSeen: now}
	return nil
}

func (r *uploadSessionRegistry) release(realPath, sessionID string, actors ...string) {
	actor := ""
	if len(actors) > 0 {
		actor = actors[0]
	}
	realPath = canonicalUploadPath(realPath)
	r.mu.Lock()
	defer r.mu.Unlock()
	if active, ok := r.sessions[realPath]; ok && active.id == sessionID && active.actor == actor {
		delete(r.sessions, realPath)
	}
}

func (r *uploadSessionRegistry) foreignOwner(realPath, session, actor string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.sessions[canonicalUploadPath(realPath)]
	return ok && entry.id == session && entry.actor != actor && time.Since(entry.lastSeen) <= uploadSessionTTL
}

// expireForTest marks a path's session as expired without deleting it (for unit tests).
func (r *uploadSessionRegistry) expireForTest(realPath string) {
	realPath = canonicalUploadPath(realPath)
	r.mu.Lock()
	defer r.mu.Unlock()
	if active, ok := r.sessions[realPath]; ok {
		active.lastSeen = time.Now().Add(-uploadSessionTTL - time.Second)
		r.sessions[realPath] = active
	}
}

func isUploadSessionConflict(err error) bool {
	return errors.Is(err, errUploadSessionConflict)
}

// parseUploadSession reads X-File-Upload-Session. The value must be a short safe token.
func parseUploadSession(r *http.Request) (string, error) {
	s := strings.TrimSpace(r.Header.Get(uploadSessionHeader))
	if s == "" {
		return "", fmt.Errorf("missing %s", uploadSessionHeader)
	}
	if len(s) > 128 {
		return "", fmt.Errorf("invalid upload session")
	}
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return "", fmt.Errorf("invalid upload session")
	}
	return s, nil
}
