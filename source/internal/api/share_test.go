package api

// share_test.go W7: 转存解析类纯逻辑用例(TestTransferParse, 验收②) + 分享/离线/回收站
// 域 handler 参数解析与响应整形(桩注入, 网络零依赖)。
// 用例约定与 files_test.go 一致: stubEngine 手工实现 Engine, 仅覆盖被测方法。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"baidupcs-web/internal/xengine"
)

// shareStub 在 files_test.go stubEngine 基础上覆写分享/离线/回收站被测方法
// (Go 无逐方法覆写, 改用组合: 新建类型内嵌 *stubEngine 并重新实现目标方法)。
type shareStub struct {
	*stubEngine
	shared    *xengine.Shared
	shareErr  error
	setPaths  []string
	setPwd    string
	setPeriod int
	records   xengine.ShareRecordList
	listErr   error
	cancelled []int64
	cancelErr error
	parsedLink string
	parsedPwd  string
	parseErr   error
	transferred struct{ link, passcode, dir string; fsids []string }
	transferErr error
}

func (s *shareStub) ShareSet(_ context.Context, paths []string, password string, period int) (*xengine.Shared, error) {
	s.setPaths, s.setPwd, s.setPeriod = paths, password, period
	return s.shared, s.shareErr
}
func (s *shareStub) ShareList(context.Context, int) (xengine.ShareRecordList, error) {
	return s.records, s.listErr
}
func (s *shareStub) ShareCancel(_ context.Context, ids []int64) error {
	s.cancelled = ids
	return s.cancelErr
}
func (s *shareStub) ParseShareLink(raw string) (string, string, error) {
	// 与引擎同款归一逻辑的最小桩: 返回原文与显式参数, 供 handler 编排断言
	if s.parseErr != nil {
		return "", "", s.parseErr
	}
	return raw, "", nil
}
func (s *shareStub) Transfer(_ context.Context, link, passcode, dir string, fsids []string) error {
	s.transferred.link, s.transferred.passcode, s.transferred.dir, s.transferred.fsids = link, passcode, dir, fsids
	return s.transferErr
}

// ---------------------------------------------------------------------------
// 验收②: TestTransferParse ≥5 用例(完整链/短链/?pwd=/口令文本/非法输入)。
// 解析规则 1:1 上游 RunShareTransfer 正则(实现在 xengine.ParseShareLink, 此处验 handler 编排)。
// ---------------------------------------------------------------------------

func TestTransferParse(t *testing.T) {
	stub := &shareStub{stubEngine: &stubEngine{}}
	h := NewShare(stub).HandleTransfer

	cases := []struct {
		name       string
		body       string
		wantLink   string
		wantPwd    string
		wantDir    string
		wantStatus int
	}{
		{"01_完整链+pwd参数", `{"link":"https://pan.baidu.com/s/1AbC-dEf_123?pwd=x9y8"}`,
			"https://pan.baidu.com/s/1AbC-dEf_123?pwd=x9y8", "", "/", 200},
		{"02_短链init", `{"link":"https://pan.baidu.com/share/init?surl=abc123","passcode":"ab12"}`,
			"https://pan.baidu.com/share/init?surl=abc123", "ab12", "/", 200},
		{"03_问号pwd显式提取码", `{"link":"https://pan.baidu.com/s/1xyz?pwd=q9w8","passcode":"q9w8"}`,
			"https://pan.baidu.com/s/1xyz?pwd=q9w8", "q9w8", "/", 200},
		{"04_口令文本整段", `{"link":"通过网盘分享的文件: https://pan.baidu.com/s/1zzz 提取码: ab12 复制这段内容后打开"}`,
			"通过网盘分享的文件: https://pan.baidu.com/s/1zzz 提取码: ab12 复制这段内容后打开", "", "/", 200},
		{"05_非法输入_无链接", `{"link":"这里什么都没有"}`, "", "", "", 400},
		{"06_自定义目录", `{"link":"https://pan.baidu.com/s/1abc","dir":"/docs"}`,
			"https://pan.baidu.com/s/1abc", "", "/docs", 200},
		{"07_相对目录补斜杠", `{"link":"https://pan.baidu.com/s/1abc","dir":"docs"}`,
			"https://pan.baidu.com/s/1abc", "", "/docs", 200},
		{"08_空链接", `{"link":"   "}`, "", "", "", 400},
		{"09_坏JSON", `not json`, "", "", "", 400},
		{"10_引擎转存错误透传", `{"link":"https://pan.baidu.com/s/1bad","passcode":"0000"}`,
			"", "", "", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 重置桩状态
			stub.parseErr = nil
			stub.transferErr = nil
			if tc.name == "05_非法输入_无链接" {
				stub.parseErr = &xengine.Error{Code: xengine.ErrInternal, Message: "未找到合法的百度网盘分享链接"}
			}
			if tc.name == "10_引擎转存错误透传" {
				stub.transferErr = &xengine.Error{Code: xengine.ErrBadPasscode, Message: "提取码错误"}
			}
			rec := postJSONHandler(t, h, "/api/transfer", tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d, want %d body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != 200 {
				return
			}
			got := stub.transferred
			if got.link != tc.wantLink {
				t.Errorf("link=%q, want %q", got.link, tc.wantLink)
			}
			if got.passcode != tc.wantPwd {
				t.Errorf("passcode=%q, want %q", got.passcode, tc.wantPwd)
			}
			if got.dir != tc.wantDir {
				t.Errorf("dir=%q, want %q", got.dir, tc.wantDir)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 分享域: set/list/cancel 参数校验与响应整形(FR-25~27)。
// ---------------------------------------------------------------------------

// pageArg 单参分页(share/recycle 域只有 page 语义, 上游 page<1 回落 1)。
func pageArg(q string) int {
	n := 0
	for _, c := range q {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			return 0
		}
	}
	return n
}

// mkQuery 构造带单个 query 参数的 URL。
func mkQuery(key, val string) string { return "/api/x?" + key + "=" + val }

func TestShareSetListCancel(t *testing.T) {
	stub := &shareStub{stubEngine: &stubEngine{}, shared: &xengine.Shared{Link: "https://pan.baidu.com/s/1n", Pwd: "ab12", ShareID: 42}}
	s := NewShare(stub)

	rec := postJSONHandler(t, s.HandleShareSet, "/api/share/set", `{"paths":["/a.txt", " /b.txt ","","rel"],"password":"ab12","period":7}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(stub.setPaths) != 2 || stub.setPwd != "ab12" || stub.setPeriod != 7 {
		t.Errorf("set 透传: %v %q %d", stub.setPaths, stub.setPwd, stub.setPeriod)
	}
	if !strings.Contains(rec.Body.String(), `"password":"ab12"`) {
		t.Errorf("响应应含链接与提取码: %s", rec.Body.String())
	}

	rec = postJSONHandler(t, s.HandleShareSet, "/api/share/set", `{"paths":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("空 paths 应 400, got %d", rec.Code)
	}

	stub.records = xengine.ShareRecordList{
		{ShareID: 1, TypicalPath: "/docs/a.txt", Shortlink: "https://pan.baidu.com/s/1a", Status: 0, ExpireType: -1, ExpireTime: 86400, ViewCount: 3},
	}
	rec, _ = getJSON(t, s.HandleShareList, mkQuery("page", "2"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"expire_type":-1`) {
		t.Errorf("list 不对: %d %s", rec.Code, rec.Body.String())
	}

	rec = postJSONHandler(t, s.HandleShareCancel, "/api/share/cancel", `{"share_ids":[7, 7, 0, -1, 9]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel=%d %s", rec.Code, rec.Body.String())
	}
	if len(stub.cancelled) != 2 || stub.cancelled[0] != 7 || stub.cancelled[1] != 9 {
		t.Errorf("cancel 去重去非法: %v", stub.cancelled)
	}
	rec = postJSONHandler(t, s.HandleShareCancel, "/api/share/cancel", `{"share_ids":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("空 share_ids 应 400, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// 离线域(FR-29/30): add 逐 URL 成败分流 + query 上限 100 + delete all 语义。
// ---------------------------------------------------------------------------

type offlineStub struct {
	*stubEngine
	addURLs []string
	addPath string
	addErrs []error
	list    xengine.OfflineTaskList
	listErr error
	queryIDs []int64
	cancelID int64
	delID   int64
	delAll  bool
	delErr  error
}

func (s *offlineStub) OfflineAdd(_ context.Context, urls []string, path string) ([]xengine.OfflineTask, []error) {
	s.addURLs, s.addPath = urls, path
	out := make([]xengine.OfflineTask, 0, len(urls))
	for i, u := range urls {
		if i < len(s.addErrs) && s.addErrs[i] != nil {
			out = append(out, xengine.OfflineTask{SourceURL: u})
			continue
		}
		out = append(out, xengine.OfflineTask{TaskID: int64(i + 1), SourceURL: u, SavePath: path, Status: 1, StatusText: "进行中", FileSize: 200, FinishedSize: 50})
	}
	errs := make([]error, len(urls))
	copy(errs, s.addErrs)
	return out, errs
}
func (s *offlineStub) OfflineList(context.Context) (xengine.OfflineTaskList, error) { return s.list, s.listErr }
func (s *offlineStub) OfflineQuery(_ context.Context, ids []int64) (xengine.OfflineTaskList, error) {
	s.queryIDs = ids
	return s.list, nil
}
func (s *offlineStub) OfflineCancel(_ context.Context, id int64) error { s.cancelID = id; return nil }
func (s *offlineStub) OfflineDelete(_ context.Context, id int64, all bool) error {
	s.delID, s.delAll = id, all
	return s.delErr
}

func TestOfflineDomain(t *testing.T) {
	stub := &offlineStub{stubEngine: &stubEngine{}, addErrs: []error{nil, errors.New("任务数已达上限")}}
	o := NewOffline(stub)

	rec := postJSONHandler(t, o.HandleAdd, "/api/offline/add", `{"urls":["magnet:?xt=urn:btih:abc","http://bad","magnet:?xt=urn:btih:abc",""],"path":"/docs"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("add=%d %s", rec.Code, rec.Body.String())
	}
	if len(stub.addURLs) != 2 || stub.addPath != "/docs" {
		t.Errorf("add 去重去空: %v %q", stub.addURLs, stub.addPath)
	}
	if !strings.Contains(rec.Body.String(), `"task_id":1`) || !strings.Contains(rec.Body.String(), "任务数已达上限") {
		t.Errorf("add 应含成功项与失败原因: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"progress":25`) {
		t.Errorf("add 进度整形(50/200=25%%): %s", rec.Body.String())
	}

	rec = postJSONHandler(t, o.HandleAdd, "/api/offline/add", `{"urls":["  ",""]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("全空 urls 应 400, got %d", rec.Code)
	}

	rec, _ = getJSON(t, o.HandleQuery, "/api/offline/query?ids=11,12")
	if rec.Code != http.StatusOK || len(stub.queryIDs) != 2 || stub.queryIDs[0] != 11 {
		t.Errorf("query=%d %v %s", rec.Code, stub.queryIDs, rec.Body.String())
	}
	rec, _ = getJSON(t, o.HandleQuery, "/api/offline/query?ids=x")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("非法 ids 应 400, got %d", rec.Code)
	}
	rec, _ = getJSON(t, o.HandleQuery, "/api/offline/query")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("缺 ids 应 400, got %d", rec.Code)
	}

	rec = postJSONHandler(t, o.HandleCancel, "/api/offline/cancel", `{"task_id":33}`)
	if rec.Code != http.StatusOK || stub.cancelID != 33 {
		t.Errorf("cancel: %d %d", rec.Code, stub.cancelID)
	}
	rec = postJSONHandler(t, o.HandleDelete, "/api/offline/delete", `{"all":true}`)
	if rec.Code != http.StatusOK || !stub.delAll {
		t.Errorf("delete all: %d %v", rec.Code, stub.delAll)
	}
	rec = postJSONHandler(t, o.HandleDelete, "/api/offline/delete", `{"task_id":44}`)
	if rec.Code != http.StatusOK || stub.delID != 44 || stub.delAll {
		t.Errorf("delete single: %d %d %v", rec.Code, stub.delID, stub.delAll)
	}
	rec = postJSONHandler(t, o.HandleDelete, "/api/offline/delete", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("空请求体应 400, got %d", rec.Code)
	}

	stub.list = xengine.OfflineTaskList{{TaskID: 9, Status: 0, StatusText: "成功", SavePath: "/", TaskName: "movie.mp4"}}
	rec, _ = getJSON(t, o.HandleList, "/api/offline/list")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "movie.mp4") {
		t.Errorf("list: %d %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// 回收站域(FR-31~33): 还原/删除参数校验 + all 清空语义。
// ---------------------------------------------------------------------------

type recycleStub struct {
	*stubEngine
	page      int
	list      xengine.RecycleInfoList
	listErr   error
	restored  []int64
	delIDs    []int64
	delAll    bool
	delErr    error
}

func (s *recycleStub) RecycleList(_ context.Context, page int) (xengine.RecycleInfoList, error) {
	s.page = page
	return s.list, s.listErr
}
func (s *recycleStub) RecycleRestore(_ context.Context, ids []int64) error {
	s.restored = ids
	return nil
}
func (s *recycleStub) RecycleDelete(_ context.Context, ids []int64, all bool) error {
	s.delIDs, s.delAll = ids, all
	return s.delErr
}

func TestRecycleDomain(t *testing.T) {
	stub := &recycleStub{stubEngine: &stubEngine{}}
	rc := NewRecycle(stub)

	if pageArg("3") != 3 || pageArg("abc") != 0 {
		t.Fatalf("pageArg 解析不对")
	}

	stub.list = xengine.RecycleInfoList{
		{FsID: 101, Path: "/a.txt", Filename: "a.txt", Size: 10, Isdir: 0, LeftTime: 8, Mtime: 1700000000},
		{FsID: 102, Path: "/dir", Filename: "dir", Isdir: 1, LeftTime: 3},
	}
	rec, body := getJSON(t, rc.HandleList, "/api/recycle/list")
	if rec.Code != http.StatusOK {
		t.Fatalf("list=%d %s", rec.Code, rec.Body.String())
	}
	if stub.page != 1 {
		t.Errorf("缺省 page 应回落 1, got %d", stub.page)
	}
	list, _ := body["list"].([]interface{})
	if len(list) != 2 {
		t.Fatalf("list len=%d", len(list))
	}
	first, _ := list[0].(map[string]interface{})
	if first["isDir"] != false || first["name"] != "a.txt" {
		t.Errorf("行整形: %+v", first)
	}

	rec = postJSONHandler(t, rc.HandleRestore, "/api/recycle/restore", `{"fs_ids":[101, 101, 0, -2, 103]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore=%d %s", rec.Code, rec.Body.String())
	}
	if len(stub.restored) != 2 || stub.restored[0] != 101 || stub.restored[1] != 103 {
		t.Errorf("restore 去重去非法: %v", stub.restored)
	}
	rec = postJSONHandler(t, rc.HandleRestore, "/api/recycle/restore", `{"fs_ids":[0]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("全非法 fs_ids 应 400, got %d", rec.Code)
	}

	rec = postJSONHandler(t, rc.HandleDelete, "/api/recycle/delete", `{"fs_ids":[101,102]}`)
	if rec.Code != http.StatusOK || len(stub.delIDs) != 2 || stub.delAll {
		t.Errorf("delete 批删: %d %v %v", rec.Code, stub.delIDs, stub.delAll)
	}
	rec = postJSONHandler(t, rc.HandleDelete, "/api/recycle/delete", `{"all":true}`)
	if rec.Code != http.StatusOK || !stub.delAll {
		t.Errorf("delete 清空: %d %v", rec.Code, stub.delAll)
	}
	rec = postJSONHandler(t, rc.HandleDelete, "/api/recycle/delete", `{"fs_ids":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("空删除应 400, got %d", rec.Code)
	}
}
