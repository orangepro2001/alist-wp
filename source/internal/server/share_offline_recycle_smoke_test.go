package server

// share_offline_recycle_smoke_test.go W7 冒烟: 桩 Engine 注入装配, 验证分享/转存/
// 离线/回收站域路由登录后全链路(§11 W7: 桩数据; handler 单测见 internal/api)。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"baidupcs-web/internal/xengine"
)

// stubW7Engine 在文件域桩基础上覆写 W7 域方法(独立类型, 组合 stubFilesEngine)。
type stubW7Engine struct {
	*stubFilesEngine
}

func (s *stubW7Engine) ShareSet(context.Context, []string, string, int) (*xengine.Shared, error) {
	return &xengine.Shared{Link: "https://pan.baidu.com/s/1stub", Pwd: "ab12", ShareID: 7}, nil
}
func (s *stubW7Engine) ShareList(context.Context, int) (xengine.ShareRecordList, error) {
	return xengine.ShareRecordList{
		{ShareID: 7, TypicalPath: "/docs/a.txt", Shortlink: "https://pan.baidu.com/s/1stub", Status: 0, ExpireType: 0, ExpireTime: 86400, ViewCount: 2},
	}, nil
}
func (s *stubW7Engine) ShareCancel(context.Context, []int64) error { return nil }
func (s *stubW7Engine) ParseShareLink(raw string) (string, string, error) {
	return raw, "", nil
}
func (s *stubW7Engine) Transfer(context.Context, string, string, string, []string) error {
	return nil
}
func (s *stubW7Engine) OfflineAdd(_ context.Context, urls []string, path string) ([]xengine.OfflineTask, []error) {
	out := make([]xengine.OfflineTask, 0, len(urls))
	for i, u := range urls {
		out = append(out, xengine.OfflineTask{TaskID: int64(i + 1), SourceURL: u, SavePath: path, Status: 1, StatusText: "进行中", FileSize: 100, FinishedSize: 25})
	}
	return out, make([]error, len(urls))
}
func (s *stubW7Engine) OfflineList(context.Context) (xengine.OfflineTaskList, error) {
	return xengine.OfflineTaskList{{TaskID: 1, Status: 1, StatusText: "进行中", SavePath: "/"}}, nil
}
func (s *stubW7Engine) OfflineQuery(context.Context, []int64) (xengine.OfflineTaskList, error) {
	return xengine.OfflineTaskList{{TaskID: 1, Status: 1, StatusText: "进行中", SavePath: "/"}}, nil
}
func (s *stubW7Engine) OfflineCancel(context.Context, int64) error   { return nil }
func (s *stubW7Engine) OfflineDelete(context.Context, int64, bool) error { return nil }
func (s *stubW7Engine) RecycleList(context.Context, int) (xengine.RecycleInfoList, error) {
	return xengine.RecycleInfoList{{FsID: 101, Path: "/a.txt", Filename: "a.txt", Size: 10, LeftTime: 8}}, nil
}
func (s *stubW7Engine) RecycleRestore(context.Context, []int64) error { return nil }
func (s *stubW7Engine) RecycleDelete(context.Context, []int64, bool) error { return nil }

var _ xengine.Engine = (*stubW7Engine)(nil)

func TestW7DomainsSmokeWithStubEngine(t *testing.T) {
	srv := newFilesSmokeServer(t, &stubW7Engine{stubFilesEngine: newStubFilesEngine()})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	call := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		return r.StatusCode, string(readAll(t, r))
	}

	type tc struct {
		name, method, path, body, wantSub string
	}
	cases := []tc{
		{"分享创建", "POST", "/api/share/set", `{"paths":["/docs/a.txt"],"password":"ab12","period":7}`, `"share_id":7`},
		{"分享列表", "GET", "/api/share/list?page=1", "", `"typical_path":"/docs/a.txt"`},
		{"分享取消", "POST", "/api/share/cancel", `{"share_ids":[7]}`, `"cancelled":[7]`},
		{"转存", "POST", "/api/transfer", `{"link":"https://pan.baidu.com/s/1abc?pwd=x9y8"}`, `"dir":"/"`},
		{"离线添加", "POST", "/api/offline/add", `{"urls":["magnet:?xt=urn:btih:abc"],"path":"/"}`, `"task_id":1`},
		{"离线列表", "GET", "/api/offline/list", "", `"status_text":"进行中"`},
		{"离线查询", "GET", "/api/offline/query?ids=1", "", `"task_id":1`},
		{"离线取消", "POST", "/api/offline/cancel", `{"task_id":1}`, `"ok":true`},
		{"离线删除", "POST", "/api/offline/delete", `{"task_id":1}`, `"ok":true`},
		{"回收站列表", "GET", "/api/recycle/list?page=1", "", `"fs_id":101`},
		{"回收站还原", "POST", "/api/recycle/restore", `{"fs_ids":[101]}`, `"restored":[101]`},
		{"回收站删除", "POST", "/api/recycle/delete", `{"fs_ids":[101]}`, `"deleted":[101]`},
		{"回收站清空", "POST", "/api/recycle/delete", `{"all":true}`, `"all":true`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := call(c.method, c.path, c.body)
			if status != http.StatusOK {
				t.Fatalf("%s %s → %d body=%s", c.method, c.path, status, body)
			}
			if !strings.Contains(body, c.wantSub) {
				t.Errorf("%s 响应缺 %q: %s", c.path, c.wantSub, body)
			}
		})
	}
}
