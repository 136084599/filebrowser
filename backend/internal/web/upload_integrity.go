package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type completedUploadReceipt struct {
	actor, session, digest string
	size                   int64
	at                     time.Time
}

var completedUploads = struct {
	sync.Mutex
	entries map[string]completedUploadReceipt
}{entries: make(map[string]completedUploadReceipt)}

// Keep a bounded, short-lived receipt so a lost final response can be retried
// without turning a create-only upload into an overwrite operation.
func rememberCompletedUpload(path, actor, session, digest string, size int64) {
	if digest == "" {
		return
	}
	completedUploads.Lock()
	defer completedUploads.Unlock()
	if len(completedUploads.entries) >= 10000 {
		var oldest string
		var at time.Time
		for key, entry := range completedUploads.entries {
			if oldest == "" || entry.at.Before(at) {
				oldest, at = key, entry.at
			}
		}
		delete(completedUploads.entries, oldest)
	}
	completedUploads.entries[canonicalUploadPath(path)] = completedUploadReceipt{actor: actor, session: session, digest: digest, size: size, at: time.Now()}
}

func acknowledgeCompletedUpload(w http.ResponseWriter, r *http.Request, path, actor string) (bool, int, error) {
	session, err := parseUploadSession(r)
	if err != nil {
		return false, 0, nil
	}
	digest, err := uploadDigestHeader(r, "X-File-SHA256")
	if err != nil || digest == "" {
		return false, 0, nil
	}
	completedUploads.Lock()
	receipt, ok := completedUploads.entries[canonicalUploadPath(path)]
	completedUploads.Unlock()
	if !ok || receipt.actor != actor || receipt.session != session || receipt.digest != digest || time.Since(receipt.at) > 30*time.Minute {
		return false, 0, nil
	}
	total, hasTotal, err := parseUploadTotalSize(r)
	if err != nil || !hasTotal || total != receipt.size {
		return false, 0, nil
	}
	var offset int64
	chunked := r.Header.Get("X-File-Chunk-Offset") != ""
	if chunked {
		offset, err = strconv.ParseInt(r.Header.Get("X-File-Chunk-Offset"), 10, 64)
		if err != nil || offset < 0 || offset > total {
			return false, 0, nil
		}
	}
	if stat, err := os.Stat(path); err != nil || stat.IsDir() || stat.Size() != total {
		return false, 0, nil
	}
	staged, size, chunkDigest, err := stageUploadChunk(r, path, total-offset)
	if err != nil {
		return true, http.StatusBadRequest, err
	}
	defer func() { staged.Close(); os.Remove(staged.Name()) }()
	if offset+size != total {
		return true, http.StatusBadRequest, fmt.Errorf("incomplete completed-upload retry")
	}
	file, err := os.Open(path)
	if err != nil {
		return true, http.StatusInternalServerError, err
	}
	actual, err := fileRangeDigest(file, offset, size)
	file.Close()
	if err != nil || actual != chunkDigest || verifyUploadFile(path, digest) != nil {
		return true, http.StatusConflict, fmt.Errorf("completed upload changed; original file was kept unchanged")
	}
	if chunked {
		return true, http.StatusOK, writeChunkUploadResponse(w, total, total, true)
	}
	return true, http.StatusOK, nil
}

type uploadWriteEntry struct {
	gate chan struct{}
	refs int
}

var uploadWrites = struct {
	sync.Mutex
	entries map[string]*uploadWriteEntry
}{entries: map[string]*uploadWriteEntry{}}

// Serialize all writes to a destination, including requests using the same
// session ID. A lease alone does not prevent overlapping retries from racing.
func lockUploadWrite(ctx context.Context, path string) (func(), error) {
	path = canonicalUploadPath(path)
	uploadWrites.Lock()
	entry := uploadWrites.entries[path]
	if entry == nil {
		entry = &uploadWriteEntry{gate: make(chan struct{}, 1)}
		uploadWrites.entries[path] = entry
	}
	entry.refs++
	uploadWrites.Unlock()
	drop := func() {
		uploadWrites.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(uploadWrites.entries, path)
		}
		uploadWrites.Unlock()
	}
	select {
	case entry.gate <- struct{}{}:
		return func() { <-entry.gate; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

func uploadDigestHeader(r *http.Request, key string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(r.Header.Get(key)))
	if value == "" {
		return "", nil
	}
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != sha256.Size {
		return "", fmt.Errorf("invalid %s", key)
	}
	return value, nil
}

// Verify the body before it may replace any accepted chunk or complete file.
func stageUploadChunk(r *http.Request, destination string, remaining int64) (*os.File, int64, string, error) {
	staged, err := os.CreateTemp(filepath.Dir(destination), ".fbchunk-*")
	if err != nil {
		return nil, 0, "", err
	}
	size, actual, err := receiveUploadChunk(r, staged, remaining, true)
	if err != nil {
		staged.Close()
		os.Remove(staged.Name())
		return nil, 0, "", err
	}
	if _, err = staged.Seek(0, io.SeekStart); err != nil {
		staged.Close()
		os.Remove(staged.Name())
		return nil, 0, "", err
	}
	return staged, size, actual, nil
}

func receiveUploadChunk(r *http.Request, target io.Writer, remaining int64, compareRetry bool) (int64, string, error) {
	expected, err := uploadDigestHeader(r, "X-File-Chunk-SHA256")
	if err != nil {
		return 0, "", err
	}
	digest := sha256.New()
	// Current clients verify the assembled file once. Only older clients with
	// explicit chunk hashes, or duplicate retries comparing stored bytes, need
	// a body hash. Normal contiguous writes do no per-chunk hashing.
	hashBody := compareRetry || expected != ""
	if hashBody {
		target = io.MultiWriter(target, digest)
	}
	var body io.Reader = r.Body
	if remaining < math.MaxInt64 {
		body = io.LimitReader(r.Body, remaining+1)
	}
	size, err := io.Copy(target, body)
	if err != nil {
		err = fmt.Errorf("could not receive upload chunk: %w", err)
	}
	actual := ""
	if hashBody {
		actual = hex.EncodeToString(digest.Sum(nil))
	}
	if err == nil {
		err = validateReceivedBytes(size, 0, false, r.ContentLength)
	}
	if err == nil && expected != "" && actual != expected {
		err = fmt.Errorf("chunk SHA-256 mismatch: upload was not accepted")
	}
	return size, actual, err
}

// Only a new, contiguous suffix uses this path. Failed bodies are rolled back;
// accepted prefix bytes and the previously published file are never overwritten.
func appendVerifiedUploadChunk(r *http.Request, file *os.File, offset, total int64) (int64, error) {
	stat, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if stat.Size() != offset {
		return 0, fmt.Errorf("upload append offset is not contiguous")
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	size, _, err := receiveUploadChunk(r, file, total-offset, false)
	if err == nil {
		err = validateAssembledSize(offset, size, total)
	}
	if err != nil {
		if rollback := file.Truncate(offset); rollback != nil {
			return 0, fmt.Errorf("upload rollback failed: %v (receive error: %w)", rollback, err)
		}
		if syncErr := file.Sync(); syncErr != nil {
			return 0, fmt.Errorf("upload rollback sync failed: %v (receive error: %w)", syncErr, err)
		}
		return 0, err
	}
	return size, nil
}

func fileRangeDigest(file *os.File, offset, size int64) (string, error) {
	digest := sha256.New()
	n, err := io.Copy(digest, io.NewSectionReader(file, offset, size))
	if err != nil {
		return "", err
	}
	if n != size {
		return "", io.ErrUnexpectedEOF
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func verifyUploadFile(path, expected string) error {
	if expected == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err = io.Copy(digest, file); err != nil {
		return err
	}
	if hex.EncodeToString(digest.Sum(nil)) != expected {
		return fmt.Errorf("file SHA-256 mismatch: original destination was kept unchanged")
	}
	return nil
}
