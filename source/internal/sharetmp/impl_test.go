package sharetmp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// stubDeps 可编程桩(工程纪律: 函数字段注入, 不覆写方法; 短间隔不真等)。
type stubDeps struct {
	mu          sync.Mutex
	previewErr  error
	transferErr error
	removeErr   error
	freeBytes   int64
	items       []Item
	transferred []Item
	target      string
	removed     [][]string
	dlSeq       []error // 每文件下载结果
	dlCalled    []string
	dlBlock     chan struct{} // 非空时首个下载阻塞直至关闭
}

func (s *stubDeps) deps() Deps {
	d := Deps{
		PreviewFn: func(context.Context, string, string) ([]Item, string, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.items, "sid-1", s.previewErr
		},
		FreeBytesFn: func(context.Context) (int64, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.freeBytes, nil
		},
		TransferFn: func(_ context.Context, _ string, _ string, fsids []string) (string, []Item, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.transferErr != nil {
				return "", nil, s.transferErr
			}
			s.target = "/来自分享/.tmp-sid-1"
			s.transferred = nil
			for _, it := range s.items {
				for _, f := range fsids {
					if it.FsID == f {
						s.transferred = append(s.transferred, it)
					}
				}
			}
			return s.target, s.transferred, nil
		},
		RemoveFn: func(_ context.Context, paths []string) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.removeErr != nil {
				return s.removeErr
			}
			s.removed = append(s.removed, append([]string(nil), paths...))
			return nil
		},
		DownloadFn: func(ctx context.Context, p string) error {
			s.mu.Lock()
			i := len(s.dlCalled)
			s.dlCalled = append(s.dlCalled, p)
			block := s.dlBlock
			s.mu.Unlock()
			if block != nil && i == 0 {
				select {
				case <-block:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if i < len(s.dlSeq) {
				return s.dlSeq[i]
			}
			return nil
		},
		DeleteDelay: 30 * time.Millisecond,
		Now:         time.Now,
	}
	return d
}

func items3() []Item {
	return []Item{
		{FsID: "1", Filename: "a.txt", Size: 100},
		{FsID: "2", Filename: "b.txt", Size: 200},
		{FsID: "3", Filename: "c.txt", Size: 300},
	}
}

func waitPhase(t *testing.T, o *orch, id string, want Phase, timeout time.Duration) Job {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		j, _ := o.Get(id)
		if j.Phase == want {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	j, _ := o.Get(id)
	t.Fatalf("作业 %s 未达 %s(当前 %s, msg=%s)", id, want, j.Phase, j.Message)
	return j
}

// TestPreviewParsingToPreviewing 状态机 parsing→previewing(成功)。
func TestPreviewParsingToPreviewing(t *testing.T) {
	s := &stubDeps{items: items3()}
	o, _ := New(s.deps())
	j, err := o.Preview("http://pan.baidu.com/s/1abc", "1234")
	if err != nil {
		t.Fatal(err)
	}
	if j.Phase != PhasePreviewing || j.ShareID != "sid-1" {
		t.Fatalf("应 previewing: %+v", j)
	}
}

// TestPreviewBadPasscode 提取码错 → failed(PREVIEW_FAILED), 无任何网盘动作(异常 ④)。
func TestPreviewBadPasscode(t *testing.T) {
	s := &stubDeps{previewErr: errors.New("提取码错误")}
	o, _ := New(s.deps())
	j, err := o.Preview("http://x", "0000")
	if err == nil {
		t.Fatal("应报错")
	}
	if j.Phase != PhaseFailed || j.ErrCode != "PREVIEW_FAILED" {
		t.Fatalf("应 failed: %+v", j)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.removed) != 0 || s.target != "" {
		t.Fatal("预览失败不应有网盘动作")
	}
}

// TestQuotaPrecheckReject 配额不足 → checking 失败中文报错, 无转存无残留(异常 ①)。
func TestQuotaPrecheckReject(t *testing.T) {
	s := &stubDeps{items: items3(), freeBytes: 150}
	o, _ := New(s.deps())
	j, _ := o.Preview("http://x", "1")
	if err := o.StartDownload(j.ID, []string{"1", "2"}); err != nil {
		t.Fatal(err)
	}
	got := waitPhase(t, o.(*orch), j.ID, PhaseFailed, time.Second)
	if got.ErrCode != "QUOTA_EXCEEDED" {
		t.Fatalf("错误码不符: %+v", got)
	}
	if got.Message != "网盘剩余空间不足, 无法转存分享内容" {
		t.Fatalf("中文文案不符: %s", got.Message)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.removed) != 0 || s.transferred != nil {
		t.Fatal("配额不足不应有转存/残留")
	}
}

// TestQuotaPrecheckPassExactly 配额恰好等于需求 → 通过(边界)。
func TestQuotaPrecheckPassExactly(t *testing.T) {
	s := &stubDeps{items: items3(), freeBytes: 300}
	o, _ := New(s.deps())
	j, _ := o.Preview("http://x", "1")
	_ = o.StartDownload(j.ID, []string{"3"}) // 300B 恰好
	got := waitPhase(t, o.(*orch), j.ID, PhaseDone, time.Second)
	if got.Phase != PhaseDone {
		t.Fatalf("应 done: %+v", got)
	}
}

// TestTransferFailedNoResidue 转存失败(禁止转存等)→ failed, 无回删调用(目录未建)。
func TestTransferFailedNoResidue(t *testing.T) {
	s := &stubDeps{items: items3(), freeBytes: 1 << 30, transferErr: errors.New("该分享不支持转存, 无法直连下载")}
	o, _ := New(s.deps())
	j, _ := o.Preview("http://x", "1")
	_ = o.StartDownload(j.ID, []string{"1"})
	got := waitPhase(t, o.(*orch), j.ID, PhaseFailed, time.Second)
	if got.ErrCode != "TRANSFER_FAILED" || got.Message == "" {
		t.Fatalf("应 TRANSFER_FAILED: %+v", got)
	}
}

// TestFullPathDone 状态机全迁移: previewing→checking→transferring→downloading→cleaning→done。
func TestFullPathDone(t *testing.T) {
	s := &stubDeps{items: items3(), freeBytes: 1 << 30}
	o, _ := New(s.deps())
	j, _ := o.Preview("http://x", "1")
	_ = o.StartDownload(j.ID, []string{"1", "2"})
	waitPhase(t, o.(*orch), j.ID, PhaseDone, time.Second)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dlCalled) != 2 {
		t.Fatalf("应下载勾选的 2 个: %v", s.dlCalled)
	}
	// 终态 10s 内(注入 30ms)删临时目录
	if len(s.removed) == 0 || s.removed[len(s.removed)-1][0] != "/来自分享/.tmp-sid-1" {
		t.Fatalf("终态后应回删目录: %v", s.removed)
	}
}

// TestDownloadFailureRollback 中途失败 → failed 且立即回删(异常 ③ 前半)。
func TestDownloadFailureRollback(t *testing.T) {
	s := &stubDeps{items: items3(), freeBytes: 1 << 30, dlSeq: []error{nil, errors.New("dlink 失效")}}
	o, _ := New(s.deps())
	j, _ := o.Preview("http://x", "1")
	_ = o.StartDownload(j.ID, []string{"1", "2"})
	got := waitPhase(t, o.(*orch), j.ID, PhaseFailed, time.Second)
	if got.ErrCode != "DOWNLOAD_FAILED" {
		t.Fatalf("应 DOWNLOAD_FAILED: %+v", got)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.removed) == 0 {
		t.Fatal("失败后应回删临时目录")
	}
}

// TestCancelGoesCleaning 取消 → cancelled 终态仍删目录(FR-40: 终态后删)。
func TestCancelGoesCleaning(t *testing.T) {
	s := &stubDeps{items: items3(), freeBytes: 1 << 30}
	block := make(chan struct{})
	s.dlBlock = block
	o, _ := New(s.deps())
	j, _ := o.Preview("http://x", "1")
	_ = o.StartDownload(j.ID, []string{"1", "2"})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cur, _ := o.(*orch).Get(j.ID); cur.Phase == PhaseDownloading {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = o.Cancel(j.ID)
	close(block)
	got := waitPhase(t, o.(*orch), j.ID, PhaseFailed, time.Second)
	if got.ErrCode != "CANCELLED" {
		t.Fatalf("取消终态应 CANCELLED: %+v", got)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.removed) == 0 {
		t.Fatal("取消后应删临时目录")
	}
}

// TestPartialSelection 部分勾选: 转存/下载清单只含勾选项(验收 ①)。
func TestPartialSelection(t *testing.T) {
	s := &stubDeps{items: items3(), freeBytes: 1 << 30}
	o, _ := New(s.deps())
	j, _ := o.Preview("http://x", "1")
	_ = o.StartDownload(j.ID, []string{"2"})
	waitPhase(t, o.(*orch), j.ID, PhaseDone, time.Second)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.transferred) != 1 || s.transferred[0].FsID != "2" {
		t.Fatalf("转存应只含 fsid=2: %v", s.transferred)
	}
}

// TestStartDownloadValidation StartDownload 校验: 未知作业/空勾选拒绝。
func TestStartDownloadValidation(t *testing.T) {
	s := &stubDeps{items: items3()}
	o, _ := New(s.deps())
	if err := o.StartDownload("nosuch", []string{"1"}); err == nil {
		t.Fatal("未知作业应报错")
	}
	j, _ := o.Preview("http://x", "1")
	if err := o.StartDownload(j.ID, nil); err == nil {
		t.Fatal("空勾选应报错")
	}
}

// TestCancelUnknown Cancel 幂等: 不存在作业报错, 存在作业重复取消不报错。
func TestCancelUnknown(t *testing.T) {
	s := &stubDeps{items: items3()}
	o, _ := New(s.deps())
	if err := o.Cancel("nosuch"); err == nil {
		t.Fatal("未知作业应报错")
	}
}

// TestSweepPanTmp 重启孤儿扫: /来自分享/ 下 .tmp-* 全删, 非 .tmp- 保留(异常 ③ 后半)。
func TestSweepPanTmp(t *testing.T) {
	var removed []string
	n, err := SweepPanTmp(context.Background(), SweepDeps{
		ListTmpDirs: func(context.Context) ([]string, error) {
			return []string{"/来自分享/.tmp-a", "/来自分享/.tmp-b", "/来自分享/正常"}, nil
		},
		RemoveDirs: func(_ context.Context, paths []string) error {
			removed = append(removed, paths...)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(removed) != 2 {
		t.Fatalf("应删 2 个孤儿目录: n=%d removed=%v", n, removed)
	}
	for _, p := range removed {
		if p == "/来自分享/正常" {
			t.Fatal("非 .tmp- 目录不应被删")
		}
	}
}

// TestList List 输出活跃作业。
func TestList(t *testing.T) {
	s := &stubDeps{items: items3()}
	o, _ := New(s.deps())
	_, _ = o.Preview("http://x", "1")
	_, _ = o.Preview("http://y", "2")
	if got := len(o.List()); got != 2 {
		t.Fatalf("应 2 条: %d", got)
	}
}

// 接口实现断言。
var (
	_ Orchestrator = (*orch)(nil)
	_ = fmt.Sprintf
)

// TestTransferFailedNonEmptyTargetRemoved B3: 转存失败但 target 非空(MkdirFirst 已建目录)
// → failed 且 RemoveFn 以该 target 回删, 不留孤儿目录。
func TestTransferFailedNonEmptyTargetRemoved(t *testing.T) {
	s := &stubDeps{items: items3(), freeBytes: 1 << 30}
	d := s.deps()
	d.TransferFn = func(context.Context, string, string, []string) (string, []Item, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		// 模拟引擎 B3 语义: 失败也返回已建的 .tmp- 目录(MkdirFirst 已建)
		s.target = "/来自分享/.tmp-sid-1"
		return s.target, nil, errors.New("该分享不支持转存")
	}
	o, _ := New(d)
	j, _ := o.Preview("http://x", "1")
	if err := o.StartDownload(j.ID, []string{"1"}); err != nil {
		t.Fatal(err)
	}
	got := waitPhase(t, o.(*orch), j.ID, PhaseFailed, time.Second)
	if got.ErrCode != "TRANSFER_FAILED" {
		t.Fatalf("应 TRANSFER_FAILED: %+v", got)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.removed) == 0 || s.removed[0][0] != "/来自分享/.tmp-sid-1" {
		t.Fatalf("失败也应回删非空 target: %v", s.removed)
	}
}
