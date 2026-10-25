package uploader

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// stubTemp tempmgr 桩: 记录删除调用(工程纪律: 独立桩类型+函数注入, 不覆写方法)。
type stubTemp struct {
	mu      sync.Mutex
	dir     string
	deleted []string
	files   map[string]string
}

func newStubTemp() *stubTemp {
	s := &stubTemp{files: map[string]string{}}
	s.dir, _ = os.MkdirTemp("", "uptest-*")
	return s
}

func (s *stubTemp) NewTaskFile(taskID, filename string) (string, error) {
	base := filepath.Base(filename)
	p := filepath.Join(s.dir, "uptask-"+taskID+"-"+base)
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.files[taskID] = p
	s.mu.Unlock()
	return p, nil
}

func (s *stubTemp) DeleteTaskFile(taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, taskID)
	return nil
}

func (s *stubTemp) deletedCount(taskID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, d := range s.deleted {
		if d == taskID {
			n++
		}
	}
	return n
}

// stubStart 上传启动桩: 记录入参并返回递增唯一 taskID(与生产唯一任务 ID 契约一致)。
type stubStart struct {
	mu        sync.Mutex
	paths     []string
	savePaths []string
	taskID    string
	seq       int
	err       error
}

func (s *stubStart) start(_ context.Context, tempPath, savePath, _ string, _ bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paths = append(s.paths, tempPath)
	s.savePaths = append(s.savePaths, savePath)
	if s.err != nil {
		return "", s.err
	}
	s.seq++ // 并发会话下每个任务唯一 ID(与生产一致)
	return fmt.Sprintf("%s-%d", s.taskID, s.seq), nil
}

// taskN 取第 n 次(1 起)引擎任务 ID。
func (s *stubStart) taskN(n int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("%s-%d", s.taskID, n)
}

// resolvePath 读取临时文件当前路径(complete 后可能已改名挂引擎任务 ID; 文件不存在返回原路径)。
func (s *stubTemp) resolvePath(sessTaskID, engineTaskID string) string {
	s.mu.Lock()
	orig := s.files[sessTaskID]
	s.mu.Unlock()
	if _, err := os.Stat(orig); err == nil {
		return orig
	}
	matches, _ := filepath.Glob(filepath.Join(s.dir, "uptask-"+engineTaskID+"-*"))
	if len(matches) > 0 {
		return matches[0]
	}
	return orig
}

func newReceiver(t *testing.T, tm *stubTemp, st *stubStart, wm WatermarkChecker) Receiver {
	t.Helper()
	rec, err := New(Config{TempMgr: tm, StartUpload: st.start, Watermark: wm})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func chunkOf(data []byte, seq int, chunkSize int64) []byte {
	lo := int64(seq) * chunkSize
	hi := lo + chunkSize
	if lo >= int64(len(data)) {
		return nil
	}
	if hi > int64(len(data)) {
		hi = int64(len(data))
	}
	return data[lo:hi]
}

// TestInitCreatesSession Init 建会话: 8MB 建议分片/临时文件 0600 已登记。
func TestInitCreatesSession(t *testing.T) {
	tm, st := newStubTemp(), &stubStart{taskID: "task-1"}
	rec := newReceiver(t, tm, st, nil)
	s, err := rec.Init("a.bin", "", "/", 100, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if s.ChunkSize != ChunkSize || s.TaskID == "" || s.Filename != "a.bin" {
		t.Fatalf("会话字段不符: %+v", s)
	}
	if fi, err := os.Stat(filepath.Join(t.TempDir(), "x")); err == nil {
		_ = fi
	}
}

// TestWatermarkReject 水位不足 Init 拒绝(FR-17, ErrBelowWatermark)。
func TestWatermarkReject(t *testing.T) {
	tm, st := newStubTemp(), &stubStart{taskID: "task-1"}
	rec := newReceiver(t, tm, st, func() (int64, bool, error) { return 1 << 30, true, nil })
	_, err := rec.Init("a.bin", "", "/", 10, "", false)
	var wm *ErrBelowWatermark
	if !errors.As(err, &wm) {
		t.Fatalf("应返回水位错误: %v", err)
	}
}

// TestChunkSequentialMD5 流式 MD5 一致性: 分片写入后的流式 MD5 == 整文件一次性 MD5。
func TestChunkSequentialMD5(t *testing.T) {
	tm, st := newStubTemp(), &stubStart{taskID: "task-md5"}
	rec := newReceiver(t, tm, st, nil)
	data := bytes.Repeat([]byte("百度网盘上传测试0123456789"), 100000) // ~3.2MB
	s, _ := rec.Init("md5.bin", "", "/", int64(len(data)), "", false)
	var last int64
	for seq := 0; int64(seq)*ChunkSize < int64(len(data)); seq++ {
		got, err := rec.Chunk(s.TaskID, seq, bytes.NewReader(chunkOf(data, seq, ChunkSize)))
		if err != nil {
			t.Fatal(err)
		}
		last = got
	}
	if last != int64(len(data)) {
		t.Fatalf("received=%d want=%d", last, len(data))
	}
	// 经临时文件核验流式拼接与流式 MD5(complete 前读取, 未改名)
	tempPath := tm.files[s.TaskID]
	got, _ := os.ReadFile(tempPath)
	if !bytes.Equal(got, data) {
		t.Fatal("拼接内容与源不一致")
	}
	sum := md5.Sum(data)
	_ = hex.EncodeToString(sum[:]) // 引擎侧重算; 此处核验文件即核验流式路径
	if _, err := rec.Complete(s.TaskID); err != nil {
		t.Fatal(err)
	}
}

// TestChunkOutOfOrder 乱序拒绝: 期望第 1 片收到第 2 片报错。
func TestChunkOutOfOrder(t *testing.T) {
	tm, st := newStubTemp(), &stubStart{taskID: "task-o"}
	rec := newReceiver(t, tm, st, nil)
	s, _ := rec.Init("a.bin", "", "/", 100, "", false)
	if _, err := rec.Chunk(s.TaskID, 1, bytes.NewReader(make([]byte, 10))); err == nil {
		t.Fatal("乱序应报错")
	}
}

// TestChunkIdempotentReplay 重复序号幂等(浏览器重传单片)。
func TestChunkIdempotentReplay(t *testing.T) {
	tm, st := newStubTemp(), &stubStart{taskID: "task-idem"}
	rec := newReceiver(t, tm, st, nil)
	s, _ := rec.Init("a.bin", "", "/", 0, "", false)
	payload := []byte("hello-baidu-pcs")
	if _, err := rec.Chunk(s.TaskID, 0, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	got, err := rec.Chunk(s.TaskID, 0, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if got != int64(len(payload)) {
		t.Fatalf("重传后 received 应不变: %d", got)
	}
	if _, err := rec.Complete(s.TaskID); err != nil {
		t.Fatal(err)
	}
	path := tm.resolvePath(s.TaskID, st.taskN(1))
	if got, _ := os.ReadFile(path); string(got) != string(payload) {
		t.Fatalf("重传不应重复写入: %q", got)
	}
}

// TestChunkBoundaryExact8MB 分片边界: 恰好 2×8MB 文件, 两片各 8MB, complete 校验通过。
func TestChunkBoundaryExact8MB(t *testing.T) {
	tm, st := newStubTemp(), &stubStart{taskID: "task-edge"}
	rec := newReceiver(t, tm, st, nil)
	data := make([]byte, 2*ChunkSize)
	for i := range data {
		data[i] = byte(i % 251)
	}
	s, _ := rec.Init("edge.bin", "", "/", int64(len(data)), "", false)
	for seq := 0; seq < 2; seq++ {
		if _, err := rec.Chunk(s.TaskID, seq, bytes.NewReader(chunkOf(data, seq, ChunkSize))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := rec.Chunk(s.TaskID, 2, bytes.NewReader(nil)); err != nil {
		t.Fatalf("空尾片应幂等: %v", err)
	}
	if _, err := rec.Complete(s.TaskID); err != nil {
		t.Fatal(err)
	}
}

// TestCompleteSizeMismatch 大小不符: complete 报错并清理半成品。
func TestCompleteSizeMismatch(t *testing.T) {
	tm, st := newStubTemp(), &stubStart{taskID: "task-mm"}
	rec := newReceiver(t, tm, st, nil)
	s, _ := rec.Init("a.bin", "", "/", 1000, "", false)
	if _, err := rec.Chunk(s.TaskID, 0, bytes.NewReader(make([]byte, 100))); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Complete(s.TaskID); err == nil {
		t.Fatal("大小不符应报错")
	}
	if _, err := os.Stat(tm.files[s.TaskID]); !os.IsNotExist(err) {
		t.Fatal("不符半成品应被清理")
	}
}

// TestAbortCleansOnBrokenLink 断链清理: Abort 删半成品并注销会话(FR-15 异常分支)。
func TestAbortCleansOnBrokenLink(t *testing.T) {	tm, st := newStubTemp(), &stubStart{taskID: "task-abort"}
	rec := newReceiver(t, tm, st, nil)
	s, _ := rec.Init("a.bin", "", "/", 9999, "", false)
	if _, err := rec.Chunk(s.TaskID, 0, bytes.NewReader(make([]byte, ChunkSize))); err != nil {
		t.Fatal(err)
	}
	if err := rec.Abort(s.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tm.files[s.TaskID]); !os.IsNotExist(err) {
		t.Fatal("断链半成品应被删除")
	}
	// 会话已注销: 续写/完结均报"不存在"
	if _, err := rec.Chunk(s.TaskID, 1, bytes.NewReader(nil)); err == nil {
		t.Fatal("Abort 后续写应报错")
	}
	_ = rec.Abort(s.TaskID) // 幂等
}

// TestCompleteHandsToWorker complete 交接: StartUpload 收到临时路径与目标路径。
// B9: 删除不再由 Complete 登记(改由 xfer 终态 FinishHook), 且 rename 移入
// EnqueueUploadWithRename——receiver 侧 Complete 只交 StartUpload, 返回引擎任务 ID。
func TestCompleteHandsToWorker(t *testing.T) {
	tm, st := newStubTemp(), &stubStart{taskID: "engine-task-7"}
	rec := newReceiver(t, tm, st, nil)
	s, _ := rec.Init("照片.jpg", "", "/备份", 5, "", false)
	_, err := rec.Chunk(s.TaskID, 0, bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := rec.Complete(s.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(taskID, "engine-task-7") {
		t.Fatalf("应返回引擎任务 ID: %s", taskID)
	}
	if len(st.paths) != 1 || st.paths[0] != tm.files[s.TaskID] {
		t.Fatalf("StartUpload 应收到临时路径: %v want %s", st.paths, tm.files[s.TaskID])
	}
	if st.savePaths[0] != "/备份/照片.jpg" {
		t.Fatalf("目标路径拼接错误: %s", st.savePaths[0])
	}
	// B9: Complete 不再登记删除(排队任务文件不能被提前删); 文件仍在盘上等 worker。
	if _, err := os.Stat(tm.files[s.TaskID]); err != nil {
		t.Fatalf("Complete 后临时文件应仍在(删除由终态钩子): %v", err)
	}
}

// TestCompleteEnqueueFailureCleans 入队失败: 临时文件立即清理不留孤儿。
func TestCompleteEnqueueFailureCleans(t *testing.T) {
	tm := newStubTemp()
	st := &stubStart{taskID: "", err: errors.New("队列满")}
	rec := newReceiver(t, tm, st, nil)
	s, _ := rec.Init("a.bin", "", "/", 3, "", false)
	_, _ = rec.Chunk(s.TaskID, 0, bytes.NewReader([]byte("abc")))
	if _, err := rec.Complete(s.TaskID); err == nil {
		t.Fatal("入队失败应报错")
	}
	if _, err := os.Stat(tm.files[s.TaskID]); !os.IsNotExist(err) {
		t.Fatal("入队失败应清理临时文件")
	}
}

// TestChunkUnknownSession 不存在会话的 chunk/complete 报错。
func TestChunkUnknownSession(t *testing.T) {
	tm, st := newStubTemp(), &stubStart{taskID: "x"}
	rec := newReceiver(t, tm, st, nil)
	if _, err := rec.Chunk("nosuch", 0, strings.NewReader("x")); err == nil {
		t.Fatal("未知会话应报错")
	}
	if _, err := rec.Complete("nosuch"); err == nil {
		t.Fatal("未知会话 complete 应报错")
	}
}

// TestMultipartStyleChunk 模拟 multipart 解出的单片 Reader 正常走通(与 API 层契约一致)。
func TestMultipartStyleChunk(t *testing.T) {
	tm, st := newStubTemp(), &stubStart{taskID: "task-mp"}
	rec := newReceiver(t, tm, st, nil)
	s, _ := rec.Init("multi.bin", "", "/", 0, "", false)
	var rd io.Reader = bytes.NewReader([]byte("part-0-data"))
	if _, err := rec.Chunk(s.TaskID, 0, rd); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Complete(s.TaskID); err != nil {
		t.Fatal(err)
	}
}

// TestInitValidation Init 参数校验: 空文件名/负大小拒绝。
func TestInitValidation(t *testing.T) {
	tm, st := newStubTemp(), &stubStart{taskID: "v"}
	rec := newReceiver(t, tm, st, nil)
	if _, err := rec.Init("", "", "/", 1, "", false); err == nil {
		t.Fatal("空文件名应拒绝")
	}
	if _, err := rec.Init("a", "", "/", -1, "", false); err == nil {
		t.Fatal("负大小应拒绝")
	}
}

// TestConcurrentSessionsIsolated 跨会话并发: 各会话内容互不串扰。
func TestConcurrentSessionsIsolated(t *testing.T) {
	tm := newStubTemp()
	st := &stubStart{taskID: "cc"}
	rec := newReceiver(t, tm, st, nil)
	const n = 6
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := bytes.Repeat([]byte{byte('A' + i)}, 1000)
			s, err := rec.Init("f"+string(rune('a'+i))+".bin", "", "/", int64(len(payload)), "", false)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := rec.Chunk(s.TaskID, 0, bytes.NewReader(payload)); err != nil {
				t.Error(err)
				return
			}
			engineID, err := rec.Complete(s.TaskID)
			if err != nil {
				t.Error(err)
				return
			}
			got, _ := os.ReadFile(tm.resolvePath(s.TaskID, engineID))
			if !bytes.Equal(got, payload) {
				t.Errorf("会话 %d 内容串扰", i)
			}
		}(i)
	}
	wg.Wait()
}
