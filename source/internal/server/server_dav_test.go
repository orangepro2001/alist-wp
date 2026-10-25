package server

// server_dav_test.go: 无鉴权装配 + WebDAV/账号路由随服务挂载; 已删除的页面与接口不再存在。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"baidupcs-web/internal/config"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg, err := config.Load(config.Options{DataDir: t.TempDir(), Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	srv, err := NewWithEngine(Options{Config: cfg, Version: "test"}, newStubFilesEngine())
	if err != nil {
		t.Fatalf("NewWithEngine: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return hs
}

func do(t *testing.T, method, url string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

func TestHealthAndAccountsNoAuth(t *testing.T) {
	hs := newTestServer(t)
	if code, body := do(t, "GET", hs.URL+"/api/health", nil); code != 200 || !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("health = %d %s", code, body)
	}
	if code, body := do(t, "GET", hs.URL+"/api/accounts", nil); code != 200 || !strings.Contains(body, `"accounts"`) {
		t.Fatalf("accounts = %d %s", code, body)
	}
}

// WebDAV 随服务装配: 可 PROPFIND 到引擎里的文件。
func TestDAVMounted(t *testing.T) {
	hs := newTestServer(t)
	code, body := do(t, "PROPFIND", hs.URL+"/dav/", map[string]string{"Depth": "1"})
	if code != 207 || !strings.Contains(body, "readme.txt") {
		t.Fatalf("PROPFIND /dav/ = %d %s", code, body)
	}
}

// 页面与旧接口已移除: 不再有 SPA/口令/文件 API。
func TestRemovedSurfaces(t *testing.T) {
	hs := newTestServer(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/"}, {"GET", "/assets/app.js"}, {"GET", "/api/files"}, {"GET", "/api/quota"},
		{"POST", "/api/login"}, {"POST", "/api/upload/init"}, {"GET", "/api/events"},
	} {
		if code, _ := do(t, c.method, hs.URL+c.path, nil); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s 应 404/405, got %d", c.method, c.path, code)
		}
	}
}
