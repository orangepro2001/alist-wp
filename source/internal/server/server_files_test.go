// server_files_test.go W4 冒烟: 桩 Engine 注入装配, 验证 /api/files 与 /api/quota
// 登录后全链路(§11 W4 验收: 桩数据 curl)。
//
// 打桩纪律: engine 依赖经 Server 的可选注入钩子(FilesEngine)替换, 不用 struct 内嵌覆写;
// 桩实现为独立类型 stubFilesEngine(实现 xengine.Engine 其余方法报错, 见 newStubFilesEngine)。

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFilesSmokeWithStubEngine(t *testing.T) {
	srv := newFilesSmokeServer(t, newStubFilesEngine())
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	// GET / → 200 HTML
	get := func(url string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	index := get(hs.URL + "/")
	body := readAll(t, index)
	index.Body.Close()
	if index.StatusCode != http.StatusOK || index.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("GET / 应 200 text/html, got %d %s", index.StatusCode, index.Header.Get("Content-Type"))
	}
	for _, want := range []string{"#/files", "assets/app.js", "topbar"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("SPA 壳缺少 %q", want)
		}
	}

	// /api/files → 200 JSON 含 list
	fr := get(hs.URL + "/api/files?dir=/&sort=name&order=asc&page=1&pageSize=50")
	fb := readAll(t, fr)
	fr.Body.Close()
	if fr.StatusCode != http.StatusOK {
		t.Fatalf("/api/files 应 200, got %d: %s", fr.StatusCode, fb)
	}
	var filesResp struct {
		List []map[string]interface{} `json:"list"`
		Dir  string                   `json:"dir"`
	}
	if err := json.Unmarshal(fb, &filesResp); err != nil {
		t.Fatalf("/api/files 非 JSON: %v: %s", err, fb)
	}
	if filesResp.Dir != "/" || len(filesResp.List) == 0 {
		t.Fatalf("files 响应不对: dir=%q list=%d", filesResp.Dir, len(filesResp.List))
	}
	first := filesResp.List[0]
	for _, key := range []string{"name", "size", "isDir", "mtime"} {
		if _, ok := first[key]; !ok {
			t.Errorf("list 行缺少字段 %q", key)
		}
	}

	// /api/quota → 200 含 quota/used
	qr := get(hs.URL + "/api/quota")
	qb := readAll(t, qr)
	qr.Body.Close()
	if qr.StatusCode != http.StatusOK {
		t.Fatalf("/api/quota 应 200, got %d: %s", qr.StatusCode, qb)
	}
	var quotaResp map[string]float64
	if err := json.Unmarshal(qb, &quotaResp); err != nil {
		t.Fatalf("/api/quota 非 JSON: %v", err)
	}
	if quotaResp["quota"] == 0 || quotaResp["used"] == 0 {
		t.Errorf("quota/used 不对: %v", quotaResp)
	}
}
