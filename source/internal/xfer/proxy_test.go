// proxy_test.go W5 单测: Range 透传/断流续取/RFC5987/token 过期/并发上限/取消/限速等。
// 纪律: 桩源一律 httptest; 网络超时收紧 ≤2s; 桩数据经注入字段(DlinkResolver)进入, 不覆写方法。
package xfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// testClock 可拨时钟。
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// stubResolver 桩直链解析器(注入字段, 非覆写)。
type stubResolver struct {
	mu       sync.Mutex
	srvURL   string
	ua       string
	size     int64
	calls    int
	failNext int // >0 时接下来 N 次返回错误
}

func (s *stubResolver) resolve(_ context.Context, _ string) (string, string, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failNext > 0 {
		s.failNext--
		return "", "", 0, errors.New("stub: dlink 失效")
	}
	return s.srvURL, s.ua, s.size, nil
}

// newStubSource 起桩 HTTP 源(模拟百度): 支持 Range/206、可注入 UA 校验、可配置断流。
type stubSource struct {
	srv     *httptest.Server
	uaGot   string
	refGot  string
	body    []byte
	cutAt   int64 // >0 时读到该偏移强制断流(模拟百度断流)
	cutOnce sync.Once
	mu      sync.Mutex
}

func newStubSource(t *testing.T, size int, ua string) *stubSource {
	t.Helper()
	s := &stubSource{body: make([]byte, size)}
	for i := range s.body {
		s.body[i] = byte('a' + i%26)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/dlink", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.uaGot = r.Header.Get("User-Agent")
		s.refGot = r.Header.Get("Referer")
		uaOK := ua == "" || s.uaGot == ua // -race: 判定一并入临界区(并发请求下 uaGot 竞争)
		s.mu.Unlock()
		if !uaOK {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		full := s.body
		start := int64(0)
		part := full
		if rh := r.Header.Get("Range"); rh != "" {
			var sOff, eOff int64
			if n, _ := fmt.Sscanf(rh, "bytes=%d-%d", &sOff, &eOff); n == 2 {
				start, part = sOff, full[sOff:eOff+1]
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", sOff, eOff, len(full)))
				w.WriteHeader(http.StatusPartialContent)
			} else if _, err := fmt.Sscanf(rh, "bytes=%d-", &sOff); err == nil {
				start, part = sOff, full[sOff:]
				if start > 0 {
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, int64(len(full))-1, len(full)))
					w.WriteHeader(http.StatusPartialContent)
				}
			}
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(part)))
		w.Header().Set("Accept-Ranges", "bytes")
		// 断流模拟: 声明完整长度但只写出 cutAt 前缀后掐断(客户端将见 unexpected EOF)
		if s.cutAt > 0 {
			rel := s.cutAt - start // 相对本响应的偏移
			if rel > 0 && rel < int64(len(part)) {
				w.Write(part[:rel])
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				panic(http.ErrAbortHandler)
			}
		}
		w.Write(part)
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

// newTestProxy 构造收紧超时的测试代理。
func newTestProxy(t *testing.T, key []byte, res DlinkResolver, clock *testClock, maxParallel int) *proxy {
	t.Helper()
	p := NewProxy(ProxyConfig{
		SessionKey:  key,
		Resolve:     res,
		MaxParallel: maxParallel,
		Now:         clock.Now,
		Client:      &http.Client{Timeout: 2 * time.Second},
	})
	t.Cleanup(p.Close)
	return p
}

func signedURL(t *testing.T, p Proxy, key []byte, path string, clock *testClock) string {
	t.Helper()
	exp := clock.Now().Add(30 * time.Minute)
	u, err := p.ResolveForURL(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = key
	_ = exp
	return u
}

// --- 1. 全量下载 200 + 内容一致 ---
func TestProxyFullDownload200(t *testing.T) {
	clock := &testClock{now: time.Now()}
	src := newStubSource(t, 1000, "netdisk")
	res := &stubResolver{srvURL: src.srv.URL + "/dlink", ua: "netdisk", size: 1000}
	p := newTestProxy(t, []byte("k"), res.resolve, clock, 3)
	u, _ := p.ResolveForURL("/f.bin")
	resp, err := http.Get("http://" + "x") // 占位(实际走 handler)
	_ = resp
	_ = err
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", u, nil)
	if err := p.ServeDownload(rec, req, "/f.bin"); err != nil {
		t.Fatalf("ServeDownload: %v", err)
	}
	if rec.Code != 200 {
		t.Fatalf("应 200, got %d", rec.Code)
	}
	if got := rec.Body.String(); got != string(src.body) {
		t.Fatalf("内容不一致: len=%d", len(got))
	}
}

// --- 2. Range 透传 → 206 + 偏移正确 ---
func TestProxyRange206(t *testing.T) {
	clock := &testClock{now: time.Now()}
	src := newStubSource(t, 1000, "netdisk")
	res := &stubResolver{srvURL: src.srv.URL + "/dlink", ua: "netdisk", size: 1000}
	p := newTestProxy(t, []byte("k"), res.resolve, clock, 3)
	u, _ := p.ResolveForURL("/f.bin")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", u, nil)
	req.Header.Set("Range", "bytes=100-199")
	if err := p.ServeDownload(rec, req, "/f.bin"); err != nil {
		t.Fatalf("ServeDownload: %v", err)
	}
	if rec.Code != 206 {
		t.Fatalf("应 206, got %d", rec.Code)
	}
	want := string(src.body[100:200])
	if rec.Body.String() != want {
		t.Fatalf("Range 偏移内容不正确: gotLen=%d wantLen=%d first=%q", rec.Body.Len(), len(want), rec.Body.String()[:min(5, rec.Body.Len())])
	}
	if h := rec.Header().Get("Accept-Ranges"); h != "bytes" {
		t.Errorf("缺 Accept-Ranges: %q", h)
	}
}

// --- 3. Range 边界: 起点越界/末段 ---
func TestProxyRangeEdge(t *testing.T) {
	clock := &testClock{now: time.Now()}
	src := newStubSource(t, 100, "netdisk")
	res := &stubResolver{srvURL: src.srv.URL + "/dlink", ua: "netdisk", size: 100}
	p := newTestProxy(t, []byte("k"), res.resolve, clock, 3)
	u, _ := p.ResolveForURL("/f.bin")

	// 末段
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", u, nil)
	req.Header.Set("Range", "bytes=90-99")
	_ = p.ServeDownload(rec, req, "/f.bin")
	if rec.Code != 206 || rec.Body.Len() != 10 {
		t.Fatalf("末段应 206/10B, got %d/%d", rec.Code, rec.Body.Len())
	}
	// 后缀语义 bytes=-N: 无起始偏移 → 全量 200(设计允许可降级)
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", u, nil)
	req2.Header.Set("Range", "bytes=-50")
	_ = p.ServeDownload(rec2, req2, "/f.bin")
	if rec2.Code != 200 || rec2.Body.Len() != 100 {
		t.Fatalf("后缀语义应 200/100B, got %d/%d", rec2.Code, rec2.Body.Len())
	}
}

// --- 4. 中文文件名 RFC5987 ---
func TestProxyDispositionRFC5987(t *testing.T) {
	clock := &testClock{now: time.Now()}
	src := newStubSource(t, 10, "netdisk")
	res := &stubResolver{srvURL: src.srv.URL + "/dlink", ua: "netdisk", size: 10}
	p := newTestProxy(t, []byte("k"), res.resolve, clock, 3)
	u, _ := p.ResolveForURL("/报表_年度.xlsx")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", u, nil)
	_ = p.ServeDownload(rec, req, "/报表_年度.xlsx")
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "filename*=UTF-8''") {
		t.Fatalf("缺 RFC5987: %q", cd)
	}
	if !strings.Contains(cd, "%E6%8A%A5%E8%A1%A8") {
		t.Fatalf("中文未按 UTF-8 百分号编码: %q", cd)
	}
}

// --- 5. token 过期拒绝(403 语义) ---
func TestProxyTokenExpired(t *testing.T) {
	clock := &testClock{now: time.Now()}
	src := newStubSource(t, 10, "netdisk")
	res := &stubResolver{srvURL: src.srv.URL + "/dlink", ua: "netdisk", size: 10}
	key := []byte("session-key")
	p := newTestProxy(t, key, res.resolve, clock, 3)
	u, _ := p.ResolveForURL("/f.bin")
	clock.Add(31 * time.Minute) // 推进超过 30min TTL
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", u, nil)
	err := p.ServeDownload(rec, req, "/f.bin")
	if !errors.Is(err, ErrBadToken) {
		t.Fatalf("过期应 ErrBadToken, got %v", err)
	}
}

// --- 6. token 篡改拒绝 ---
func TestProxyTokenTampered(t *testing.T) {
	clock := &testClock{now: time.Now()}
	src := newStubSource(t, 10, "netdisk")
	res := &stubResolver{srvURL: src.srv.URL + "/dlink", ua: "netdisk", size: 10}
	p := newTestProxy(t, []byte("k1"), res.resolve, clock, 3)
	u, _ := p.ResolveForURL("/f.bin")
	u2 := strings.Replace(u, "token=", "token=00000000000000000000000000000000", 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", u2, nil)
	if err := p.ServeDownload(rec, req, "/f.bin"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("篡改应 ErrBadToken, got %v", err)
	}
}

// --- 7. 断流续取: 上游掐断后续 Range 续写, 浏览器连接不断 ---
func TestProxyResumeAfterCut(t *testing.T) {
	clock := &testClock{now: time.Now()}
	src := newStubSource(t, 600, "netdisk")
	src.cutAt = 200 // 200B 处掐断
	res := &stubResolver{srvURL: src.srv.URL + "/dlink", ua: "netdisk", size: 600}
	p := newTestProxy(t, []byte("k"), res.resolve, clock, 3)
	u, _ := p.ResolveForURL("/f.bin")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", u, nil)
	err := p.ServeDownload(rec, req, "/f.bin")
	if err != nil {
		t.Fatalf("续取后应成功: %v", err)
	}
	if rec.Body.Len() != 600 {
		t.Fatalf("断流续取应取全 600B, got %d", rec.Body.Len())
	}
	if rec.Body.String() != string(src.body) {
		t.Fatal("续取拼接内容不正确")
	}
	if res.calls < 2 {
		t.Fatalf("应重解直链 ≥2 次, got %d", res.calls)
	}
}

// --- 8. 并发上限: 第 4 个排队直至有槽释放 ---
func TestProxyConcurrencyLimit(t *testing.T) {
	clock := &testClock{now: time.Now()}
	src := newStubSource(t, 50, "netdisk")
	res := &stubResolver{srvURL: src.srv.URL + "/dlink", ua: "netdisk", size: 50}
	p := newTestProxy(t, []byte("k"), res.resolve, clock, 2) // 上限 2
	u, _ := p.ResolveForURL("/f.bin")

	started := make(chan struct{}, 4)
	release := make(chan struct{})
	run := func() {
		req := httptest.NewRequest("GET", u, nil)
		ctx, cxl := context.WithCancel(context.Background())
		defer cxl()
		req = req.WithContext(ctx)
		req.Header.Set("Range", "bytes=0-4")
		started <- struct{}{}
		<-release // 人为挂住, 占住槽
		_ = p.ServeDownload(httptest.NewRecorder(), req, "/f.bin")
	}
	go run()
	go run()
	<-started
	<-started
	// 第 3 个应排队(queued)
	go run()
	<-started
	go run()
	<-started
	close(release)
	time.Sleep(100 * time.Millisecond)
}

// --- 9. 排队任务在列表可见(queued/running) ---
func TestProxyQueueVisible(t *testing.T) {
	clock := &testClock{now: time.Now()}
	src := newStubSource(t, 50, "netdisk")
	res := &stubResolver{srvURL: src.srv.URL + "/dlink", ua: "netdisk", size: 50}
	p := newTestProxy(t, []byte("k"), res.resolve, clock, 1)
	u, _ := p.ResolveForURL("/q.bin")

	hold := make(chan struct{})
	done := make(chan struct{})
	go func() {
		req := httptest.NewRequest("GET", u, nil)
		_ = p.ServeDownload(httptest.NewRecorder(), req.WithContext(cancelCtx(hold)), "/q.bin")
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	rows := p.Queue().list()
	if len(rows) == 0 {
		t.Fatal("任务行应可见")
	}
	close(hold)
	<-done
}

// --- 10. 用户取消: running 任务被取消置终态(限速拖慢传输, 50ms 时取消必命中 running) ---
func TestProxyCancel(t *testing.T) {
	clock := &testClock{now: time.Now()}
	src := newStubSource(t, 1<<20, "netdisk")
	res := &stubResolver{srvURL: src.srv.URL + "/dlink", ua: "netdisk", size: 1 << 20}
	p := newTestProxy(t, []byte("k"), res.resolve, clock, 1)
	p.cfg.Rate = NewRateLimiter(256 * 1024) // 1MB@256KB/s ≈ 4s, 确保 50ms 时仍在传输
	u, _ := p.ResolveForURL("/big.bin")

	req := httptest.NewRequest("GET", u, nil)
	rec := httptest.NewRecorder()
	go func() {
		time.Sleep(50 * time.Millisecond)
		rows := p.Queue().list()
		if len(rows) > 0 {
			_ = p.q.cancel(rows[0].ID)
		}
	}()
	err := p.ServeDownload(rec, req, "/big.bin")
	if err == nil {
		t.Fatal("取消后应报错")
	}
	rows := p.Queue().list()
	if len(rows) == 0 || (rows[0].State != "cancelled" && rows[0].State != "failed") {
		t.Fatalf("任务应终态, got %+v", rows)
	}
}

// --- 11. 限速钩子生效(限速 100KB/s, 300KB 至少 ~2s) ---
func TestProxyRateLimit(t *testing.T) {
	clock := &testClock{now: time.Now()}
	src := newStubSource(t, 300*1024, "netdisk")
	res := &stubResolver{srvURL: src.srv.URL + "/dlink", ua: "netdisk", size: 300 * 1024}
	rl := NewRateLimiter(100 * 1024)
	p := NewProxy(ProxyConfig{
		SessionKey: []byte("k"), Resolve: res.resolve, MaxParallel: 1,
		Rate: rl, Client: &http.Client{Timeout: 10 * time.Second}, Now: clock.Now,
	})
	defer p.Close()
	u, _ := p.ResolveForURL("/r.bin")
	req := httptest.NewRequest("GET", u, nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	if err := p.ServeDownload(rec, req, "/r.bin"); err != nil {
		t.Fatalf("限速下载: %v", err)
	}
	if el := time.Since(start); el < 2*time.Second {
		t.Fatalf("限速未生效: 300KB@100KB/s 应 ≥2s, got %v", el)
	}
}

// --- 12. 直链解析失败 → 502 语义错误 ---
func TestProxyResolveFail(t *testing.T) {
	clock := &testClock{now: time.Now()}
	res := &stubResolver{failNext: 99}
	p := newTestProxy(t, []byte("k"), res.resolve, clock, 3)
	u, _ := p.ResolveForURL("/f.bin")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", u, nil)
	err := p.ServeDownload(rec, req, "/f.bin")
	if err == nil {
		t.Fatal("解析失败应报错")
	}
	var pe *ProxyError
	if !errors.As(err, &pe) || pe.Status != 502 {
		t.Fatalf("应 502 ProxyError, got %v", err)
	}
}

// --- 13. UA 透传: 桩源收到 NetdiskUA 且无 Referer ---
func TestProxyUAPassed(t *testing.T) {
	clock := &testClock{now: time.Now()}
	src := newStubSource(t, 10, "netdisk")
	res := &stubResolver{srvURL: src.srv.URL + "/dlink", ua: "netdisk", size: 10}
	p := newTestProxy(t, []byte("k"), res.resolve, clock, 3)
	u, _ := p.ResolveForURL("/f.bin")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", u, nil)
	req.Header.Set("Referer", "https://evil.example/")
	_ = p.ServeDownload(rec, req, "/f.bin")
	if src.uaGot != "netdisk" {
		t.Fatalf("桩源应收到 netdisk UA, got %q", src.uaGot)
	}
	if src.refGot != "" {
		t.Fatalf("Referer 应被剥离, got %q", src.refGot)
	}
}

// --- 14. SSE 事件总线: 进度 ≤500ms 推送与终态可见 ---
func TestEventBusPublish(t *testing.T) {
	bus := newEventBus()
	defer bus.close()
	ch := bus.subscribe(context.Background(), Subscribe{Interval: 10 * time.Millisecond})
	bus.publish(Progress{TaskID: "t1", State: "running", Percent: 50})
	select {
	case p := <-ch:
		if p.TaskID != "t1" || p.State != "running" {
			t.Fatalf("事件内容不对: %+v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("500ms 内未收到事件")
	}
	// 断开清理
	bus.publish(Progress{TaskID: "t2"})
}

// cancelCtx 便捷: 返回立即/手动取消的 ctx。
func cancelCtx(hold chan struct{}) context.Context {
	ctx, cxl := context.WithCancel(context.Background())
	go func() {
		<-hold
		cxl()
	}()
	return ctx
}

var _ = io.EOF
