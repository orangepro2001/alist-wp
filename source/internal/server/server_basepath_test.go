package server

// server_basepath_test.go: 无鉴权 + 路径前缀挂载(/baidu)行为。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"baidupcs-web/internal/config"
)

func newBasePathServer(t *testing.T, base string) *httptest.Server {
	t.Helper()
	cfg, err := config.Load(config.Options{DataDir: t.TempDir(), Listen: "127.0.0.1:0", BasePath: base})
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

func get(t *testing.T, url string) (int, string, http.Header) {
	t.Helper()
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := cl.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b), r.Header
}

// 无口令/会话: /api/* 直接可用。
func TestAPINoAuthRequired(t *testing.T) {
	hs := newBasePathServer(t, "")
	if code, body, _ := get(t, hs.URL+"/api/quota"); code != http.StatusOK {
		t.Fatalf("/api/quota 应 200, got %d %s", code, body)
	}
	// 口令相关接口已移除
	for _, p := range []string{"/api/login", "/api/setup", "/api/password", "/api/logout"} {
		req, _ := http.NewRequest(http.MethodPost, hs.URL+p, strings.NewReader(`{}`))
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		// 未注册路径回落到 SPA index.html(HTML), 不得再返回登录接口的 JSON 结果
		if r.StatusCode == http.StatusUnauthorized || strings.Contains(string(b), `"ok"`) {
			t.Errorf("%s 应已移除, got %d %s", p, r.StatusCode, b)
		}
	}
}

func TestBasePathMount(t *testing.T) {
	hs := newBasePathServer(t, "/baidu")

	if code, _, h := get(t, hs.URL+"/baidu"); code != http.StatusMovedPermanently || h.Get("Location") != "/baidu/" {
		t.Errorf("/baidu 应 301 到 /baidu/, got %d %q", code, h.Get("Location"))
	}
	code, body, _ := get(t, hs.URL+"/baidu/")
	if code != http.StatusOK || !strings.Contains(body, `<base href="/baidu/">`) {
		t.Errorf("/baidu/ 应返回注入 <base> 的 SPA, got %d", code)
	}
	if strings.Contains(body, `"/assets/`) || strings.Contains(body, `"/api/`) {
		t.Error("index.html 不应含根绝对路径资源引用")
	}
	if code, _, _ := get(t, hs.URL+"/baidu/assets/app.js"); code != http.StatusOK {
		t.Errorf("/baidu/assets/app.js 应 200, got %d", code)
	}
	if code, _, _ := get(t, hs.URL+"/baidu/api/quota"); code != http.StatusOK {
		t.Errorf("/baidu/api/quota 应 200, got %d", code)
	}
	// 前缀之外一律 404(直连根路径无内容)
	for _, p := range []string{"/", "/api/quota", "/assets/app.js", "/baiduX/"} {
		if code, _, _ := get(t, hs.URL+p); code != http.StatusNotFound {
			t.Errorf("%s 应 404, got %d", p, code)
		}
	}
}

// WebDAV 随服务装配: 经前缀挂载后可 PROPFIND 到引擎里的文件。
func TestDAVMounted(t *testing.T) {
	hs := newBasePathServer(t, "/baidu")
	req, _ := http.NewRequest("PROPFIND", hs.URL+"/baidu/dav/", nil)
	req.Header.Set("Depth", "1")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 207 || !strings.Contains(string(b), "readme.txt") {
		t.Fatalf("PROPFIND /baidu/dav/ = %d %s", r.StatusCode, b)
	}
}
