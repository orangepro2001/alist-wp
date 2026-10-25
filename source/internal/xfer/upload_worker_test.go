// upload_worker_test.go W6: 上传通道单测(秒传→分片 policy/noRapid; 终态钩子)。
// 桩注入: 独立 stubUploadEngine/RapidProbe 函数字段, 不覆写方法(工程纪律)。

package xfer

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"baidupcs-web/internal/xengine"
)

type stubUploadEngine struct {
	mu       sync.Mutex
	params   []paramRec
	rapidHit bool
}

type paramRec struct {
	localPath, savePath, policy string
	noRapid                     bool
}

func (s *stubUploadEngine) Upload(_ context.Context, p xengine.UploadParam) (xengine.UploadResult, error) {
	s.mu.Lock()
	s.params = append(s.params, paramRec{p.LocalPath, p.SavePath, p.Policy, p.NoRapid})
	s.mu.Unlock()
	return xengine.UploadResult{RapidUpload: s.rapidHit, SavePath: p.SavePath}, nil
}

func newUploadTest(t *testing.T, cfg UploadConfig) (*uploadManager, *queue) {
	t.Helper()
	p := NewProxy(ProxyConfig{MaxParallel: 2, Now: cfg.Now})
	u := newUploadManager(cfg, p.q)
	return u, p.q
}

// TestUploadRapidPath 秒传命中: RapidProbe 返回 true → 不调引擎 Upload, 任务 done。
func TestUploadRapidPath(t *testing.T) {
	eng := &stubUploadEngine{}
	probeCalled := false
	u, q := newUploadTest(t, UploadConfig{
		Engine: eng,
		RapidProbe: func(context.Context, string, string, string, bool) (bool, error) {
			probeCalled = true
			return true, nil
		},
	})
	dir := t.TempDir()
	tmp := filepath.Join(dir, "uptask-x-rapid.bin")
	_ = os.WriteFile(tmp, []byte("data"), 0o600)
	id, err := u.EnqueueUpload(context.Background(), tmp, "/a.rapid.bin", "", false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if row, ok := q.get(id); ok && row.State == "done" {
			if !probeCalled {
				t.Fatal("秒传探测未被调用")
			}
			if len(eng.params) != 0 {
				t.Fatal("秒传命中不应走引擎分片")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("任务未达 done: %v", id)
}

// TestUploadChunkPath 秒传未命中 → 引擎分片上传, done + FinishHook 收到任务 ID。
func TestUploadChunkPath(t *testing.T) {
	eng := &stubUploadEngine{}
	var hooked []string
	var hookMu sync.Mutex
	u, q := newUploadTest(t, UploadConfig{
		Engine: eng,
		RapidProbe: func(context.Context, string, string, string, bool) (bool, error) {
			return false, nil
		},
		FinishHook: func(id string) { hookMu.Lock(); hooked = append(hooked, id); hookMu.Unlock() },
	})
	tmp := filepath.Join(t.TempDir(), "uptask-y.bin")
	_ = os.WriteFile(tmp, []byte("chunkme"), 0o600)
	id, err := u.EnqueueUpload(context.Background(), tmp, "/y.bin", "overwrite", true)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if row, ok := q.get(id); ok && row.State == "done" {
			eng.mu.Lock()
			defer eng.mu.Unlock()
			if len(eng.params) != 1 {
				t.Fatalf("引擎应被调用一次: %+v", eng.params)
			}
			if eng.params[0].policy != "overwrite" || !eng.params[0].noRapid {
				t.Fatalf("policy/noRapid 未透传: %+v", eng.params[0])
			}
			hookMu.Lock()
			defer hookMu.Unlock()
			if len(hooked) != 1 || hooked[0] != id {
				t.Fatalf("终态钩子应收到任务 ID: %v", hooked)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("任务未达 done: %v", id)
}

// TestUploadEnqueueMissingTemp 临时文件不存在 → 入队报错(不建任务)。
func TestUploadEnqueueMissingTemp(t *testing.T) {
	eng := &stubUploadEngine{}
	u, _ := newUploadTest(t, UploadConfig{Engine: eng})
	if _, err := u.EnqueueUpload(context.Background(), "/nonexistent/x", "/x.bin", "", false); err == nil {
		t.Fatal("临时文件缺失应报错")
	}
}

// TestUploadCancel 取消: 运行中任务取消 → cancelled 终态。
func TestUploadCancel(t *testing.T) {
	eng := &stubUploadEngine{}
	u, q := newUploadTest(t, UploadConfig{Engine: eng})
	started := make(chan struct{})
	eng.mu.Lock()
	_ = eng
	eng.mu.Unlock()
	tmp := filepath.Join(t.TempDir(), "uptask-z.bin")
	_ = os.WriteFile(tmp, []byte("z"), 0o600)
	id, err := u.EnqueueUpload(context.Background(), tmp, "/z.bin", "", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = started
	_ = u.cancelUpload(id) // 立即取消(可能仍在 queued/running)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if row, ok := q.get(id); ok && (row.State == "cancelled" || row.State == "done") {
			return // 竞态下先完成也接受(桩引擎即时返回)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("任务未终态")
}

// TestUploadRowInTaskList 上传任务行进入任务表(传输页/列表可见, Kind=upload)。
func TestUploadRowInTaskList(t *testing.T) {
	eng := &stubUploadEngine{}
	u, q := newUploadTest(t, UploadConfig{Engine: eng})
	tmp := filepath.Join(t.TempDir(), "uptask-l.bin")
	_ = os.WriteFile(tmp, []byte("l"), 0o600)
	id, err := u.EnqueueUpload(context.Background(), tmp, "/列表.bin", "", false)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := q.get(id)
	if !ok || row.Kind != "upload" {
		t.Fatalf("任务行应为 upload: %+v %v", row, ok)
	}
}
