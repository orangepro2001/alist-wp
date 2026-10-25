package xengine

// share_transfer_test.go W7: FR-28 转存编排时序用例——fakeBaidu 桩逐步应答,
// 断言编排 1:1 上游 RunShareTransfer 的 8 步顺序(不自创顺序):
//   ①正则抽链(单测已覆盖 ParseShareLink) ②featureStr ③AccessSharePage(true)
//   ④verify+PostShareQuery ⑤UpdatePCSCookies(true) ⑥AccessSharePage(false)
//   ⑦list+ExtractShareInfo ⑧UpdatePCSCookies(true)+GenerateRequestQuery POST。
// 网络全部指向 httptest 桩, 收紧超时(工程纪律)。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs"
)

// sharePageHTML AccessSharePage 需要的 loginstate 片段(参考上游 #534 正则;
// 值必须是带引号的合法 JSON, gjson 才能取到 bdstoken/shareid 等)。
const sharePageHTML = `<html><script>var locals = {"bdstoken":"tok123","uk":"999","share_uk":"777","shareid":"555","loginstate":1};</script></html>`

// stepRecorder 记录 pan 域请求顺序(时序断言核心)。
type stepRecorder struct {
	mu   sync.Mutex
	steps []string
}

func (s *stepRecorder) add(step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = append(s.steps, step)
}

// newShareFake 构造打通 ①~⑧ 全链路的 fakeBaidu(分步应答, 每步记序)。
func newShareFake(t *testing.T, rec *stepRecorder) *fakeBaidu {
	t.Helper()
	fb := newFakeBaidu(t)
	pcsTimeout(t, fb)
	// ③/⑥ GET /s/<feature> → 分享页 HTML(带 loginstate)
	fb.handle("/s/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("surl-fail") == "1" { // 不可达分支用
			_, _ = w.Write([]byte(`platform-non-found`))
			return
		}
		rec.add("GET /s/")
		_, _ = w.Write([]byte(sharePageHTML))
	})
	// ④ POST /share/verify
	fb.handle("/share/verify", func(w http.ResponseWriter, r *http.Request) {
		rec.add("POST /share/verify")
		_ = r.ParseForm()
		if r.Form.Get("pwd") == "bad1" {
			_, _ = w.Write([]byte(`{"errno":-9}`))
			return
		}
		_, _ = w.Write([]byte(`{"errno":0,"randsk":"sk"}`))
	})
	// ⑦ GET /share/list
	fb.handle("/share/list", func(w http.ResponseWriter, r *http.Request) {
		rec.add("GET /share/list")
		_, _ = w.Write([]byte(`{"errno":0,"list":[
			{"fs_id":11,"server_filename":"a.txt","path":"/a.txt","isdir":0,"size":1},
			{"fs_id":22,"server_filename":"b.mkv","path":"/b.mkv","isdir":0,"size":2}]}`))
	})
	// ⑧ POST /share/transfer
	fb.handle("/share/transfer", func(w http.ResponseWriter, r *http.Request) {
		rec.add("POST /share/transfer")
		_ = r.ParseForm()
		if r.Form.Get("path") == "/collide" {
			_, _ = w.Write([]byte(`{"errno":12,"info":[{"path":"/x/y","errno":-30}],"target_file_nums":1,"target_file_nums_limit":500}`))
			return
		}
		_, _ = w.Write([]byte(`{"errno":0,"info":[
			{"path":"/from/a.txt"},{"path":"/from/b.mkv"}]}`))
	})
	return fb
}

// pcsTimeout 收紧桩引擎超时(网络用例纪律: 不吃上游 50s 默认; newShareEngine 内实施)。
func pcsTimeout(t *testing.T, fb *fakeBaidu) {
	t.Helper()
	_ = fb
}

// newShareEngine 在桩引擎上收紧 pan 域超时。
func newShareEngine(fb *fakeBaidu, uid uint64) *stubEngine {
	se, _ := newStubEngine(fb, uid)
	se.pcs.GetClient().Timeout = 3 * time.Second
	se.pcs.GetClient().SetResponseHeaderTimeout(2 * time.Second)
	return se
}

// 确认上游常量仍被引用(防 import 漂移)。
var _ = baidupcs.PanAppID

// TestShareTransferSequence 转存编排时序(核心验收: 不自创顺序)。
func TestShareTransferSequence(t *testing.T) {
	rec := &stepRecorder{}
	fb := newShareFake(t, rec)
	e := newShareEngine(fb, 333)

	if err := e.Transfer(context.Background(),
		"https://pan.baidu.com/s/1AbCdEf?pwd=x9y8", "x9y8", "/docs", nil); err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	want := []string{
		"GET /s/",            // ③ AccessSharePage(true)
		"POST /share/verify", // ④ verify 提取码
		"GET /s/",            // ⑥ AccessSharePage(false)(⑤ UpdatePCSCookies 为 cookie 整理, 无网络请求)
		"GET /share/list",    // ⑦ list
		"POST /share/transfer", // ⑧ POST 转存
	}
	if len(rec.steps) != len(want) {
		t.Fatalf("时序=%v, want %v", rec.steps, want)
	}
	for i := range want {
		if rec.steps[i] != want[i] {
			t.Fatalf("第 %d 步=%q, want %q (全序列=%v)", i+1, rec.steps[i], want[i], rec.steps)
		}
	}
}

// TestShareTransferToTmpSequence FR-40 参数化编排(MkdirFirst)。
func TestShareTransferToTmpSequence(t *testing.T) {
	rec := &stepRecorder{}
	fb := newShareFake(t, rec)
	e := newShareEngine(fb, 333)
	fb.handle("/rest/2.0/pcs/file", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("method") == "mkdir" {
			rec.add("POST mkdir")
			_, _ = w.Write([]byte(`{"errno":0}`))
			return
		}
		_, _ = w.Write([]byte(`{"errno":0}`))
	})
	tt, err := e.TransferToTmp(context.Background(), "https://pan.baidu.com/s/1AbCdEf", "x9y8", []string{"22"})
	if err != nil {
		t.Fatalf("TransferToTmp: %v", err)
	}
	if tt.TargetDir != "/来自分享/.tmp-555" || tt.ShareID != "555" {
		t.Fatalf("tt=%+v", tt)
	}
	// 转存 POST 应答的上游语义: filenames=全部转存成功的文件名(fs_ids 过滤由百度侧执行,
	// 桩返回两条 info), resp["filenames"] → a.txt,b.mkv 两条都进 Files。
	if len(tt.Files) != 2 || tt.Files[1].Filename != "b.mkv" || tt.Files[1].Path != "/来自分享/.tmp-555/b.mkv" {
		t.Fatalf("files=%+v", tt.Files)
	}
	joined := strings.Join(rec.steps, " -> ")
	iList, iMkdir, iPost := strings.Index(joined, "GET /share/list"), strings.Index(joined, "POST mkdir"), strings.LastIndex(joined, "POST /share/transfer")
	if iMkdir < 0 || iMkdir < iList || iMkdir > iPost {
		t.Fatalf("mkdir 时序不对(应介于 list 与 transfer 之间): %s", joined)
	}
}

// TestShareTransferBadPasscode 提取码错误(-9)→ ErrBadPasscode, 且不发转存 POST。
func TestShareTransferBadPasscode(t *testing.T) {
	rec := &stepRecorder{}
	fb := newShareFake(t, rec)
	e := newShareEngine(fb, 333)
	err := e.Transfer(context.Background(), "https://pan.baidu.com/s/1AbCdEf", "bad1", "/", nil)
	var we *Error
	if !errors.As(err, &we) || we.Code != ErrBadPasscode {
		t.Fatalf("want ErrBadPasscode, got %#v", err)
	}
	for _, s := range rec.steps {
		if s == "POST /share/transfer" {
			t.Fatal("提取码错误不应到达转存步骤")
		}
	}
}

// TestShareTransferSameName 拒转(-30)→ ErrForbiddenTransfer。
func TestShareTransferSameName(t *testing.T) {
	rec := &stepRecorder{}
	fb := newShareFake(t, rec)
	e := newShareEngine(fb, 333)
	err := e.Transfer(context.Background(), "https://pan.baidu.com/s/1AbCdEf", "x9y8", "/collide", nil)
	var we *Error
	if !errors.As(err, &we) || we.Code != ErrForbiddenTransfer {
		t.Fatalf("want ErrForbiddenTransfer, got %#v", err)
	}
}

// TestShareTransferInvalidLink 非法链接/短链在步骤②前拒绝, 零网络请求。
func TestShareTransferInvalidLink(t *testing.T) {
	rec := &stepRecorder{}
	fb := newShareFake(t, rec)
	e := newShareEngine(fb, 333)
	if err := e.Transfer(context.Background(), "https://pan.baidu.com/s/2notvalid", "x9y8", "/", nil); err == nil {
		t.Fatal("非 1 开头 feature 应拒绝")
	}
	if len(rec.steps) != 0 {
		t.Fatalf("非法链接不应发出请求: %v", rec.steps)
	}
}

// TestShareTransferExpired 分享失效(分享页 platform-non-found)→ ErrNotFound。
func TestShareTransferExpired(t *testing.T) {
	fb := newFakeBaidu(t)
	fb.handle("/s/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html class="platform-non-found">失效</html>`))
	})
	e := newShareEngine(fb, 333)
	err := e.Transfer(context.Background(), "https://pan.baidu.com/s/1dead", "x9y8", "/", nil)
	var we *Error
	if !errors.As(err, &we) || we.Code != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %#v", err)
	}
}

// 确认上游常量仍被引用(防 import 漂移)。
var _ = baidupcs.PanAppID
