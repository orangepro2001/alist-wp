package xengine

// pcs_test.go Wave 2 单测: httptest 模拟百度响应(Quota/List)+ 接口桩(Accounts 持久化)
// + ErrCode 映射表逐项(§0.4)。
//
// 测试通过 SetPCSAddr 将上游请求指向 httptest 服务器(pcs.SetPCSAddr 只影响 PCS 域),
// pan 域接口(/api/list)经 httptest 无法重定向域名, 故 List 用请求级桩:
// 构造 BaiduPCS 后以 NewPCSWithClient + 自定义 Transport 拦截全部出网请求。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs"
	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs/pcserror"
)

// ---------------------------------------------------------------------------
// 请求拦截桩: 记录请求并按路径分发假百度响应(覆盖 pcs 与 pan 两域)。
// ---------------------------------------------------------------------------

type fakeBaidu struct {
	mu       sync.Mutex
	handlers map[string]http.HandlerFunc // path → handler(前缀匹配)
	requests []string                    // method + " " + path(断言用)
	srv      *httptest.Server
}

func newFakeBaidu(t *testing.T) *fakeBaidu {
	t.Helper()
	fb := &fakeBaidu{handlers: map[string]http.HandlerFunc{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fb.mu.Lock()
		fb.requests = append(fb.requests, r.Method+" "+r.URL.Path)
		fb.mu.Unlock()
		for prefix, h := range fb.handlers {
			if strings.HasPrefix(r.URL.Path, prefix) {
				h(w, r)
				return
			}
		}
		http.Error(w, `{"errno":1,"error_msg":"no fake handler"}`, http.StatusOK)
	})
	fb.srv = httptest.NewServer(mux)
	t.Cleanup(fb.srv.Close)
	return fb
}

func (fb *fakeBaidu) handle(prefix string, h http.HandlerFunc) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.handlers[prefix] = h
}

// pcsFor 构造一个把全部出网流量重定向到 fake server 的 BaiduPCS。
// 做法: 自定义 http.Transport.RoundTripper 改写 Host; 凭证随意(桩不校验签名)。
func (fb *fakeBaidu) pcsFor(uid uint64) *baidupcs.BaiduPCS {
	pcs := baidupcs.NewPCS(DefaultAppID, "fake-bduss")
	pcs.SetUID(uid)
	pcs.SetHTTPS(false)
	rewrite := fb.rewriteTransport()
	// PCS 域: SetPCSAddr 指向桩
	u, _ := url.Parse(fb.srv.URL)
	pcs.SetPCSAddr(u.Host)
	pcs.SetStaticPCSAddr(true)
	// Pan 域(pan.baidu.com): 经 Transport 改写
	pcs.GetClient().SetHTTPSecure(false)
	pcs.GetClient().Transport = rewrite
	_ = rewrite
	return pcs
}

func (fb *fakeBaidu) rewriteTransport() http.RoundTripper {
	target, _ := url.Parse(fb.srv.URL)
	base := http.DefaultTransport
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != target.Host {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
		}
		return base.RoundTrip(req)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ---------------------------------------------------------------------------
// 桩引擎: 预制 BaiduPCS 注入 pool, 让真实 active() 命中。
// 注意: 不能用"覆写 active()"的方式——e.Quota 等方法定义在 pcsEngine 上,
// 内部调用的是 *pcsEngine.active()(方法提升后接收者是内层), 外层覆写不生效,
// 会现造一个无桩地址的 pcs 打向真实 pcs.baidu.com(50s 超时, 即曾经的 flaky 根因)。
// ---------------------------------------------------------------------------

type stubEngine struct {
	pcsEngine
	pcs *baidupcs.BaiduPCS
}

func newStubEngine(fb *fakeBaidu, uid uint64) (*stubEngine, *fakeBaidu) {
	pcs := fb.pcsFor(uid)
	// B1 后 active() 经 pcsForLocked → store.get 取完整账号构造实例,
	// 桩 store 须登记同 uid 账号(凭证随意, 桩不校验签名)。
	se := &stubEngine{
		pcsEngine: pcsEngine{
			store:     &accountStore{list: []*Account{{UID: uid, Name: "stub", BDUSS: "fake-bduss"}}},
			pool:      map[uint64]*baidupcs.BaiduPCS{uid: pcs},
			cfg:       defaultTransConfig(),
			activeUID: uid,
		},
		pcs: pcs,
	}
	return se, fb
}

// ---------------------------------------------------------------------------
// Quota(FR-05)
// ---------------------------------------------------------------------------

func TestQuotaOK(t *testing.T) {
	fb := newFakeBaidu(t)
	fb.handle("/rest/2.0/pcs/quota", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errno":0,"quota":104857600,"used":12345}`))
	})
	e, _ := newStubEngine(fb, 111)
	quota, used, err := e.Quota(context.Background())
	if err != nil {
		t.Fatalf("Quota: %v", err)
	}
	if quota != 104857600 || used != 12345 {
		t.Fatalf("quota=%d used=%d, want 104857600/12345", quota, used)
	}
}

func TestQuotaAuthExpired(t *testing.T) {
	fb := newFakeBaidu(t)
	fb.handle("/rest/2.0/pcs/quota", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errno":-6,"error_code":-6,"error_msg":"请重新登录"}`))
	})
	e, _ := newStubEngine(fb, 111)
	_, _, err := e.Quota(context.Background())
	var we *Error
	if !errors.As(err, &we) || we.Code != ErrAuth {
		t.Fatalf("want ErrAuth, got %#v", err)
	}
}

func TestQuotaNotFound(t *testing.T) {
	fb := newFakeBaidu(t)
	fb.handle("/rest/2.0/pcs/quota", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errno":31066,"error_code":31066,"error_msg":"文件或目录不存在"}`))
	})
	e, _ := newStubEngine(fb, 111)
	_, _, err := e.Quota(context.Background())
	var we *Error
	if !errors.As(err, &we) || we.Code != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %#v", err)
	}
}

// ---------------------------------------------------------------------------
// List(FR-06)
// ---------------------------------------------------------------------------

const fakeDir = "/"

func fakeListBody(t *testing.T, items ...map[string]interface{}) string {
	t.Helper()
	list := make([]map[string]interface{}, 0, len(items))
	for i, it := range items {
		if it["path"] == nil {
			it["path"] = fmt.Sprintf("/item%d", i)
		}
		if it["server_filename"] == nil {
			it["server_filename"] = filepath.Base(it["path"].(string))
		}
		list = append(list, it)
	}
	b, _ := json.Marshal(map[string]interface{}{"errno": 0, "list": list})
	return string(b)
}

func TestListOK(t *testing.T) {
	fb := newFakeBaidu(t)
	fb.handle("/api/list", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("dir") != "/" {
			t.Errorf("dir param = %q", r.URL.Query().Get("dir"))
		}
		_, _ = w.Write([]byte(fakeListBody(t,
			map[string]interface{}{"fs_id": 11, "path": "/docs", "server_filename": "docs", "isdir": 1, "ifhassubdir": 1, "server_ctime": 1700000000, "server_mtime": 1700000001},
			map[string]interface{}{"fs_id": 22, "path": "/a.txt", "server_filename": "a.txt", "isdir": 0, "size": 2048, "md5": "d41d8cd98f00b204e9800998ecf8427e", "server_mtime": 1700000002},
		)))
	})
	e, _ := newStubEngine(fb, 222)
	fdl, err := e.List(context.Background(), "/", 0, 0, "name", "asc")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(fdl) != 2 {
		t.Fatalf("len=%d want 2", len(fdl))
	}
	if fdl[0].FsID != 11 || !fdl[0].Isdir || !fdl[0].HasSub {
		t.Fatalf("fdl[0]=%+v", fdl[0])
	}
	if fdl[1].FsID != 22 || fdl[1].Isdir || fdl[1].Size != 2048 {
		t.Fatalf("fdl[1]=%+v", fdl[1])
	}
}

func TestListPagingSlice(t *testing.T) {
	fb := newFakeBaidu(t)
	fb.handle("/api/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fakeListBody(t,
			map[string]interface{}{"fs_id": 1, "path": "/a"},
			map[string]interface{}{"fs_id": 2, "path": "/b"},
			map[string]interface{}{"fs_id": 3, "path": "/c"},
		)))
	})
	e, _ := newStubEngine(fb, 222)
	got, err := e.List(context.Background(), "/", 2, 2, "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Path != "/c" {
		t.Fatalf("page2 size2 = %+v, want [/c]", got)
	}
}

func TestListAuthExpired(t *testing.T) {
	fb := newFakeBaidu(t)
	fb.handle("/api/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errno":4,"show_msg":0,"errtext":"illegal request"}`))
	})
	e, _ := newStubEngine(fb, 222)
	_, err := e.List(context.Background(), "/", 0, 0, "", "")
	var we *Error
	if !errors.As(err, &we) || we.Code != ErrAuth {
		t.Fatalf("want ErrAuth, got %#v", err)
	}
}

func TestListNetError(t *testing.T) {
	// 指向已关闭端口 → ErrTypeNetError
	// 收紧超时: 上游默认 client Timeout=50s, 连接拒绝虽快返回, 但 TCP 层
	// 在某些内核回路配置下会等满超时, 测试里缩短到 1s 保证用例敏捷。
	// 注意(2026-10-24 补): List 走 pan 域(pan.baidu.com), SetPCSAddr 只影响 pcs 域;
	// 必须**只**设 pcs 域地址并拦截 pan 域 Transport, 否则真网可达时百度返回 errno -6
	// (假 BDUSS)→ ErrTypeRemoteError 而非 NetError(W2 时外网不可达所以侥幸通过)。
	pcs := baidupcs.NewPCS(DefaultAppID, "fake-bduss")
	pcs.SetUID(1)
	pcs.SetPCSAddr("127.0.0.1:1")
	pcs.SetStaticPCSAddr(true)
	pcs.GetClient().Timeout = 1 * time.Second
	pcs.GetClient().SetResponseHeaderTimeout(500 * time.Millisecond)
	// pan 域全部重定向到已关闭端口 → 真正的 net error
	pcs.GetClient().SetHTTPSecure(false)
	pcs.GetClient().Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = "127.0.0.1:1"
		return http.DefaultTransport.RoundTrip(req)
	})
	s := &stubEngine{pcsEngine: pcsEngine{store: &accountStore{list: []*Account{{UID: 1, BDUSS: "fake-bduss"}}}, pool: map[uint64]*baidupcs.BaiduPCS{1: pcs}, cfg: defaultTransConfig(), activeUID: 1}, pcs: pcs}
	_, err := s.List(context.Background(), "/", 0, 0, "", "")
	var we *Error
	if !errors.As(err, &we) || we.Code != ErrNet {
		t.Fatalf("want ErrNet, got %#v", err)
	}
}

// ---------------------------------------------------------------------------
// Accounts(FR-01~04): accounts.json 0600 持久化 + 实例池
// ---------------------------------------------------------------------------

func TestAccountsPersistAndPermissions(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir, TransConfig{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a, err := eng.AddAccountByBDUSS("bduss-value", "stoken-value")
	if err != nil {
		t.Fatalf("AddAccountByBDUSS: %v", err)
	}
	if a.UID == 0 || a.Name == "" {
		t.Fatalf("account=%+v", a)
	}

	// 文件存在 + 权限 0600
	accPath := filepath.Join(dir, accountsFile)
	fi, err := os.Stat(accPath)
	if err != nil {
		t.Fatalf("stat accounts.json: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm=%o, want 600", perm)
	}
	// 目录 0700
	dirInfo, _ := os.Stat(dir)
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("dir perm=%o, want 700", perm)
	}

	// 重新加载 → 账号与 activeUID 恢复
	eng2, err := New(dir, TransConfig{})
	if err != nil {
		t.Fatalf("New(2): %v", err)
	}
	if _, ok := eng2.ActiveAccount(); !ok {
		t.Fatal("active account lost after reload")
	}
	if got := len(eng2.Accounts()); got != 1 {
		t.Fatalf("accounts=%d, want 1", got)
	}
}

func TestAccountsCRUDAndSwitch(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir, TransConfig{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a1, _ := eng.AddAccountByBDUSS("bduss-1", "stoken-1")
	a2, _ := eng.AddAccountByBDUSS("bduss-2", "stoken-2")
	if a1.UID == a2.UID {
		t.Fatal("uid collision")
	}
	// AddAccountByBDUSS 缺 stoken 拒绝
	if _, err := eng.AddAccountByBDUSS("bduss-3", ""); err == nil {
		t.Fatal("stoken required for BDUSS login")
	}
	// 默认活跃=最后添加
	if act, _ := eng.ActiveAccount(); act.UID != a2.UID {
		t.Fatalf("active=%d, want %d", act.UID, a2.UID)
	}
	// 切换
	if err := eng.SwitchAccount(a1.UID); err != nil {
		t.Fatalf("SwitchAccount: %v", err)
	}
	if act, _ := eng.ActiveAccount(); act.UID != a1.UID {
		t.Fatalf("active after switch=%d", act.UID)
	}
	// 切到不存在账号
	if err := eng.SwitchAccount(999); err == nil {
		t.Fatal("switch to unknown uid should fail")
	}
	// 删除活跃账号 → 回落到剩余首个
	if err := eng.RemoveAccount(a1.UID); err != nil {
		t.Fatalf("RemoveAccount: %v", err)
	}
	if act, ok := eng.ActiveAccount(); !ok || act.UID != a2.UID {
		t.Fatalf("fallback active=%v ok=%v", act, ok)
	}
	// Cookie 方式: 同凭证幂等
	c1, _ := eng.AddAccountByCookie("BDUSS=cookie-bduss; STOKEN=abc")
	c2, _ := eng.AddAccountByCookie("BDUSS=cookie-bduss; STOKEN=abc")
	if c1.UID != c2.UID {
		t.Fatal("cookie uid should be stable")
	}
	if got := len(eng.Accounts()); got != 2 {
		t.Fatalf("accounts=%d, want 2", got)
	}
}

func TestAccountsOpsWithoutAccount(t *testing.T) {
	dir := t.TempDir()
	eng, _ := New(dir, TransConfig{})
	if _, _, err := eng.Quota(context.Background()); err == nil {
		t.Fatal("quota without account should fail")
	}
	var we *Error
	if _, err := eng.List(context.Background(), "/", 0, 0, "", ""); !errors.As(err, &we) || we.Code != ErrAuth {
		t.Fatalf("want ErrAuth, got %#v", err)
	}
}

func TestEngineImplementsInterface(t *testing.T) {
	var _ Engine = (*pcsEngine)(nil)
	// 镜像类型关键字段存在性(编译期已查, 此处防漂移断言)
	fd := FileDirectory{FsID: 1, Path: "/", Filename: "x", Isdir: true}
	if !fd.Isdir || fd.Path != "/" {
		t.Fatal("mirror type drifted")
	}
}

// ---------------------------------------------------------------------------
// ErrCode 映射表逐项(§0.4): 用 pcserror 构造各类上游错误 → 断言映射
// ---------------------------------------------------------------------------

// mkRemoteErr 构造远端错误(PCS 风格: error_code/error_msg)。
func mkRemoteErr(code int, msg string) pcserror.Error {
	info := pcserror.NewPCSErrorInfo("测试操作")
	info.ErrCode = code
	info.ErrMsg = msg
	info.SetRemoteError()
	return info
}

// mkPanErr 构造远端错误(Pan 风格: errno)。
func mkPanErr(errno int) pcserror.Error {
	info := pcserror.NewPanErrorInfo("测试操作")
	info.ErrNo = errno
	info.SetRemoteError()
	return info
}

func mkNetErr() pcserror.Error {
	info := pcserror.NewPCSErrorInfo("测试操作")
	info.SetNetError(errors.New("dial tcp: connection refused"))
	return info
}

func TestErrCodeMapping(t *testing.T) {
	cases := []struct {
		name   string
		in     pcserror.Error
		want   ErrCode
		remote int
	}{
		{"errno4_凭证失效_PCS", mkRemoteErr(4, "illegal request"), ErrAuth, 4},
		{"errno-6_凭证失效_Pan", mkPanErr(-6), ErrAuth, -6},
		{"errno-4_登录信息有误", mkPanErr(-4), ErrRemote, -4},
		{"errno31066_不存在", mkRemoteErr(31066, "file does not exist"), ErrNotFound, 31066},
		{"errno112_页面过期按不存在", mkPanErr(112), ErrNotFound, 112},
		{"errno31112_配额满", mkRemoteErr(31112, "exceed quota"), ErrQuota, 31112},
		{"errno-9_提取码错", mkPanErr(-9), ErrBadPasscode, -9},
		{"errno-30_同名禁止转存", mkPanErr(-30), ErrForbiddenTransfer, -30},
		{"errno105_链接错误", mkPanErr(105), ErrNotFound, 105},
		{"errno31061_重名_策略语义", mkRemoteErr(31061, "file already exists"), ErrRemote, 31061},
		{"errno114514_skip命中", mkRemoteErr(114514, "skip"), ErrRemote, 114514},
		{"errno1919810_rsync命中", mkRemoteErr(1919810, "rsync"), ErrRemote, 1919810},
		{"errno31363_上传状态过期", mkRemoteErr(31363, "block miss"), ErrRemote, 31363},
		{"errno31626_UA不对", mkRemoteErr(31626, "ua error"), ErrRemote, 31626},
		{"网络错误", mkNetErr(), ErrNet, 0},
		{"JSON解析错误_内部", func() pcserror.Error {
			info := pcserror.NewPCSErrorInfo("测试操作")
			info.SetJSONError(errors.New("unexpected end of JSON"))
			return info
		}(), ErrInternal, 0},
		{"远端未知错误", mkRemoteErr(99999, "mystery"), ErrRemote, 99999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapErr(tc.in)
			we, ok := got.(*Error)
			if !ok {
				t.Fatalf("mapErr returned %#v", got)
			}
			if we.Code != tc.want {
				t.Fatalf("code=%v, want %v (msg=%q)", we.Code, tc.want, we.Message)
			}
			if we.RemoteCode != tc.remote {
				t.Fatalf("remote=%d, want %d", we.RemoteCode, tc.remote)
			}
		})
	}
}

func TestMapErrNil(t *testing.T) {
	if mapErr(nil) != nil {
		t.Fatal("mapErr(nil) should be nil")
	}
}

// ---------------------------------------------------------------------------
// 工具函数: orderOptions / ParseShareLink / uid 派生
// ---------------------------------------------------------------------------

func TestOrderOptionsMapping(t *testing.T) {
	cases := []struct {
		by, order string
		wantBy    baidupcs.OrderBy
		wantOrder baidupcs.Order
	}{
		{"name", "asc", baidupcs.OrderByName, baidupcs.OrderAsc},
		{"time", "desc", baidupcs.OrderByTime, baidupcs.OrderDesc},
		{"size", "desc", baidupcs.OrderBySize, baidupcs.OrderDesc},
		{"bogus", "bogus", baidupcs.OrderByName, baidupcs.OrderAsc}, // 非法回落默认
	}
	for _, tc := range cases {
		opts := orderOptions(tc.by, tc.order)
		if opts.By != tc.wantBy || opts.Order != tc.wantOrder {
			t.Fatalf("orderOptions(%q,%q) = %v/%v, want %v/%v",
				tc.by, tc.order, opts.By, opts.Order, tc.wantBy, tc.wantOrder)
		}
	}
}

func TestParseShareLink(t *testing.T) {
	e := &pcsEngine{}
	cases := []struct {
		name, in, link, pass string
		wantErr              bool
	}{
		{"完整链+pwd参数", "链接: https://pan.baidu.com/s/1AbC-dEf_123?pwd=x9y8 提取码: x9y8",
			"https://pan.baidu.com/s/1AbC-dEf_123", "x9y8", false},
		{"完整链+文本提取码", "通过网盘分享的文件: https://pan.baidu.com/s/1abcdef 提取码: ab12",
			"https://pan.baidu.com/s/1abcdef", "ab12", false},
		{"裸链接无提取码", "https://pan.baidu.com/s/1xyz",
			"https://pan.baidu.com/s/1xyz", "", false},
		{"非法文本", "没有任何链接", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			link, pass, err := e.ParseShareLink(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseShareLink: %v", err)
			}
			if link != tc.link || pass != tc.pass {
				t.Fatalf("got (%q,%q), want (%q,%q)", link, pass, tc.link, tc.pass)
			}
		})
	}
}

func TestShareFeatureValidation(t *testing.T) {
	if _, err := shareFeature("https://pan.baidu.com/s/1abc123"); err != nil {
		t.Fatalf("normal link: %v", err)
	}
	// 短链 init?surl=xxx → "1"+xxx
	f, err := shareFeature("https://pan.baidu.com/share/init?surl=abc123")
	if err != nil {
		t.Fatalf("init link: %v", err)
	}
	if f != "1abc123" {
		t.Fatalf("feature=%q, want 1abc123", f)
	}
	// 非 1 开头 → 非法
	if _, err := shareFeature("https://pan.baidu.com/s/2abc"); err == nil {
		t.Fatal("feature not starting with 1 should fail")
	}
}

func TestDeriveUIDStableAndHighBit(t *testing.T) {
	u1, n1 := deriveUIDFromCookie("BDUSS=abc; STOKEN=def")
	u2, _ := deriveUIDFromCookie("bduss=abc; path=/")
	if u1 != u2 {
		t.Fatalf("uid unstable: %d vs %d", u1, u2)
	}
	if u1&(1<<63) == 0 {
		t.Fatal("derived uid should have high bit set")
	}
	if !strings.HasPrefix(n1, "baidu_") {
		t.Fatalf("name=%q", n1)
	}
	if deriveUIDFromBDUSS("x") == deriveUIDFromBDUSS("y") {
		t.Fatal("different bduss should derive different uid")
	}
}

func TestSliceFDListEdgeCases(t *testing.T) {
	fdl := baidupcs.FileDirectoryList{
		{FsID: 1, Path: "/a", Filename: "a"},
		{FsID: 2, Path: "/b", Filename: "b"},
	}
	if got := sliceFDList(fdl, 0, 0); len(got) != 2 {
		t.Fatalf("page<=0 should return all")
	}
	if got := sliceFDList(fdl, 5, 2); len(got) != 0 {
		t.Fatalf("overflow page should be empty")
	}
	if got := sliceFDList(fdl, 1, 1); len(got) != 1 || got[0].Path != "/a" {
		t.Fatalf("page1 = %+v", got)
	}
}

func TestFilterFsIDs(t *testing.T) {
	if got := filterFsIDs("[1,2,3]", nil); got != "[1,2,3]" {
		t.Fatalf("all = %q", got)
	}
	if got := filterFsIDs("[1,2,3]", []string{"2"}); got != "[2]" {
		t.Fatalf("filter = %q", got)
	}
	if got := filterFsIDs("[1,2,3]", []string{"9"}); got != "[]" {
		t.Fatalf("filter missing = %q", got)
	}
}

func TestBlockSizeFor(t *testing.T) {
	if blockSizeFor(1<<20) != baidupcs.MinUploadBlockSize {
		t.Fatal("small file block")
	}
	if blockSizeFor(9<<30) != baidupcs.MiddleUploadBlockSize {
		t.Fatal("mid file block")
	}
	if blockSizeFor(33<<30) != baidupcs.MaxUploadBlockSize {
		t.Fatal("big file block")
	}
}

// quiesce unused warnings for build tags-free stub use
var _ = http.StatusOK
