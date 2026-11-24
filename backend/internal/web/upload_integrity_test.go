package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func digestForTest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

func TestWholeFileOnlyUploadRejectsCorruptPrefixAndCanRestart(t *testing.T) {
	root, user := setupUploadHTTPTest(t)
	target := filepath.Join(root, "whole-only.bin")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"X-File-Chunk-Offset": "0", "X-File-Total-Size": "8"}
	// No per-chunk digest: a same-length bad prefix is detected at finalization.
	if status, err := postUpload(t, user, "/whole-only.bin&override=true", bytes.NewBufferString("oops"), 4, headers); status != 200 || err != nil {
		t.Fatal(status, err)
	}
	headers["X-File-Chunk-Offset"] = "4"
	headers["X-File-SHA256"] = digestForTest([]byte("aaaabbbb"))
	if status, _ := postUpload(t, user, "/whole-only.bin&override=true", bytes.NewBufferString("bbbb"), 4, headers); status != http.StatusUnprocessableEntity {
		t.Fatal("corrupt assembled file accepted", status)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "original" {
		t.Fatal("original destination changed")
	}
	if _, err := os.Stat(uploadActorTempPath(target, "test-session", "1:")); !os.IsNotExist(err) {
		t.Fatal("corrupt partial was not removed", err)
	}
	headers["X-File-Chunk-Offset"] = "0"
	delete(headers, "X-File-SHA256")
	if status, err := postUpload(t, user, "/whole-only.bin&override=true", bytes.NewBufferString("aaaa"), 4, headers); status != 200 || err != nil {
		t.Fatal("restart failed", status, err)
	}
	// A lost middle acknowledgement is still safe without per-chunk headers.
	if status, err := postUpload(t, user, "/whole-only.bin&override=true", bytes.NewBufferString("aaaa"), 4, headers); status != 200 || err != nil {
		t.Fatal("duplicate retry failed", status, err)
	}
	headers["X-File-Chunk-Offset"] = "4"
	headers["X-File-SHA256"] = digestForTest([]byte("aaaabbbb"))
	for i := 0; i < 2; i++ {
		if status, err := postUpload(t, user, "/whole-only.bin&override=true", bytes.NewBufferString("bbbb"), 4, headers); status != 200 || err != nil {
			t.Fatal("final upload or lost final acknowledgement failed", i, status, err)
		}
	}
	got, _ = os.ReadFile(target)
	if string(got) != "aaaabbbb" {
		t.Fatal("final bytes differ")
	}
}

func TestReceiveUploadChunkSkipsHashUnlessNeeded(t *testing.T) {
	for _, compareRetry := range []bool{false, true} {
		var output bytes.Buffer
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString("body"))
		size, digest, err := receiveUploadChunk(req, &output, 4, compareRetry)
		if err != nil || size != 4 || output.String() != "body" {
			t.Fatal(size, err)
		}
		if compareRetry && digest != digestForTest([]byte("body")) || !compareRetry && digest != "" {
			t.Fatal("unexpected body hashing", compareRetry, digest)
		}
	}
}
func TestUploadRejectsCorruptChunkAndKeepsAcceptedPrefix(t *testing.T) {
	root, user := setupUploadHTTPTest(t)
	first := []byte("aaaa")
	headers := map[string]string{"X-File-Chunk-Offset": "0", "X-File-Total-Size": "8", "X-File-Chunk-SHA256": digestForTest(first)}
	if status, err := postUpload(t, user, "/checked.bin", bytes.NewReader(first), 4, headers); status != 200 || err != nil {
		t.Fatal(status, err)
	}
	headers["X-File-Chunk-Offset"] = "4"
	headers["X-File-Chunk-SHA256"] = digestForTest([]byte("bbbb"))
	if status, _ := postUpload(t, user, "/checked.bin", bytes.NewReader([]byte("oops")), 4, headers); status != http.StatusBadRequest {
		t.Fatal("corrupt chunk accepted", status)
	}
	got, err := os.ReadFile(uploadActorTempPath(filepath.Join(root, "checked.bin"), "test-session", "1:"))
	if err != nil || !bytes.Equal(got, first) {
		t.Fatal("accepted prefix changed", err)
	}
	headers["X-File-SHA256"] = digestForTest([]byte("aaaabbbb"))
	if status, err := postUpload(t, user, "/checked.bin", bytes.NewReader([]byte("bbbb")), 4, headers); status != 200 || err != nil {
		t.Fatal(status, err)
	}
	got, _ = os.ReadFile(filepath.Join(root, "checked.bin"))
	if string(got) != "aaaabbbb" {
		t.Fatal("final file corrupt")
	}
}
func TestUploadRejectsMissingPrefix(t *testing.T) {
	root, user := setupUploadHTTPTest(t)
	status, _ := postUpload(t, user, "/gap.bin", bytes.NewReader([]byte("abcd")), 4, map[string]string{"X-File-Chunk-Offset": "4", "X-File-Total-Size": "8", "X-File-Chunk-SHA256": digestForTest([]byte("abcd"))})
	if status != http.StatusRequestedRangeNotSatisfiable {
		t.Fatal("hole upload accepted", status)
	}
	if _, err := os.Stat(filepath.Join(root, "gap.bin")); !os.IsNotExist(err) {
		t.Fatal("incomplete target published")
	}
}
func TestUploadWholeFileChecksumProtectsOldDestination(t *testing.T) {
	root, user := setupUploadHTTPTest(t)
	target := filepath.Join(root, "existing.bin")
	os.WriteFile(target, []byte("old"), 0600)
	req := map[string]string{"X-File-Total-Size": "3", "X-File-Chunk-SHA256": digestForTest([]byte("new")), "X-File-SHA256": digestForTest([]byte("bad"))}
	request := httptest.NewRequest(http.MethodPost, "/api/resources?source=uploads&path=/existing.bin&override=true", bytes.NewReader([]byte("new")))
	for key, value := range req {
		request.Header.Set(key, value)
	}
	request.Header.Set("X-File-Upload-Session", "verified-overwrite")
	status, _ := ResourcePostHandler(httptest.NewRecorder(), request, &Context{User: user})
	if status != http.StatusUnprocessableEntity {
		t.Fatal("bad digest accepted", status)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "old" {
		t.Fatal("old complete destination changed")
	}
}

func TestCompletedChunkRetryWithoutOverwritePermission(t *testing.T) {
	root, user := setupUploadHTTPTest(t)
	for path, perms := range user.BackendSourcePermissions {
		perms.Modify = false
		user.BackendSourcePermissions[path] = perms
	}
	for i := range user.BackendScopes {
		user.BackendScopes[i].Permissions.Modify = false
	}
	headers := map[string]string{"X-File-Chunk-Offset": "0", "X-File-Total-Size": "8", "X-File-Chunk-SHA256": digestForTest([]byte("aaaa"))}
	if status, err := postUpload(t, user, "/receipt.bin", bytes.NewReader([]byte("aaaa")), 4, headers); status != 200 || err != nil {
		t.Fatal(status, err)
	}
	headers["X-File-Chunk-Offset"] = "4"
	headers["X-File-Chunk-SHA256"] = digestForTest([]byte("bbbb"))
	headers["X-File-SHA256"] = digestForTest([]byte("aaaabbbb"))
	if status, err := postUpload(t, user, "/receipt.bin", bytes.NewReader([]byte("bbbb")), 4, headers); status != 200 || err != nil {
		t.Fatal(status, err)
	}
	target := filepath.Join(root, "receipt.bin")
	before, _ := os.Stat(target)
	if status, err := postUpload(t, user, "/receipt.bin", bytes.NewReader([]byte("bbbb")), 4, headers); status != 200 || err != nil {
		t.Fatal("lost-response retry failed", status, err)
	}
	after, _ := os.Stat(target)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("retry rewrote the completed file")
	}
	other := *user
	other.ID++
	if status, _ := postUpload(t, &other, "/receipt.bin", bytes.NewReader([]byte("bbbb")), 4, headers); status != 409 {
		t.Fatal("receipt accepted a different user", status)
	}
	headers[uploadSessionHeader] = "other-session"
	if status, _ := postUpload(t, user, "/receipt.bin", bytes.NewReader([]byte("bbbb")), 4, headers); status != 409 {
		t.Fatal("receipt accepted a different session", status)
	}
	headers[uploadSessionHeader] = "test-session"
	headers["X-File-Chunk-SHA256"] = digestForTest([]byte("cccc"))
	if status, _ := postUpload(t, user, "/receipt.bin", bytes.NewReader([]byte("cccc")), 4, headers); status != 409 {
		t.Fatal("receipt accepted different final bytes", status)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "aaaabbbb" {
		t.Fatal("completed destination changed")
	}
	os.WriteFile(target, []byte("xxxxyyyy"), 0600)
	headers["X-File-Chunk-SHA256"] = digestForTest([]byte("bbbb"))
	if status, _ := postUpload(t, user, "/receipt.bin", bytes.NewReader([]byte("bbbb")), 4, headers); status != 409 {
		t.Fatal("receipt accepted a changed destination", status)
	}
}

func TestCompletedSingleRequestCanBeAcknowledgedAgain(t *testing.T) {
	_, user := setupUploadHTTPTest(t)
	headers := map[string]string{"X-File-Total-Size": "3", "X-File-Chunk-SHA256": digestForTest([]byte("abc")), "X-File-SHA256": digestForTest([]byte("abc"))}
	for i := 0; i < 2; i++ {
		if status, err := postUpload(t, user, "/single-receipt.bin", bytes.NewReader([]byte("abc")), 3, headers); status != 200 || err != nil {
			t.Fatal(i, status, err)
		}
	}
}

func TestAppendUploadRollbackAndRetry(t *testing.T) {
	for _, scenario := range []string{"short", "oversize", "disconnect", "checksum"} {
		t.Run(scenario, func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "upload")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err = f.Write([]byte("prefix")); err != nil {
				t.Fatal(err)
			}
			var body io.Reader = bytes.NewReader([]byte("body"))
			length := int64(4)
			switch scenario {
			case "short":
				length = 8
			case "oversize":
				body = bytes.NewReader([]byte("bodyEXTRA"))
				length = -1
			case "disconnect":
				body = &errAfterReader{data: []byte("bo"), err: errors.New("disconnected")}
			}
			req := httptest.NewRequest(http.MethodPost, "/", body)
			req.ContentLength = length
			if scenario == "checksum" {
				req.Header.Set("X-File-Chunk-SHA256", digestForTest([]byte("oops")))
			}
			if _, err := appendVerifiedUploadChunk(req, f, 6, 10); err == nil {
				t.Fatal("bad suffix accepted")
			}
			got, err := os.ReadFile(f.Name())
			if err != nil || string(got) != "prefix" {
				t.Fatal("prefix changed", string(got), err)
			}
			retry := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("body")))
			retry.Header.Set("X-File-Chunk-SHA256", digestForTest([]byte("body")))
			if n, err := appendVerifiedUploadChunk(retry, f, 6, 10); err != nil || n != 4 {
				t.Fatal(n, err)
			}
			got, _ = os.ReadFile(f.Name())
			if string(got) != "prefixbody" {
				t.Fatal(string(got))
			}
		})
	}
}
func TestChunkUploadsReuseHTTPConnection(t *testing.T) {
	root, user := setupUploadHTTPTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, err := ResourcePostHandler(w, r, &Context{User: user})
		if err != nil {
			http.Error(w, err.Error(), status)
		}
	}))
	defer server.Close()
	client := server.Client()
	defer client.CloseIdleConnections()
	for i, body := range []string{"aaaa", "bbbb"} {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/resources?source=uploads&path=/keepalive.bin&_chunkSeq="+strconv.Itoa(i), bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(uploadSessionHeader, "keepalive-test")
		req.Header.Set("X-File-Total-Size", "8")
		req.Header.Set("X-File-Chunk-Offset", strconv.Itoa(4*i))
		req.Header.Set("X-File-Chunk-SHA256", digestForTest([]byte(body)))
		reused := false
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}))
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 {
			t.Fatal(response.StatusCode, string(raw), err)
		}
		if i > 0 && !reused {
			t.Fatal("second chunk opened a new TCP connection")
		}
	}
	got, _ := os.ReadFile(filepath.Join(root, "keepalive.bin"))
	if string(got) != "aaaabbbb" {
		t.Fatal(string(got))
	}
}

func TestInterruptedDuplicateChunkRemainsRetryable(t *testing.T) {
	root, user := setupUploadHTTPTest(t)
	headers := map[string]string{"X-File-Chunk-Offset": "0", "X-File-Total-Size": "8", "X-File-Chunk-SHA256": digestForTest([]byte("aaaa"))}
	if status, err := postUpload(t, user, "/duplicate.bin", bytes.NewBufferString("aaaa"), 4, headers); status != 200 || err != nil {
		t.Fatal(status, err)
	}
	failing := &errAfterReader{data: []byte("aa"), err: errors.New("duplicate retry disconnected")}
	if status, _ := postUpload(t, user, "/duplicate.bin", failing, 4, headers); status != http.StatusInternalServerError {
		t.Fatal("transient retry body became permanent failure", status)
	}
	got, _ := os.ReadFile(uploadActorTempPath(filepath.Join(root, "duplicate.bin"), "test-session", "1:"))
	if string(got) != "aaaa" {
		t.Fatal("accepted prefix changed", string(got))
	}
	if status, err := postUpload(t, user, "/duplicate.bin", bytes.NewBufferString("aaaa"), 4, headers); status != 200 || err != nil {
		t.Fatal(status, err)
	}
	headers["X-File-Chunk-Offset"] = "4"
	headers["X-File-Chunk-SHA256"] = digestForTest([]byte("bbbb"))
	headers["X-File-SHA256"] = digestForTest([]byte("aaaabbbb"))
	if status, err := postUpload(t, user, "/duplicate.bin", bytes.NewBufferString("bbbb"), 4, headers); status != 200 || err != nil {
		t.Fatal(status, err)
	}
	got, _ = os.ReadFile(filepath.Join(root, "duplicate.bin"))
	if string(got) != "aaaabbbb" {
		t.Fatal(string(got))
	}
}
