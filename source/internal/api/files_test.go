package api

// files_test.go W4: 文件域纯逻辑单测(参数解析/sort-order 映射/响应整形/错误映射/请求体校验)。
// 网络零依赖: 引擎用手工桩(实现 xengine.Engine 接口, 非 struct 内嵌覆写)。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"baidupcs-web/internal/xengine"
)

// stubEngine 手工实现 xengine.Engine, 仅覆盖被测方法, 其余 panic 占位。
type stubEngine struct {
	listDir      string
	listPage     int
	listPageSize int
	listSort     string
	listOrder    string
	listResult   xengine.FileDirectoryList
	listErr      error
	quotaResult  [2]int64
	quotaErr     error
	metaPath     string
	metaResult   *xengine.FileDirectory
	metaErr      error
	searchResult xengine.FileDirectoryList
	searchErr    error
	mkdirPath    string
	mkdirErr     error
	removed      []string
	rmErr        error
	copied       struct {
		from  []string
		toDir string
	}
	cpErr    error
	moved    struct{ from []string; toDir string }
	mvErr    error
}

func (s *stubEngine) List(_ context.Context, dir string, page, pageSize int, orderBy, order string) (xengine.FileDirectoryList, error) {
	s.listDir, s.listPage, s.listPageSize, s.listSort, s.listOrder = dir, page, pageSize, orderBy, order
	return s.listResult, s.listErr
}
func (s *stubEngine) Quota(context.Context) (int64, int64, error) { return s.quotaResult[0], s.quotaResult[1], s.quotaErr }
func (s *stubEngine) Meta(_ context.Context, p string) (*xengine.FileDirectory, error) {
	s.metaPath = p
	return s.metaResult, s.metaErr
}
func (s *stubEngine) Search(context.Context, string, string, bool) (xengine.FileDirectoryList, error) {
	return s.searchResult, s.searchErr
}
func (s *stubEngine) Mkdir(_ context.Context, p string) error { s.mkdirPath = p; return s.mkdirErr }
func (s *stubEngine) Remove(_ context.Context, paths []string) error {
	s.removed = paths
	return s.rmErr
}
func (s *stubEngine) Copy(_ context.Context, from []string, toDir string) error {
	s.copied.from, s.copied.toDir = from, toDir
	return s.cpErr
}
func (s *stubEngine) Move(_ context.Context, from []string, toDir string) error {
	s.moved.from, s.moved.toDir = from, toDir
	return s.mvErr
}

func (s *stubEngine) panicUnimplemented(string) { panic("not implemented in stub") }
func (s *stubEngine) AddAccountByCookie(string) (*xengine.Account, error) {
	s.panicUnimplemented("AddAccountByCookie")
	return nil, nil
}
func (s *stubEngine) AddAccountByBDUSS(string, string) (*xengine.Account, error) {
	s.panicUnimplemented("AddAccountByBDUSS")
	return nil, nil
}
func (s *stubEngine) Accounts() []xengine.Account { return nil }
func (s *stubEngine) ActiveAccount() (xengine.Account, bool) {
	return xengine.Account{UID: 1, Name: "stub"}, true
}
func (s *stubEngine) SwitchAccount(uint64) error   { return nil }
func (s *stubEngine) RemoveAccount(uint64) error   { return nil }
func (s *stubEngine) Rename(context.Context, string, string) error {
	s.panicUnimplemented("Rename")
	return nil
}
func (s *stubEngine) Tree(context.Context, string, int) (xengine.FileDirectoryList, error) {
	s.panicUnimplemented("Tree")
	return nil, nil
}
func (s *stubEngine) Match(context.Context, string) ([]string, error) {
	s.panicUnimplemented("Match")
	return nil, nil
}
func (s *stubEngine) ResolveDlink(context.Context, string) (*xengine.DownloadLink, error) {
	s.panicUnimplemented("ResolveDlink")
	return nil, nil
}
func (s *stubEngine) Export(context.Context, string) (*xengine.RapidLink, error) {
	s.panicUnimplemented("Export")
	return nil, nil
}
func (s *stubEngine) Upload(context.Context, xengine.UploadParam) (xengine.UploadResult, error) {
	s.panicUnimplemented("Upload")
	return xengine.UploadResult{}, nil
}
func (s *stubEngine) ShareSet(context.Context, []string, string, int) (*xengine.Shared, error) {
	s.panicUnimplemented("ShareSet")
	return nil, nil
}
func (s *stubEngine) ShareList(context.Context, int) (xengine.ShareRecordList, error) {
	s.panicUnimplemented("ShareList")
	return nil, nil
}
func (s *stubEngine) ShareCancel(context.Context, []int64) error {
	s.panicUnimplemented("ShareCancel")
	return nil
}
func (s *stubEngine) ParseShareLink(string) (string, string, error) {
	s.panicUnimplemented("ParseShareLink")
	return "", "", nil
}
func (s *stubEngine) SharePreview(context.Context, string, string) (xengine.SharePreviewResult, error) {
	s.panicUnimplemented("SharePreview")
	return xengine.SharePreviewResult{}, nil
}
func (s *stubEngine) Transfer(context.Context, string, string, string, []string) error {
	s.panicUnimplemented("Transfer")
	return nil
}
func (s *stubEngine) TransferToTmp(context.Context, string, string, []string) (*xengine.TmpTransfer, error) {
	s.panicUnimplemented("TransferToTmp")
	return nil, nil
}
func (s *stubEngine) OfflineAdd(context.Context, []string, string) ([]xengine.OfflineTask, []error) {
	s.panicUnimplemented("OfflineAdd")
	return nil, nil
}
func (s *stubEngine) OfflineList(context.Context) (xengine.OfflineTaskList, error) {
	s.panicUnimplemented("OfflineList")
	return nil, nil
}
func (s *stubEngine) OfflineQuery(context.Context, []int64) (xengine.OfflineTaskList, error) {
	s.panicUnimplemented("OfflineQuery")
	return nil, nil
}
func (s *stubEngine) OfflineCancel(context.Context, int64) error {
	s.panicUnimplemented("OfflineCancel")
	return nil
}
func (s *stubEngine) OfflineDelete(context.Context, int64, bool) error {
	s.panicUnimplemented("OfflineDelete")
	return nil
}
func (s *stubEngine) RecycleList(context.Context, int) (xengine.RecycleInfoList, error) {
	s.panicUnimplemented("RecycleList")
	return nil, nil
}
func (s *stubEngine) RecycleRestore(context.Context, []int64) error {
	s.panicUnimplemented("RecycleRestore")
	return nil
}
func (s *stubEngine) RecycleDelete(context.Context, []int64, bool) error {
	s.panicUnimplemented("RecycleDelete")
	return nil
}

// 编译期断言: 桩实现 Engine 全接口(防将来接口扩展后测试悄悄失配)。
var _ xengine.Engine = (*stubEngine)(nil)

func fd(name string, size int64, isdir bool) *xengine.FileDirectory {
	return &xengine.FileDirectory{FsID: size + 7, Path: "/" + name, Filename: name, Size: size, Isdir: isdir, Mtime: 1700000000}
}

func getJSON(t *testing.T, h http.HandlerFunc, target string) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应非 JSON: %v; body=%s", err, rec.Body.String())
	}
	return rec, body
}

func postJSONHandler(t *testing.T, h http.HandlerFunc, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// 用例 1: /api/files 参数解析——sort/order/page/pageSize 逐字透传给引擎(page/pageSize 数值化)。
func TestFilesParamsParsing(t *testing.T) {
	stub := &stubEngine{listResult: xengine.FileDirectoryList{fd("a.txt", 10, false)}}
	api := NewFiles(stub)
	rec, body := getJSON(t, api.HandleList, "/api/files?dir=/docs&page=3&pageSize=50&sort=time&order=desc")
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if stub.listDir != "/docs" || stub.listSort != "time" || stub.listOrder != "desc" {
		t.Errorf("透传不对: dir=%q sort=%q order=%q", stub.listDir, stub.listSort, stub.listOrder)
	}
	if stub.listPage != 3 || stub.listPageSize != 50 {
		t.Errorf("page=%d pageSize=%d, want 3/50", stub.listPage, stub.listPageSize)
	}
	list, _ := body["list"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("list 长度 = %d, want 1", len(list))
	}
	item, _ := list[0].(map[string]interface{})
	if item["name"] != "a.txt" || item["isDir"] != false || item["size"] != float64(10) {
		t.Errorf("list 行字段不对: %+v", item)
	}
}

// 用例 2: sort/order 非法值回落语义——不报错, 按 name/asc 语义透传由引擎回落(参数原样、状态 200)。
func TestFilesSortOrderDefaults(t *testing.T) {
	stub := &stubEngine{}
	api := NewFiles(stub)
	rec, _ := getJSON(t, api.HandleList, "/api/files?dir=/&sort=hack%3Bdrop&order=sidew")
	if rec.Code != http.StatusOK {
		t.Fatalf("非法 sort/order 应回落而非报错, got %d", rec.Code)
	}
	if stub.listSort != "hack;drop" {
		t.Errorf("非法 sort 原样透传给引擎回落: %q", stub.listSort)
	}
}

// 用例 3: dir 缺失 → 400; page 非法数字 → 全量语义(page=0)。
func TestFilesParamErrors(t *testing.T) {
	api := NewFiles(&stubEngine{})
	rec, _ := getJSON(t, api.HandleList, "/api/files")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 dir 应 400, got %d", rec.Code)
	}
	stub := &stubEngine{}
	_, body := getJSON(t, (&filesAPI{eng: stub}).HandleList, "/api/files?dir=/&page=abc&pageSize=xyz")
	if stub.listPage != 0 || stub.listPageSize != 0 {
		t.Errorf("非法分页应取全量(0,0), got (%d,%d)", stub.listPage, stub.listPageSize)
	}
	if _, ok := body["list"]; !ok {
		t.Error("响应应含 list 字段")
	}
}

// 用例 4: xengine.Error → HTTP 状态码/机器码映射表逐项。
func TestStatusOfEngineErrMapping(t *testing.T) {
	cases := []struct {
		code   xengine.ErrCode
		status int
		machine string
	}{
		{xengine.ErrNet, http.StatusBadGateway, "UPSTREAM_ERROR"},
		{xengine.ErrRemote, http.StatusBadGateway, "UPSTREAM_ERROR"},
		{xengine.ErrAuth, http.StatusConflict, "BAIDU_NOT_LINKED"},
		{xengine.ErrNotFound, http.StatusNotFound, "NOT_FOUND"},
		{xengine.ErrQuota, http.StatusInsufficientStorage, "QUOTA_EXCEEDED"},
		{xengine.ErrForbiddenTransfer, http.StatusForbidden, "TRANSFER_FORBIDDEN"},
		{xengine.ErrBadPasscode, http.StatusUnauthorized, "AUTH_BAD_PASSCODE"},
		{xengine.ErrInternal, http.StatusInternalServerError, "INTERNAL"},
	}
	for _, c := range cases {
		status, machine := statusOf(&xengine.Error{Code: c.code, Message: "x"})
		if status != c.status || machine != c.machine {
			t.Errorf("ErrCode %d → (%d,%s), want (%d,%s)", c.code, status, machine, c.status, c.machine)
		}
	}
	if s, m := statusOf(context.DeadlineExceeded); s != http.StatusInternalServerError || m != "INTERNAL" {
		t.Errorf("非引擎错误应落 INTERNAL, got (%d,%s)", s, m)
	}
}

// 用例 5: /api/quota 数值透传; /api/meta 输出整形(isDir/md5)。
func TestQuotaAndMetaShaping(t *testing.T) {
	stub := &stubEngine{quotaResult: [2]int64{100, 30}}
	api := NewFiles(stub)
	rec, body := getJSON(t, api.HandleQuota, "/api/quota")
	if rec.Code != http.StatusOK || body["quota"] != float64(100) || body["used"] != float64(30) {
		t.Fatalf("quota 不对: %d %s", rec.Code, rec.Body.String())
	}
	if body["free"] != float64(70) {
		t.Errorf("free = %v, want 70", body["free"])
	}

	stub.metaResult = fd("b.bin", 2048, false)
	stub.metaResult.MD5 = "deadbeef"
	_, body = getJSON(t, api.HandleMeta, "/api/meta?path=/b.bin")
	if stub.metaPath != "/b.bin" {
		t.Errorf("Meta path 透传 = %q", stub.metaPath)
	}
	meta, _ := body["meta"].(map[string]interface{})
	if meta["md5"] != "deadbeef" || meta["isDir"] != false {
		t.Errorf("meta 整形不对: %+v", meta)
	}
}

// 用例 6: 写操作请求体校验与透传(rm/cp/mv/mkdir)。
func TestWriteOpsParsing(t *testing.T) {
	stub := &stubEngine{}
	api := NewFiles(stub)

	rec := postJSONHandler(t, api.HandleRemove, "/api/files/rm", `{"paths":["/a"," /b ","/a","","rel/path"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rm 应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(stub.removed) != 2 || stub.removed[0] != "/a" || stub.removed[1] != "/b" {
		t.Errorf("rm 去空去重后 = %v", stub.removed)
	}

	rec = postJSONHandler(t, api.HandleRemove, "/api/files/rm", `{"paths":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("空 paths 应 400, got %d", rec.Code)
	}
	rec = postJSONHandler(t, api.HandleRemove, "/api/files/rm", `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("坏 JSON 应 400, got %d", rec.Code)
	}

	rec = postJSONHandler(t, api.HandleCopy, "/api/files/cp", `{"paths":["/a"],"to":"/target"}`)
	if rec.Code != http.StatusOK || stub.copied.toDir != "/target" || len(stub.copied.from) != 1 {
		t.Errorf("cp 不对: %d %+v", rec.Code, stub.copied)
	}
	rec = postJSONHandler(t, api.HandleCopy, "/api/files/cp", `{"paths":["/a"],"to":"rel"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("to 非绝对路径应 400, got %d", rec.Code)
	}

	rec = postJSONHandler(t, api.HandleMove, "/api/files/mv", `{"paths":["/x"],"to":"/dst"}`)
	if rec.Code != http.StatusOK || stub.moved.toDir != "/dst" {
		t.Errorf("mv 不对: %d %+v", rec.Code, stub.moved)
	}

	rec = postJSONHandler(t, api.HandleMkdir, "/api/files/mkdir", `{"dir":"/new dir/子目录"}`)
	if rec.Code != http.StatusOK || stub.mkdirPath != "/new dir/子目录" {
		t.Errorf("mkdir 不对: %d %q", rec.Code, stub.mkdirPath)
	}
	rec = postJSONHandler(t, api.HandleMkdir, "/api/files/mkdir", `{"dir":"relative"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("mkdir 相对路径应 400, got %d", rec.Code)
	}
}

// 用例 7: /api/search 递归默认开 + 响应含所在目录(path 字段可跳转)。
func TestSearchShaping(t *testing.T) {
	stub := &stubEngine{searchResult: xengine.FileDirectoryList{
		{FsID: 1, Path: "/deep/dir/found.mp4", Filename: "found.mp4", Size: 5},
	}}
	api := NewFiles(stub)
	rec, body := getJSON(t, api.HandleSearch, "/api/search?dir=/&keyword=found")
	if rec.Code != http.StatusOK {
		t.Fatalf("search 应 200, got %d", rec.Code)
	}
	list, _ := body["list"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("list 长度 = %d", len(list))
	}
	item, _ := list[0].(map[string]interface{})
	if item["path"] != "/deep/dir/found.mp4" {
		t.Errorf("结果应含所在目录路径: %+v", item)
	}
	if body["keyword"] != "found" {
		t.Errorf("keyword 回显 = %v", body["keyword"])
	}
	rec, _ = getJSON(t, api.HandleSearch, "/api/search?dir=/")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("缺 keyword 应 400, got %d", rec.Code)
	}
}
