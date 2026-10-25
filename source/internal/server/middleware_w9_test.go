package server

// middleware_w9_test.go W9: 限速(FR-39② 10/min/IP)与凭证脱敏(FR-38)单测。
// 纪律: 定时用例注入短窗口(clock stub), 不真实等待。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fixedClock 可步进的假时钟。
type fixedClock struct{ t time.Time }

func (c *fixedClock) Now() time.Time { return c.t }
func (c *fixedClock) Add(d time.Duration) { c.t = c.t.Add(d) }

// TestRateLimitWindow 11 次请求第 11 次拒绝; 窗口滑过后恢复(短窗口注入)。
func TestRateLimitWindow(t *testing.T) {
	clk := &fixedClock{t: time.Now()}
	restore := setupRateLimitersForTest(RateLimitConfig{Limit: 3, Window: 200 * time.Millisecond, NowFunc: clk.Now})
	defer restore()

	rl := rateLimiters["/api/upload/"]
	for i := 0; i < 3; i++ {
		if !rl.allow("10.1.1.1") {
			t.Fatalf("第 %d 次请求不应被限", i+1)
		}
	}
	if rl.allow("10.1.1.1") {
		t.Fatal("超限请求应被拒绝")
	}
	// 不同 IP 互不影响
	if !rl.allow("10.1.1.2") {
		t.Fatal("其他 IP 不应受影响")
	}
	// 窗口滑过 → 恢复
	clk.Add(250 * time.Millisecond)
	if !rl.allow("10.1.1.1") {
		t.Fatal("窗口滑过后应恢复放行")
	}
}

// TestRateLimitMiddleware429 中间件对 upload 超限返回 429 RATE_LIMITED(FR-39② 验收③)。
func TestRateLimitMiddleware429(t *testing.T) {
	restore := setupRateLimitersForTest(RateLimitConfig{Limit: 2, Window: time.Minute})
	defer restore()

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	h := rateLimitMiddleware(rateLimiters)(next)

	req := httptest.NewRequest("POST", "/api/upload/init", nil)
	req.RemoteAddr = "9.9.9.9:5555"
	if code := hit(h, req); code != 200 {
		t.Fatalf("第 1 次 = %d, want 200", code)
	}
	req2 := req.Clone(req.Context())
	if code := hit(h, req2); code != 200 {
		t.Fatalf("第 2 次 = %d, want 200", code)
	}
	req3 := req.Clone(req.Context())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req3)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "RATE_LIMITED") {
		t.Fatalf("第 3 次应 429 RATE_LIMITED, got %d %s", rec.Code, rec.Body.String())
	}
}

func hit(h http.Handler, r *http.Request) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code
}

// TestMaskSecret FR-38: 前 4 后 4; 短值全掩码; 空值不变。
func TestMaskSecret(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"abc", "****"},
		{"12345678901", "****"},            // 11 字符(<12)全掩码
		{"ABCDEFGHIJKLMNOP", "ABCD****MNOP"}, // 16 字符前4后4
	}
	for _, c := range cases {
		if got := MaskSecret(c.in); got != c.want {
			t.Errorf("MaskSecret(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSanitizeLogText FR-38: 日志中 BDUSS/STOKEN/Cookie/passcode 不出现明文。
func TestSanitizeLogText(t *testing.T) {
	const bduss = "ABCDEFGH1234567890IJKL"
	const stoken = "ZXCVBNM0987654321QWER"
	cases := []struct{ name, in string }{
		{"kv 形态", "login ok BDUSS=" + bduss + " stoken=" + stoken},
		{"JSON 形态", `{"bduss":"` + bduss + `","stoken":"` + stoken + `"}`},
		{"URL pwd 形态", "https://pan.baidu.com/s/1abc?pwd=998877665544"},
	}
	for _, c := range cases {
		out := SanitizeLogText(c.in)
		if strings.Contains(out, bduss) || strings.Contains(out, stoken) {
			t.Errorf("%s: 明文残留: %s", c.name, out)
		}
	}
	// URL pwd 值脱敏
	out := SanitizeLogText("https://pan.baidu.com/s/1abc?pwd=998877665544")
	if strings.Contains(out, "998877665544") {
		t.Errorf("pwd 明文残留: %s", out)
	}
	// 普通文本不受影响
	if got := SanitizeLogText("GET /api/files 200"); got != "GET /api/files 200" {
		t.Errorf("普通文本被误改: %s", got)
	}
}

// TestSetupLogLevel 分级过滤: ERROR 级阈值下 INFO 被丢弃(捕获 stdout 不便, 只验阈值语义)。
func TestSetupLogLevel(t *testing.T) {
	old := logLevelThreshold
	defer func() { logLevelThreshold = old }()
	SetupLogLevel("error")
	if logLevelThreshold != 3 {
		t.Fatalf("threshold = %d, want 3", logLevelThreshold)
	}
	SetupLogLevel("bogus")
	if logLevelThreshold != 1 {
		t.Fatalf("非法级别应回落 INFO(1), got %d", logLevelThreshold)
	}
	// LogAt 低级别被吞(不 panic 即可)
	LogAt("debug", "quiet %d", 1)
	LogAt("ERROR", "loud %d", 1)
}
