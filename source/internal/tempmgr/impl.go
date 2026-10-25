// impl.go W6: tempmgr.Manager 实现(FR-17/D3/D4)。
// 职责: 上传临时文件创建(0600)/终态延迟删除(默认 10s, time.AfterFunc)/孤儿扫描
// (启动时+每 5min, 无任务引用且 mtime>24h 即删)/磁盘水位(剩余 <2GB 拒绝新上传)。
// 测试纪律: 删除延迟/扫描间隔/孤儿阈值经 newTunable 注入短值, 用例不真等生产间隔。

package tempmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// WatermarkBytes 磁盘水位阈值(FR-17: 临时目录剩余 <2GB 拒绝新上传)。
const WatermarkBytes int64 = 2 << 30

// taskFilePrefix 任务临时文件名前缀(孤儿扫描识别用): uptask-<taskID>-<filename>。
const taskFilePrefix = "uptask-"

// impl Manager 实现。线程安全: refs/timers 由 mu 保护。
type impl struct {
	dir           string
	deleteDelay   time.Duration // 终态→删除延迟(生产 10s)
	sweepInterval time.Duration // 孤儿扫描周期(生产 5min)
	orphanMaxAge  time.Duration // 孤儿判定阈值(生产 24h)
	watermark     int64         // 磁盘水位(生产 2GB)

	mu     sync.Mutex
	refs   map[string]string   // taskID → 临时文件路径(活跃任务引用集)
	timers map[string]*time.Timer // 已调度延迟删除的任务(幂等标记)
	nowFn  func() time.Time
}

// New 构造生产参数的 Manager(目录不存在则创建, 0700)。
func New(dir string) (Manager, error) {
	return newTunable(dir, DeleteDelay, SweepInterval, OrphanMaxAge, WatermarkBytes)
}

// newTunable 注入调参构造(单测用短间隔/短延迟; 不对外导出, 生产走 New)。
func newTunable(dir string, deleteDelay, sweepInterval, orphanMaxAge time.Duration, watermark int64) (*impl, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建临时目录 %s: %w", dir, err)
	}
	return &impl{
		dir:           dir,
		deleteDelay:   deleteDelay,
		sweepInterval: sweepInterval,
		orphanMaxAge:  orphanMaxAge,
		watermark:     watermark,
		refs:          make(map[string]string),
		timers:        make(map[string]*time.Timer),
		nowFn:         time.Now,
	}, nil
}

// Dir 实现 Manager。
func (m *impl) Dir() string { return m.dir }

// NewTaskFile 实现 Manager: 建唯一临时文件(0600, D3/D4)并登记任务引用。
func (m *impl) NewTaskFile(taskID, filename string) (string, error) {
	if taskID == "" || strings.ContainsAny(taskID, "/\\") {
		return "", fmt.Errorf("非法任务 ID: %q", taskID)
	}
	name := taskFilePrefix + taskID + "-" + sanitizeName(filename)
	path := filepath.Join(m.dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("创建临时文件 %s: %w", path, err)
	}
	f.Close()
	m.mu.Lock()
	m.refs[taskID] = path
	m.mu.Unlock()
	return path, nil
}

// DeleteTaskFile 实现 Manager: 任务终态后延迟 deleteDelay 删除(FR-17, 10s 内); 幂等。
// 重复调用/未知任务均为无害 no-op; 延迟窗口内孤儿扫描不会误删(refs 未清且 mtime 新鲜)。
func (m *impl) DeleteTaskFile(taskID string) error {
	m.mu.Lock()
	if _, pending := m.timers[taskID]; pending {
		m.mu.Unlock()
		return nil // 已调度, 幂等
	}
	path := m.refs[taskID]
	if path == "" {
		// 未登记(如服务重启后按任务 ID 补删): 按前缀定位, 找不到视为已删。
		matches, _ := filepath.Glob(filepath.Join(m.dir, taskFilePrefix+taskID+"-*"))
		if len(matches) == 0 {
			m.mu.Unlock()
			return nil
		}
		path = matches[0]
		m.refs[taskID] = path
	}
	taskIDCopy, pathCopy := taskID, path
	m.timers[taskID] = time.AfterFunc(m.deleteDelay, func() { m.removeNow(taskIDCopy, pathCopy) })
	m.mu.Unlock()
	return nil
}

// removeNow 延迟到期执行: 删文件并注销引用(文件已不存在时 Remove 静默)。
func (m *impl) removeNow(taskID, path string) {
	_ = os.Remove(path)
	m.mu.Lock()
	delete(m.refs, taskID)
	delete(m.timers, taskID)
	m.mu.Unlock()
}

// StartOrphanSweep 实现 Manager: 启动时立即全扫一次, 之后每 sweepInterval 扫描;
// 孤儿=临时目录中无任务引用(不在 referenced 集合)且 mtime 超过 orphanMaxAge 的文件。
// cancel 关闭后扫描协程退出。FR-40 的 .tmp- 网盘临时目录清理在 W8 转存域接入(本地无映射)。
func (m *impl) StartOrphanSweep(referenced func(taskIDs map[string]struct{}), cancel <-chan struct{}) {
	go func() {
		sweep := func() {
			refs := make(map[string]struct{})
			if referenced != nil {
				referenced(refs)
			}
			m.sweepOnce(refs)
		}
		sweep() // 启动全扫(FR-17: 服务重启后残留清理)
		ticker := time.NewTicker(m.sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-cancel:
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
}

// sweepOnce 单轮孤儿扫描(只处理本管理器命名约定的 uptask- 前缀文件, 不碰外来文件)。
func (m *impl) sweepOnce(referenced map[string]struct{}) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	cutoff := m.nowFn().Add(-m.orphanMaxAge)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), taskFilePrefix) {
			continue
		}
		id := e.Name()[len(taskFilePrefix):]
		if i := strings.IndexByte(id, '-'); i >= 0 {
			id = id[:i]
		}
		if _, active := referenced[id]; active {
			continue // 活跃任务引用, 即使 mtime 很旧也不删
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue // 太新, 非孤儿(含延迟删除窗口内的文件)
		}
		_ = os.Remove(filepath.Join(m.dir, e.Name()))
		m.mu.Lock()
		delete(m.refs, id)
		m.mu.Unlock()
	}
}

// DiskState 实现 Manager: statfs 读剩余空间并与水位比较(FR-17)。
func (m *impl) DiskState() (DiskState, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(m.dir, &st); err != nil {
		return DiskState{}, fmt.Errorf("读取临时目录磁盘状态: %w", err)
	}
	free := int64(st.Bavail) * int64(st.Bsize)
	return DiskState{FreeBytes: free, BelowLimit: free < m.watermark}, nil
}

// Usage 实现 Manager: 临时目录当前占用字节数(FR-36 状态页)。
func (m *impl) Usage() (int64, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return total, nil
}

// sanitizeName 文件名收敛: 取 base 并替换路径分隔符, 防目录穿越。
func sanitizeName(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." || base == ".." || base == string(filepath.Separator) {
		return "unnamed"
	}
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 {
			return '_'
		}
		return r
	}, base)
}
