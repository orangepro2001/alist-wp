package tempmgr

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// newTest 建短间隔管理器(工程纪律: 注入短延迟/短间隔, 不真等生产 10s/5min)。
func newTest(t *testing.T, deleteDelay, sweep, orphan time.Duration) (*impl, string) {
	t.Helper()
	dir := t.TempDir()
	m, err := newTunable(dir, deleteDelay, sweep, orphan, WatermarkBytes)
	if err != nil {
		t.Fatalf("newTunable: %v", err)
	}
	return m, dir
}

// TestNewTaskFileCreates0600 终态创建: 文件存在且权限 0600。
func TestNewTaskFileCreates0600(t *testing.T) {
	m, dir := newTest(t, 50*time.Millisecond, time.Hour, time.Hour)
	p, err := m.NewTaskFile("t1", "报告.txt")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("文件应存在且 0600: %v %v", fi, err)
	}
	if filepath.Dir(p) != dir {
		t.Fatalf("文件应落在临时目录: %s", p)
	}
}

// TestNewTaskFileRejectsTraversal 任务 ID 含路径分隔符拒绝(防穿越)。
func TestNewTaskFileRejectsTraversal(t *testing.T) {
	m, dir := newTest(t, 50*time.Millisecond, time.Hour, time.Hour)
	if _, err := m.NewTaskFile("../evil", "x"); err == nil {
		t.Fatal("应拒绝含分隔符的任务 ID")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
}

// TestDeleteTaskFileWithinDelay 终态删除: deleteDelay(注入 50ms)内删净(FR-17 10s 契约)。
func TestDeleteTaskFileWithinDelay(t *testing.T) {
	m, _ := newTest(t, 50*time.Millisecond, time.Hour, time.Hour)
	p, _ := m.NewTaskFile("t2", "a.bin")
	if err := m.DeleteTaskFile("t2"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return // 已删
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("延迟删除未在注入窗口内生效")
}

// TestDeleteTaskFileIdempotent 幂等: 重复 Delete/未知任务不报错不二次删。
func TestDeleteTaskFileIdempotent(t *testing.T) {
	m, _ := newTest(t, 20*time.Millisecond, time.Hour, time.Hour)
	p, _ := m.NewTaskFile("t3", "a.bin")
	_ = m.DeleteTaskFile("t3")
	_ = m.DeleteTaskFile("t3")
	_ = m.DeleteTaskFile("unknown")
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("文件应已删除")
	}
}

// TestSweepRemovesOrphan 孤儿扫描: 无引用且 mtime>orphanAge 的 uptask- 文件被删。
func TestSweepRemovesOrphan(t *testing.T) {
	m, dir := newTest(t, time.Hour, time.Hour, 40*time.Millisecond)
	stale := filepath.Join(dir, "uptask-orphan-old.bin")
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stale, past, past); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	cancel := make(chan struct{})
	m.StartOrphanSweep(func(map[string]struct{}) {}, cancel)
	go func() { m.sweepOnce(nil); close(done) }()
	<-done
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("孤儿文件应被扫描删除")
	}
}

// TestSweepKeepsFreshAndReferenced 新鲜文件与被引用文件不删(扫描只清真孤儿)。
func TestSweepKeepsFreshAndReferenced(t *testing.T) {
	m, dir := newTest(t, time.Hour, time.Hour, 40*time.Millisecond)
	fresh := filepath.Join(dir, "uptask-fresh.bin")
	_ = os.WriteFile(fresh, []byte("x"), 0o600)
	old := filepath.Join(dir, "uptask-ref-old.bin")
	_ = os.WriteFile(old, []byte("x"), 0o600)
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(old, past, past)
	m.mu.Lock()
	m.refs["ref"] = old
	m.mu.Unlock()
	m.sweepOnce(map[string]struct{}{"ref": {}})
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("新鲜文件不应被删")
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("被引用文件不应被删")
	}
}

// TestSweepTickerPeriodic ticker 周期扫描(注入 30ms 间隔, 等待两轮内生效)。
func TestSweepTickerPeriodic(t *testing.T) {
	m, dir := newTest(t, time.Hour, 30*time.Millisecond, time.Millisecond)
	stale := filepath.Join(dir, "uptask-late.bin")
	_ = os.WriteFile(stale, []byte("x"), 0o600)
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(stale, past, past)
	cancel := make(chan struct{})
	m.StartOrphanSweep(func(map[string]struct{}) {}, cancel)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stale); os.IsNotExist(err) {
			close(cancel)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(cancel)
	t.Fatal("周期扫描未删孤儿")
}

// TestDiskStateBelowWatermark 水位: 注入超大水位 → BelowLimit=true(FR-17 拒新上传依据)。
func TestDiskStateBelowWatermark(t *testing.T) {
	dir := t.TempDir()
	m, err := newTunable(dir, time.Hour, time.Hour, time.Hour, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	st, err := m.DiskState()
	if err != nil {
		t.Fatal(err)
	}
	if !st.BelowLimit || st.FreeBytes >= 1<<62 {
		t.Fatalf("应判水位不足: %+v", st)
	}
}

// TestDiskStateNormal 正常水位: 小阈值 → BelowLimit=false。
func TestDiskStateNormal(t *testing.T) {
	dir := t.TempDir()
	m, err := newTunable(dir, time.Hour, time.Hour, time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	st, err := m.DiskState()
	if err != nil || st.BelowLimit {
		t.Fatalf("水位应正常: %+v err=%v", st, err)
	}
}

// TestUsage 统计临时目录占用(FR-36)。
func TestUsage(t *testing.T) {
	m, dir := newTest(t, time.Hour, time.Hour, time.Hour)
	_ = os.WriteFile(filepath.Join(dir, "uptask-u.bin"), make([]byte, 1024), 0o600)
	got, err := m.Usage()
	if err != nil || got < 1024 {
		t.Fatalf("Usage 应 ≥1024: %d %v", got, err)
	}
}

// TestDeleteTaskFileUnregisteredGlob 未登记任务(重启场景)按前缀定位删除。
func TestDeleteTaskFileUnregisteredGlob(t *testing.T) {
	m, dir := newTest(t, 20*time.Millisecond, time.Hour, time.Hour)
	p := filepath.Join(dir, "uptask-zz-restart.bin")
	_ = os.WriteFile(p, []byte("x"), 0o600)
	if err := m.DeleteTaskFile("zz"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("未登记任务文件应按前缀定位删除")
}

// TestConcurrentSessions 跨会话并发建/删无竞态(线程安全契约)。
func TestConcurrentSessions(t *testing.T) {
	m, _ := newTest(t, 10*time.Millisecond, time.Hour, time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a'+i%26)) + time.Now().Format("150405.000000000")
			if p, err := m.NewTaskFile(id, "f.bin"); err == nil {
				_ = m.DeleteTaskFile(id)
				_ = p
			}
		}(i)
	}
	wg.Wait()
}
