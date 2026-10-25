package davfs

// davfs_test.go: 用内存桩 Engine/Uploader/Downloader 走通 WebDAV 全部方法(httptest, 无网络)。
// 纪律: 桩为独立类型, 经 Config 字段注入, 不靠内嵌覆写方法。

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"baidupcs-web/internal/tempmgr"
	"baidupcs-web/internal/xengine"
	"baidupcs-web/internal/xfer"
)

type memEntry struct {
	dir  bool
	data []byte
}

// memEngine 内存网盘。
type memEngine struct {
	mu    sync.Mutex
	files map[string]*memEntry
	metas int // Meta 调用次数(验证缓存)
}

func newMemEngine() *memEngine {
	return &memEngine{files: map[string]*memEntry{
		"/":           {dir: true},
		"/docs":       {dir: true},
		"/docs/a.txt": {data: []byte("hello world")},
		"/big.bin":    {data: []byte(strings.Repeat("0123456789", 100))},
		"/docs/sub":   {dir: true},
		"/docs/sub/x": {data: []byte("x")},
	}}
}

func notFound() error { return &xengine.Error{Code: xengine.ErrNotFound, Message: "不存在"} }

func fdOf(p string, e *memEntry) *xengine.FileDirectory {
	return &xengine.FileDirectory{Path: p, Filename: path.Base(p), Size: int64(len(e.data)), Mtime: 1700000000, Isdir: e.dir}
}

func (m *memEngine) Meta(_ context.Context, p string) (*xengine.FileDirectory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metas++
	e, ok := m.files[p]
	if !ok {
		return nil, notFound()
	}
	return fdOf(p, e), nil
}

func (m *memEngine) List(_ context.Context, dir string, _, _ int, _, _ string) (xengine.FileDirectoryList, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.files[dir]; !ok || !e.dir {
		return nil, notFound()
	}
	var names []string
	for p := range m.files {
		if p != "/" && path.Dir(p) == dir {
			names = append(names, p)
		}
	}
	sort.Strings(names)
	var out xengine.FileDirectoryList
	for _, p := range names {
		out = append(out, fdOf(p, m.files[p]))
	}
	return out, nil
}

func (m *memEngine) Mkdir(_ context.Context, p string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[p] = &memEntry{dir: true}
	return nil
}

func (m *memEngine) Remove(_ context.Context, paths []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range paths {
		for k := range m.files {
			if k == p || strings.HasPrefix(k, p+"/") {
				delete(m.files, k)
			}
		}
	}
	return nil
}

func (m *memEngine) transfer(from []string, toDir string, move bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range from {
		if _, ok := m.files[f]; !ok {
			return notFound()
		}
		dst := path.Join(toDir, path.Base(f))
		for k, e := range m.files {
			if k == f || strings.HasPrefix(k, f+"/") {
				m.files[dst+strings.TrimPrefix(k, f)] = &memEntry{dir: e.dir, data: e.data}
				if move {
					delete(m.files, k)
				}
			}
		}
	}
	return nil
}

func (m *memEngine) Copy(_ context.Context, from []string, toDir string) error {
	return m.transfer(from, toDir, false)
}
func (m *memEngine) Move(_ context.Context, from []string, toDir string) error {
	return m.transfer(from, toDir, true)
}
func (m *memEngine) Rename(_ context.Context, from, to string) error {
	m.mu.Lock()
	_, ok := m.files[from]
	m.mu.Unlock()
	if !ok {
		return notFound()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, e := range m.files {
		if k == from || strings.HasPrefix(k, from+"/") {
			delete(m.files, k)
			m.files[to+strings.TrimPrefix(k, from)] = e
		}
	}
	return nil
}

// memUploader 把临时文件内容写进 memEngine; failWith 非空则任务失败。
type memUploader struct {
	eng      *memEngine
	mu       sync.Mutex
	rows     map[string]xfer.TaskRow
	n        int
	failWith string
	policy   string
	renamed  bool
}

func (u *memUploader) EnqueueUploadWithRename(_ context.Context, tempPath, savePath, policy string, _ bool, renamer func(string) (string, error)) (string, error) {
	u.mu.Lock()
	u.n++
	id := "t" + string(rune('0'+u.n))
	u.policy = policy
	u.mu.Unlock()
	final := tempPath
	if np, err := renamer(id); err == nil {
		final = np
		u.mu.Lock()
		u.renamed = true
		u.mu.Unlock()
	}
	go func() {
		time.Sleep(20 * time.Millisecond) // 让 waitTerminal 至少轮询到一次 running
		row := xfer.TaskRow{ID: id, State: "done"}
		if u.failWith != "" {
			row.State, row.Err = "failed", u.failWith
		} else {
			data, _ := os.ReadFile(final)
			u.eng.mu.Lock()
			u.eng.files[savePath] = &memEntry{data: data}
			u.eng.mu.Unlock()
		}
		u.mu.Lock()
		u.rows[id] = row
		u.mu.Unlock()
	}()
	u.mu.Lock()
	u.rows[id] = xfer.TaskRow{ID: id, State: "running"}
	u.mu.Unlock()
	return id, nil
}

func (u *memUploader) RenameTemp(taskID, tempPath string) (string, error) {
	np := path.Join(path.Dir(tempPath), "uptask-"+taskID+"-"+path.Base(tempPath))
	return np, os.Rename(tempPath, np)
}

func (u *memUploader) Row(id string) (xfer.TaskRow, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	r, ok := u.rows[id]
	return r, ok
}

// memDownloader 用 http.ServeContent 提供 Range 语义。
type memDownloader struct{ eng *memEngine }

func (d memDownloader) ServeDownloadTrusted(w http.ResponseWriter, r *http.Request, p string) error {
	d.eng.mu.Lock()
	e, ok := d.eng.files[p]
	d.eng.mu.Unlock()
	if !ok {
		return &xfer.ProxyError{Status: 404, Msg: "不存在"}
	}
	http.ServeContent(w, r, path.Base(p), time.Unix(1700000000, 0), strings.NewReader(string(e.data)))
	return nil
}

type env struct {
	srv *httptest.Server
	eng *memEngine
	up  *memUploader
}

func newEnv(t *testing.T) *env {
	t.Helper()
	tm, err := tempmgr.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eng := newMemEngine()
	up := &memUploader{eng: eng, rows: map[string]xfer.TaskRow{}}
	h := New(Config{Engine: eng, Uploads: up, Temp: tm, Download: memDownloader{eng}, Prefix: "/dav", Poll: 5 * time.Millisecond})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &env{srv: srv, eng: eng, up: up}
}

func (e *env) do(t *testing.T, method, p, body string, hdr map[string]string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+p, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func (e *env) has(p string) bool {
	e.eng.mu.Lock()
	defer e.eng.mu.Unlock()
	_, ok := e.eng.files[p]
	return ok
}

func TestPropfindRootAndDir(t *testing.T) {
	e := newEnv(t)
	code, body, _ := e.do(t, "PROPFIND", "/dav/", "", map[string]string{"Depth": "1"})
	if code != 207 {
		t.Fatalf("PROPFIND / = %d %s", code, body)
	}
	for _, want := range []string{"/dav/docs/", "/dav/big.bin", "<D:collection", "octet-stream"} {
		if !strings.Contains(body, want) {
			t.Errorf("根列表缺 %q:\n%s", want, body)
		}
	}
	code, body, _ = e.do(t, "PROPFIND", "/dav/docs/", "", map[string]string{"Depth": "1"})
	if code != 207 || !strings.Contains(body, "a.txt") || !strings.Contains(body, "sub") {
		t.Fatalf("PROPFIND /docs = %d %s", code, body)
	}
}

func TestPropfindFileAndMissing(t *testing.T) {
	e := newEnv(t)
	code, body, _ := e.do(t, "PROPFIND", "/dav/docs/a.txt", "", map[string]string{"Depth": "0"})
	if code != 207 || !strings.Contains(body, "<D:getcontentlength>11<") || !strings.Contains(body, "text/plain") {
		t.Fatalf("PROPFIND file = %d %s", code, body)
	}
	if code, _, _ := e.do(t, "PROPFIND", "/dav/nope", "", map[string]string{"Depth": "0"}); code != 404 {
		t.Errorf("不存在应 404, got %d", code)
	}
}

func TestDirCacheAvoidsMetaStorm(t *testing.T) {
	e := newEnv(t)
	e.do(t, "PROPFIND", "/dav/docs/", "", map[string]string{"Depth": "1"})
	if e.eng.metas > 1 {
		t.Errorf("depth=1 列目录后逐项 Stat 不应再打 Meta, 实际 Meta 调用 %d 次", e.eng.metas)
	}
	before := e.eng.metas
	e.do(t, "PROPFIND", "/dav/docs/", "", map[string]string{"Depth": "1"})
	if e.eng.metas != before {
		t.Errorf("缓存期内重复 PROPFIND 不应再调 Meta: %d -> %d", before, e.eng.metas)
	}
}

func TestGetHeadRange(t *testing.T) {
	e := newEnv(t)
	code, body, _ := e.do(t, "GET", "/dav/docs/a.txt", "", nil)
	if code != 200 || body != "hello world" {
		t.Fatalf("GET = %d %q", code, body)
	}
	code, body, h := e.do(t, "GET", "/dav/docs/a.txt", "", map[string]string{"Range": "bytes=6-"})
	if code != 206 || body != "world" || !strings.HasPrefix(h.Get("Content-Range"), "bytes 6-10/11") {
		t.Fatalf("Range GET = %d %q %v", code, body, h)
	}
	code, body, h = e.do(t, "HEAD", "/dav/big.bin", "", nil)
	if code != 200 || body != "" || h.Get("Content-Length") != "1000" || h.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("HEAD = %d %q %v", code, body, h)
	}
	if code, _, _ := e.do(t, "GET", "/dav/nope", "", nil); code != 404 {
		t.Errorf("GET 不存在应 404, got %d", code)
	}
	if code, _, _ := e.do(t, "GET", "/dav/docs/", "", nil); code != 405 {
		t.Errorf("GET 目录应 405, got %d", code)
	}
}

func TestPutNewOverwriteAndErrors(t *testing.T) {
	e := newEnv(t)
	if code, _, h := e.do(t, "PUT", "/dav/docs/new.txt", "fresh", nil); code != 201 || h.Get("ETag") == "" {
		t.Fatalf("PUT 新文件 = %d etag=%q", code, h.Get("ETag"))
	}
	if string(e.eng.files["/docs/new.txt"].data) != "fresh" {
		t.Fatal("内容未进入网盘")
	}
	if !e.up.renamed || e.up.policy != "overwrite" {
		t.Errorf("应走 RenameTemp 且策略为 overwrite: renamed=%v policy=%q", e.up.renamed, e.up.policy)
	}
	if code, _, _ := e.do(t, "PUT", "/dav/docs/new.txt", "v2", nil); code != 204 {
		t.Fatalf("PUT 覆盖应 204, got %d", code)
	}
	if string(e.eng.files["/docs/new.txt"].data) != "v2" {
		t.Fatal("覆盖内容不对")
	}
	if code, _, _ := e.do(t, "PUT", "/dav/nodir/f.txt", "x", nil); code != 409 {
		t.Errorf("父目录不存在应 409, got %d", code)
	}
	if code, _, _ := e.do(t, "PUT", "/dav/docs", "x", nil); code != 405 {
		t.Errorf("PUT 到目录应 405, got %d", code)
	}
}

func TestPutUploadFailureSurfaces(t *testing.T) {
	e := newEnv(t)
	e.up.failWith = "配额不足"
	code, body, _ := e.do(t, "PUT", "/dav/docs/f.txt", "data", nil)
	if code != 502 || !strings.Contains(body, "配额不足") {
		t.Fatalf("上传失败应 502 并带原因, got %d %q", code, body)
	}
	if e.has("/docs/f.txt") {
		t.Error("失败任务不应写入网盘")
	}
}

func TestMkcolDelete(t *testing.T) {
	e := newEnv(t)
	if code, _, _ := e.do(t, "MKCOL", "/dav/newdir", "", nil); code != 201 || !e.has("/newdir") {
		t.Fatalf("MKCOL = %d", code)
	}
	if code, _, _ := e.do(t, "MKCOL", "/dav/newdir", "", nil); code != 405 {
		t.Errorf("重复 MKCOL 应 405, got %d", code)
	}
	if code, _, _ := e.do(t, "MKCOL", "/dav/a/b/c", "", nil); code != 409 {
		t.Errorf("父目录缺失 MKCOL 应 409, got %d", code)
	}
	if code, _, _ := e.do(t, "DELETE", "/dav/docs", "", nil); code != 204 || e.has("/docs") || e.has("/docs/a.txt") {
		t.Fatalf("DELETE 目录 = %d", code)
	}
	if code, _, _ := e.do(t, "DELETE", "/dav/docs", "", nil); code != 404 {
		t.Errorf("DELETE 不存在应 404, got %d", code)
	}
}

func TestMove(t *testing.T) {
	e := newEnv(t)
	dst := e.srv.URL + "/dav/docs/b.txt"
	if code, _, _ := e.do(t, "MOVE", "/dav/docs/a.txt", "", map[string]string{"Destination": dst}); code != 201 {
		t.Fatalf("同目录改名 = %d", code)
	}
	if e.has("/docs/a.txt") || !e.has("/docs/b.txt") {
		t.Fatal("改名结果不对")
	}
	dst = e.srv.URL + "/dav/docs/sub/c.txt"
	if code, _, _ := e.do(t, "MOVE", "/dav/docs/b.txt", "", map[string]string{"Destination": dst}); code != 201 {
		t.Fatalf("跨目录移动+改名 = %d", code)
	}
	if e.has("/docs/b.txt") || !e.has("/docs/sub/c.txt") || string(e.eng.files["/docs/sub/c.txt"].data) != "hello world" {
		t.Fatal("移动结果不对")
	}
}

func TestCopy(t *testing.T) {
	e := newEnv(t)
	dst := e.srv.URL + "/dav/docs/sub/a.txt"
	if code, _, _ := e.do(t, "COPY", "/dav/docs/a.txt", "", map[string]string{"Destination": dst}); code != 201 {
		t.Fatalf("COPY = %d", code)
	}
	if !e.has("/docs/a.txt") || !e.has("/docs/sub/a.txt") {
		t.Fatal("复制后源与目标都应存在")
	}
	if code, _, _ := e.do(t, "COPY", "/dav/docs/a.txt", "", map[string]string{"Destination": dst, "Overwrite": "F"}); code != 412 {
		t.Errorf("Overwrite:F 且目标存在应 412, got %d", code)
	}
	if code, _, _ := e.do(t, "COPY", "/dav/docs/a.txt", "", map[string]string{"Destination": dst}); code != 204 {
		t.Errorf("覆盖复制应 204, got %d", code)
	}
	renamed := e.srv.URL + "/dav/docs/sub/other.txt"
	if code, _, _ := e.do(t, "COPY", "/dav/docs/a.txt", "", map[string]string{"Destination": renamed}); code != 501 {
		t.Errorf("复制并改名应 501, got %d", code)
	}
	if code, _, _ := e.do(t, "COPY", "/dav/docs/a.txt", "", map[string]string{"Destination": "http://evil.example/dav/x/a.txt"}); code != 502 {
		t.Errorf("跨主机应 502, got %d", code)
	}
}

func TestMapErr(t *testing.T) {
	if !errors.Is(mapErr(&xengine.Error{Code: xengine.ErrNotFound}), os.ErrNotExist) {
		t.Error("ErrNotFound 应映射为 os.ErrNotExist")
	}
	other := &xengine.Error{Code: xengine.ErrNet}
	if mapErr(other) != error(other) {
		t.Error("其他错误应原样返回")
	}
}
