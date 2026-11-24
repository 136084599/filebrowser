package web

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
)

func cancelUploadForTest(t *testing.T, user *users.User, path, session string) (int, error) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/resources/cancel-upload?source=uploads&path="+url.QueryEscape(path), nil)
	req.Header.Set(uploadSessionHeader, session)
	return resourceCancelUploadHandler(httptest.NewRecorder(), req, &Context{User: user})
}

func TestCancelUploadProtectsOriginalAndBlocksLateRequests(t *testing.T) {
	root, user := setupUploadHTTPTest(t)
	target := filepath.Join(root, "keep.bin")
	os.WriteFile(target, []byte("original"), 0600)
	headers := map[string]string{uploadSessionHeader: "cancel-test", "X-File-Chunk-Offset": "0", "X-File-Total-Size": "8"}
	status, err := postUpload(t, user, "/keep.bin&override=true", bytes.NewBufferString("aaaa"), 4, headers)
	if status != 200 || err != nil {
		t.Fatal(status, err)
	}
	temp := uploadActorTempPath(target, "cancel-test", "1:")
	if _, err = os.Stat(temp); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if status, err := cancelUploadForTest(t, user, "/keep.bin", "cancel-test"); status != 200 || err != nil {
			t.Fatal(status, err)
		}
	}
	if _, err = os.Stat(temp); !os.IsNotExist(err) {
		t.Fatal("partial file remains", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "original" {
		t.Fatal("original changed")
	}
	headers["X-File-Chunk-Offset"] = "4"
	if status, _ := postUpload(t, user, "/keep.bin&override=true", bytes.NewBufferString("bbbb"), 4, headers); status != 410 {
		t.Fatal("cancelled request revived", status)
	}
	got, _ = os.ReadFile(target)
	if string(got) != "original" {
		t.Fatal("late request changed original")
	}
}

func TestCancelUploadAccountIsolation(t *testing.T) {
	root, user := setupUploadHTTPTest(t)
	other := *user
	other.ID = 2
	other.Username = "second"
	headers := map[string]string{uploadSessionHeader: "same-token", "X-File-Chunk-Offset": "0", "X-File-Total-Size": "8"}
	if status, err := postUpload(t, user, "/shared.bin", bytes.NewBufferString("aaaa"), 4, headers); status != 200 || err != nil {
		t.Fatal(status, err)
	}
	if status, _ := cancelUploadForTest(t, &other, "/shared.bin", "same-token"); status != 403 {
		t.Fatal("another account may cancel active session", status)
	}
	if status, _ := postUpload(t, &other, "/shared.bin", bytes.NewBufferString("xxxx"), 4, headers); status != 409 {
		t.Fatal("same token mixed accounts", status)
	}
	got, _ := os.ReadFile(uploadActorTempPath(filepath.Join(root, "shared.bin"), "same-token", "1:"))
	if string(got) != "aaaa" {
		t.Fatal("foreign cancellation changed prefix")
	}
	if status, err := cancelUploadForTest(t, user, "/shared.bin", "same-token"); status != 200 || err != nil {
		t.Fatal(status, err)
	}
	if status, err := postUpload(t, &other, "/shared.bin", bytes.NewBufferString("bbbb"), 4, headers); status != 200 || err != nil {
		t.Fatal("cancel did not free the path", status, err)
	}
}

func TestCancelOnlyRemovesNewEmptyDirectories(t *testing.T) {
	root, user := setupUploadHTTPTest(t)
	os.Mkdir(filepath.Join(root, "existing"), 0700)
	for _, name := range []string{"new", "existing", "nonempty"} {
		req := httptest.NewRequest(http.MethodPost, "/api/resources?source=uploads&path=/"+name+"&isDir=true&override=true", nil)
		req.Header.Set(uploadSessionHeader, name)
		if status, err := ResourcePostHandler(httptest.NewRecorder(), req, &Context{User: user}); status != 200 || err != nil {
			t.Fatal(status, err)
		}
	}
	os.WriteFile(filepath.Join(root, "nonempty", "someone-else.txt"), []byte("keep"), 0600)
	for _, name := range []string{"new", "existing", "nonempty"} {
		if status, err := cancelUploadForTest(t, user, "/"+name, name); status != 200 || err != nil {
			t.Fatal(name, status, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Fatal("new empty directory remains", err)
	}
	if _, err := os.Stat(filepath.Join(root, "existing")); err != nil {
		t.Fatal("existing empty directory removed", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "nonempty", "someone-else.txt"))
	if string(got) != "keep" {
		t.Fatal("nonempty directory changed")
	}
}

func TestCancelBeforeRequestAndAfterServerStateLoss(t *testing.T) {
	root, user := setupUploadHTTPTest(t)
	if status, err := cancelUploadForTest(t, user, "/queued.bin", "queued"); status != 200 || err != nil {
		t.Fatal(status, err)
	}
	if status, _ := postUpload(t, user, "/queued.bin", bytes.NewBufferString("abcd"), 4, map[string]string{uploadSessionHeader: "queued", "X-File-Total-Size": "4"}); status != 410 {
		t.Fatal(status)
	}
	own := uploadActorTempPath(filepath.Join(root, "orphan.bin"), "known-session", "1:")
	foreign := uploadActorTempPath(filepath.Join(root, "orphan.bin"), "known-session", "2:")
	os.WriteFile(own, []byte("mine"), 0600)
	os.WriteFile(foreign, []byte("theirs"), 0600)
	if status, err := cancelUploadForTest(t, user, "/orphan.bin", "known-session"); status != 200 || err != nil {
		t.Fatal(status, err)
	}
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(foreign); string(got) != "theirs" {
		t.Fatal("another account's partial removed")
	}
}

type cancelBlockingBody struct {
	entered chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (b *cancelBlockingBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.closed
	return 0, io.ErrClosedPipe
}
func (b *cancelBlockingBody) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}

func TestCancelInterruptsBlockedBody(t *testing.T) {
	root, user := setupUploadHTTPTest(t)
	body := &cancelBlockingBody{entered: make(chan struct{}), closed: make(chan struct{})}
	req := httptest.NewRequest(http.MethodPost, "/api/resources?source=uploads&path=/blocked.bin", nil)
	req.Body = body
	req.ContentLength = 8
	req.Header.Set(uploadSessionHeader, "blocked")
	req.Header.Set("X-File-Chunk-Offset", "0")
	req.Header.Set("X-File-Total-Size", "8")
	done := make(chan struct{})
	go func() { defer close(done); ResourcePostHandler(httptest.NewRecorder(), req, &Context{User: user}) }()
	select {
	case <-body.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("upload never read body")
	}
	status, err := cancelUploadForTest(t, user, "/blocked.bin", "blocked")
	if status != 200 || err != nil {
		t.Fatal(status, err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop request")
	}
	if _, err := os.Stat(uploadActorTempPath(filepath.Join(root, "blocked.bin"), "blocked", "1:")); !os.IsNotExist(err) {
		t.Fatal("temporary file remains", err)
	}
}

func TestCancelDoesNotHideCleanupFailure(t *testing.T) {
	root, user := setupUploadHTTPTest(t)
	temp := uploadActorTempPath(filepath.Join(root, "failed.bin"), "failed", "1:")
	os.Mkdir(temp, 0700)
	os.WriteFile(filepath.Join(temp, "keep"), []byte("data"), 0600)
	if status, err := cancelUploadForTest(t, user, "/failed.bin", "failed"); status != 500 || err == nil {
		t.Fatal("cleanup failure hidden", status, err)
	}
	if _, err := os.Stat(filepath.Join(temp, "keep")); errors.Is(err, os.ErrNotExist) {
		t.Fatal("unexpected recursive cleanup")
	}
}
