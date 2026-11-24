// FileBrowser directory downloader. Tickets are restricted to one batch and do
// not contain the user's password or a general-purpose authentication token.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type taskFile struct {
	ID           int    `json:"id"`
	Source       string `json:"source"`
	Path         string `json:"path"`
	RelativePath string `json:"relativePath"`
	Size         int64  `json:"size"`
	Modified     int64  `json:"modified,string"`
}
type task struct {
	Version     int        `json:"version"`
	APIBase     string     `json:"apiBase"`
	Ticket      string     `json:"ticket"`
	Expires     int64      `json:"expires"`
	Concurrency int        `json:"concurrency"`
	Files       []taskFile `json:"files"`
	Directories []string   `json:"directories"`
}
type partialState struct {
	Fingerprint string `json:"fingerprint"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
}

var printMu sync.Mutex

func progress(format string, args ...interface{}) {
	printMu.Lock()
	fmt.Printf(format+"\n", args...)
	printMu.Unlock()
}

var reserved = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)`)

func safeDestination(root, relative string) (string, error) {
	if relative == "" || strings.Contains(relative, "\\") {
		return "", fmt.Errorf("无效的下载路径")
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "<>:\"|?*\x00") || strings.TrimRight(part, ". ") != part || reserved.MatchString(part) {
			return "", fmt.Errorf("无效的下载路径 %q", relative)
		}
		for _, char := range part {
			if char < 32 {
				return "", fmt.Errorf("无效路径控制字符")
			}
		}
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("下载路径超出保存目录")
	}
	// Existing junctions/symlinks may redirect an otherwise safe relative path.
	current := target
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			check, err := filepath.Rel(root, resolved)
			if err != nil || check == ".." || strings.HasPrefix(check, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("本地链接超出保存目录")
			}
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("找不到保存目录")
		}
		current = parent
	}
	return target, nil
}
func fileURL(t task, f taskFile) string {
	return strings.TrimRight(t.APIBase, "/") + "/" + t.Ticket + "/file/" + strconv.Itoa(f.ID)
}
func client() *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 45 * time.Second, MaxIdleConnsPerHost: 12}, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 3 || r.URL.Host != via[0].URL.Host || r.URL.Scheme != via[0].URL.Scheme {
			return fmt.Errorf("拒绝下载任务跨服务器跳转")
		}
		return nil
	}}
}
func remoteDigest(ctx context.Context, c *http.Client, t task, f taskFile) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL(t, f)+"?checksum=sha256", nil)
	if err != nil {
		return "", err
	}
	response, err := c.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", fmt.Errorf("服务器校验失败 HTTP %d", response.StatusCode)
	}
	var result struct {
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&result); err != nil {
		return "", err
	}
	raw, err := hex.DecodeString(result.SHA256)
	if err != nil || len(raw) != 32 || result.Size != f.Size {
		return "", fmt.Errorf("服务器返回无效校验结果")
	}
	return strings.ToLower(result.SHA256), nil
}
func localDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func fingerprint(t task, f taskFile) string {
	v := fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d", t.APIBase, f.Source, f.Path, f.Size, f.Modified)
	hash := sha256.Sum256([]byte(v))
	return hex.EncodeToString(hash[:])
}

func transfer(ctx context.Context, c *http.Client, t task, f taskFile, root string) error {
	destination, err := safeDestination(root, f.RelativePath)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	var expected string
	if stat, err := os.Stat(destination); err == nil && stat.Mode().IsRegular() && stat.Size() == f.Size {
		expected, err = remoteDigest(ctx, c, t, f)
		if err != nil {
			return err
		}
		actual, err := localDigest(destination)
		if err == nil && actual == expected {
			progress("已校验，跳过：%s", f.RelativePath)
			return nil
		}
	}
	part := destination + ".fbdownload.part"
	statePath := part + ".json"
	for _, p := range []string{part, statePath, statePath + ".new"} {
		if stat, err := os.Lstat(p); err == nil && !stat.Mode().IsRegular() {
			return fmt.Errorf("临时文件路径不是普通文件：%s", p)
		}
	}
	output, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer output.Close()
	hash := sha256.New()
	offset := int64(0)
	state := partialState{}
	if data, err := os.ReadFile(statePath); err == nil {
		if json.Unmarshal(data, &state) == nil && state.Fingerprint == fingerprint(t, f) && state.Bytes >= 0 && state.Bytes <= f.Size {
			if stat, err := output.Stat(); err == nil && stat.Size() == state.Bytes {
				if _, err = io.Copy(hash, output); err == nil && hex.EncodeToString(hash.Sum(nil)) == state.SHA256 {
					offset = state.Bytes
				}
			}
		}
	}
	if offset == 0 {
		hash = sha256.New()
		if err = output.Truncate(0); err != nil {
			return err
		}
	}
	if _, err = output.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	const chunkSize = int64(8 * 1024 * 1024)
	for offset < f.Size {
		end := offset + chunkSize - 1
		if end >= f.Size {
			end = f.Size - 1
		}
		var chunkErr error
		for attempt := 0; attempt < 3; attempt++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			chunkCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			request, err := http.NewRequestWithContext(chunkCtx, http.MethodGet, fileURL(t, f), nil)
			if err != nil {
				cancel()
				return err
			}
			request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
			response, err := c.Do(request)
			if err == nil {
				want := fmt.Sprintf("bytes %d-%d/%d", offset, end, f.Size)
				if response.StatusCode != http.StatusPartialContent || response.Header.Get("Content-Range") != want {
					err = fmt.Errorf("分块响应不匹配 HTTP %d", response.StatusCode)
				} else {
					var n int64
					n, err = io.Copy(io.MultiWriter(output, hash), io.LimitReader(response.Body, end-offset+2))
					if err == nil && n != end-offset+1 {
						err = fmt.Errorf("下载分块不完整")
					}
				}
				response.Body.Close()
			}
			cancel()
			chunkErr = err
			if err == nil {
				break
			}
			output.Truncate(offset)
			output.Seek(0, io.SeekStart)
			hash = sha256.New()
			if _, restoreErr := io.Copy(hash, io.NewSectionReader(output, 0, offset)); restoreErr != nil {
				return restoreErr
			}
			output.Seek(offset, io.SeekStart)
			if attempt < 2 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(attempt+1) * time.Second):
				}
			}
		}
		if chunkErr != nil {
			return chunkErr
		}
		offset = end + 1
		if err = output.Sync(); err != nil {
			return err
		}
		state = partialState{Fingerprint: fingerprint(t, f), Bytes: offset, SHA256: hex.EncodeToString(hash.Sum(nil))}
		data, _ := json.Marshal(state)
		if err = os.WriteFile(statePath+".new", data, 0600); err != nil {
			return err
		}
		if err = os.Rename(statePath+".new", statePath); err != nil {
			return err
		}
		progress("下载：%s  %d/%d 字节", f.RelativePath, offset, f.Size)
	}
	if expected == "" {
		expected, err = remoteDigest(ctx, c, t, f)
		if err != nil {
			return err
		}
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != expected {
		output.Close()
		os.Remove(part)
		os.Remove(statePath)
		return fmt.Errorf("SHA-256 校验失败；原有完整文件未改变，请重试此文件")
	}
	if err = output.Sync(); err != nil {
		return err
	}
	if err = output.Close(); err != nil {
		return err
	}
	// Same-directory rename publishes only a complete, validated file.
	if err = os.Rename(part, destination); err != nil {
		return err
	}
	os.Remove(statePath)
	progress("已完成并校验：%s", f.RelativePath)
	return nil
}

func orderedFiles(input []taskFile) []taskFile {
	groups := map[string][]taskFile{}
	keys := []string{}
	for _, file := range input {
		key := strings.Split(file.RelativePath, "/")[0]
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], file)
	}
	output := []taskFile{}
	for i := 0; len(output) < len(input); i++ {
		for _, key := range keys {
			if i < len(groups[key]) {
				output = append(output, groups[key][i])
			}
		}
	}
	return output
}

func run() error {
	taskPath := flag.String("task", "", "下载任务文件")
	destination := flag.String("destination", "", "本地保存目录")
	workers := flag.Int("workers", 4, "并发文件数（1-8）")
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append([]string{"-task", args[0]}, args[1:]...)
	}
	if err := flag.CommandLine.Parse(args); err != nil {
		return err
	}
	var err error
	if *taskPath == "" {
		*taskPath, err = chooseTask()
		if err != nil {
			return err
		}
	}
	data, err := os.ReadFile(*taskPath)
	if err != nil {
		return err
	}
	if len(data) > 64*1024*1024 {
		return fmt.Errorf("下载任务文件过大")
	}
	var t task
	if err = json.Unmarshal(data, &t); err != nil {
		return err
	}
	server, err := url.Parse(t.APIBase)
	if err != nil || (server.Scheme != "http" && server.Scheme != "https") || server.Host == "" || server.User != nil || server.RawQuery != "" || server.Fragment != "" {
		return fmt.Errorf("下载任务服务器地址无效")
	}
	raw, err := hex.DecodeString(t.Ticket)
	if err != nil || len(raw) != 32 || t.Version != 1 || t.Expires < time.Now().Unix() {
		return fmt.Errorf("下载任务已失效，请在 FileBrowser 中重新选择并下载")
	}
	if *destination == "" {
		*destination, err = chooseDestination()
		if err != nil {
			return err
		}
	}
	root, err := filepath.Abs(*destination)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(root, 0755); err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	ids := map[int]bool{}
	for _, file := range t.Files {
		if file.Size < 0 || file.ID < 0 || ids[file.ID] {
			return fmt.Errorf("下载任务文件信息无效")
		}
		ids[file.ID] = true
		if _, err = safeDestination(root, file.RelativePath); err != nil {
			return err
		}
		key := strings.ToLower(file.RelativePath)
		if names[key] {
			return fmt.Errorf("任务包含同名本地文件 %q", file.RelativePath)
		}
		names[key] = true
	}
	for _, dir := range t.Directories {
		target, err := safeDestination(root, dir)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(target, 0755); err != nil {
			return err
		}
	}
	if *workers < 1 {
		*workers = 1
	}
	if *workers > 8 {
		*workers = 8
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	c := client()
	defer c.CloseIdleConnections()
	progress("开始下载：%d 个文件，%d 路并发，保存到 %s", len(t.Files), *workers, root)
	jobs := make(chan taskFile)
	var group sync.WaitGroup
	var failuresMu sync.Mutex
	failures := map[string]string{}
	for i := 0; i < *workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for file := range jobs {
				if err := transfer(ctx, c, t, file, root); err != nil {
					progress("未完成：%s：%v", file.RelativePath, err)
					failuresMu.Lock()
					failures[file.RelativePath] = err.Error()
					failuresMu.Unlock()
				}
			}
		}()
	}
	for _, file := range orderedFiles(t.Files) {
		if ctx.Err() != nil {
			break
		}
		jobs <- file
	}
	close(jobs)
	group.Wait()
	if ctx.Err() != nil {
		return fmt.Errorf("下载已暂停；重新打开相同任务可以继续")
	}
	if len(failures) > 0 {
		report, _ := json.MarshalIndent(failures, "", "  ")
		os.WriteFile(filepath.Join(root, "FileBrowser-下载失败.json"), report, 0600)
		return fmt.Errorf("%d 个文件未完成；重新打开任务即可重试，已校验文件会跳过", len(failures))
	}
	progress("全部下载完成，SHA-256 校验通过。")
	return nil
}
func main() {
	enableUTF8()
	if err := run(); err != nil {
		progress("%v", err)
		waitBeforeExit()
		os.Exit(1)
	}
	waitBeforeExit()
}
