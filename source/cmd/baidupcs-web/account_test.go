package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoopbackAddr(t *testing.T) {
	for in, want := range map[string]string{
		"":               "127.0.0.1:8080",
		"0.0.0.0:9000":   "127.0.0.1:9000",
		":9000":          "127.0.0.1:9000",
		"127.0.0.1:8080": "127.0.0.1:8080",
	} {
		if got := loopbackAddr(in); got != want {
			t.Errorf("loopbackAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeServer 模拟 /api/accounts* 并记录最后一次请求。
func fakeServer(t *testing.T) (addr string, last *struct {
	method, path, body string
}) {
	t.Helper()
	last = &struct{ method, path, body string }{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/accounts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"accounts":[{"uid":7,"name":"alice","active":true},{"uid":8,"name":"bob","active":false}],"total":2}`)
	})
	mux.HandleFunc("/api/accounts/add", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		last.method, last.path, last.body = r.Method, r.URL.Path, string(b)
		if strings.Contains(string(b), "bad") {
			w.WriteHeader(409)
			_, _ = io.WriteString(w, `{"code":"X","message":"凭证无效"}`)
			return
		}
		_, _ = io.WriteString(w, `{"uid":9,"name":"carol"}`)
	})
	mux.HandleFunc("/api/accounts/", func(w http.ResponseWriter, r *http.Request) {
		last.method, last.path = r.Method, r.URL.Path
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), last
}

func run(args ...string) (int, string, string) {
	return runWithStdin("", args...)
}

func runWithStdin(stdin string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := runAccount(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestAccountList(t *testing.T) {
	addr, _ := fakeServer(t)
	code, out, errs := run("list", "-addr", addr)
	if code != 0 || !strings.Contains(out, "* 7\talice") || !strings.Contains(out, "  8\tbob") {
		t.Fatalf("list = %d %q %q", code, out, errs)
	}
}

func TestAccountAddCookieViaStdin(t *testing.T) {
	addr, last := fakeServer(t)
	code, out, errs := runWithStdin("BDUSS=abc; STOKEN=def\n", "add", "-addr", addr, "-cookie", "-")
	if code != 0 || !strings.Contains(out, "9\tcarol") {
		t.Fatalf("add = %d %q %q", code, out, errs)
	}
	var body map[string]string
	_ = json.Unmarshal([]byte(last.body), &body)
	if last.method != "POST" || last.path != "/api/accounts/add" || body["cookie"] != "BDUSS=abc; STOKEN=def" {
		t.Fatalf("请求不对: %+v", last)
	}
}

func TestAccountAddBDUSSAndServerError(t *testing.T) {
	addr, last := fakeServer(t)
	if code, _, errs := run("add", "-addr", addr, "-bduss", "B1", "-stoken", "S1"); code != 0 {
		t.Fatalf("add bduss = %d %q", code, errs)
	}
	if !strings.Contains(last.body, `"bduss":"B1"`) || !strings.Contains(last.body, `"stoken":"S1"`) {
		t.Errorf("body = %s", last.body)
	}
	code, _, errs := run("add", "-addr", addr, "-bduss", "bad")
	if code != 1 || !strings.Contains(errs, "凭证无效") {
		t.Fatalf("服务端错误应带 message 并退出 1: %d %q", code, errs)
	}
}

func TestAccountSwitchRmAndUsage(t *testing.T) {
	addr, last := fakeServer(t)
	if code, _, _ := run("switch", "-addr", addr, "8"); code != 0 || last.method != "POST" || last.path != "/api/accounts/8/switch" {
		t.Fatalf("switch: %+v", last)
	}
	if code, _, _ := run("rm", "-addr", addr, "8"); code != 0 || last.method != "DELETE" || last.path != "/api/accounts/8" {
		t.Fatalf("rm: %+v", last)
	}
	if code, _, _ := run("rm", "-addr", addr, "abc"); code != 2 {
		t.Errorf("非法 uid 应退出 2, got %d", code)
	}
	if code, _, _ := run(); code != 2 {
		t.Errorf("无子命令应退出 2, got %d", code)
	}
	if code, _, _ := run("add", "-addr", addr); code != 2 {
		t.Errorf("add 缺参数应退出 2, got %d", code)
	}
	if code, _, errs := run("list", "-addr", "127.0.0.1:1"); code != 1 || !strings.Contains(errs, "连接服务失败") {
		t.Errorf("连不上应退出 1: %d %q", code, errs)
	}
}
