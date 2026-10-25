// queue.go W5: 传输任务表 + 下载并发信号量 + 事件发布(FR-14/18/19/20, §5.3)。
// 下载不入 worker 队列(浏览器直连代理流), 只占 max_download_load 信号量并记任务行;
// 排队中的任务在 /api/transfers 可见(state=queued)。
package xfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// TaskRow 任务行(/api/tasks 与 SSE 展示)。
type TaskRow struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // download | upload(W6)
	Name   string `json:"name"`
	Path   string `json:"path"` // 网盘路径
	State  string `json:"state"`
	Total  int64  `json:"total_size"`
	Done   int64  `json:"done_size"`
	Speed  int64  `json:"speed_bps"`
	Err    string `json:"error,omitempty"`
	UpAt   int64  `json:"updated_at"`
}

// dlTask 单个下载任务的运行态。
type dlTask struct {
	row     TaskRow
	cancel   context.CancelFunc
	done     chan struct{} // 关闭即终态
	finished bool          // 幂等标记(finish 只生效一次)
	semRele func()        // 信号量释放钩子(终态时调用一次)
}

// queue 任务表 + 信号量 + 事件总线。
// 锁秩序: queue.mu → eventBus.mu(单向, 禁止逆向)。
type queue struct {
	mu    sync.Mutex
	tasks map[string]*dlTask
	order []string // 展示顺序(创建序)
	sem   chan struct{}
	uploadSem chan struct{} // 上传并发槽(W6, max_upload_load)
	bus   *eventBus
	nowFn func() time.Time
}

func newQueue(maxParallel int) *queue {
	return newQueueWithUploadLoad(maxParallel, 0)
}

// newQueueWithUploadLoad B10: 上传并发槽独立传参(≤0 默认 2, 取 cfg.MaxUploadLoad)。
func newQueueWithUploadLoad(maxParallel, uploadLoad int) *queue {
	if maxParallel <= 0 {
		maxParallel = 3
	}
	if uploadLoad <= 0 {
		uploadLoad = 2
	}
	return &queue{
		tasks:     make(map[string]*dlTask),
		sem:       make(chan struct{}, maxParallel),
		uploadSem: make(chan struct{}, uploadLoad),
		bus:       newEventBus(),
		nowFn:     time.Now,
	}
}

// newTaskID 随机任务 id。
func newTaskID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// acquire 获取下载并发槽(排队阻塞); 返回释放钩子。ctx 取消返回错误(排队中取消)。
// 排队期间先建 queued 任务行(SSE/列表可见), 拿到槽转 running。
func (q *queue) acquire(ctx context.Context, pcspath, name string, total int64) (slot *dlTask, release func(), err error) {
	id := newTaskID()
	ctx2, cancel := context.WithCancel(ctx)
	t := &dlTask{row: TaskRow{
		ID: id, Kind: "download", Name: name, Path: pcspath,
		State: "queued", Total: total, UpAt: q.nowFn().Unix(),
	}, cancel: cancel, done: make(chan struct{})}
	q.mu.Lock()
	q.tasks[id] = t
	q.order = append(q.order, id)
	q.mu.Unlock()
	q.publish(t.row)

	releaseOnce := false
	var onceMu sync.Mutex
	release = func() {
		onceMu.Lock()
		first := !releaseOnce
		releaseOnce = true
		onceMu.Unlock()
		if first {
			<-q.sem // 不持 queue.mu(锁秩序: sem 独立于 mu)
		}
	}

	// 等信号量或取消
	select {
	case q.sem <- struct{}{}:
	case <-ctx2.Done():
		q.finish(id, "cancelled", "已取消")
		cancel()
		return nil, nil, ctx2.Err()
	}

	q.mu.Lock()
	t.row.State = "running"
	t.row.UpAt = q.nowFn().Unix()
	t.semRele = release
	q.mu.Unlock()
	q.publish(t.row)
	return t, release, nil
}

// update 原子更新任务行进度并广播。
func (q *queue) update(id string, fn func(*TaskRow)) {
	q.mu.Lock()
	t, ok := q.tasks[id]
	if !ok {
		q.mu.Unlock()
		return
	}
	fn(&t.row)
	t.row.UpAt = q.nowFn().Unix()
	row := t.row
	q.mu.Unlock()
	q.publish(row)
}

// finish 置终态并释放信号量; done 关闭即任务终结。
func (q *queue) finish(id, state, errMsg string) {
	q.mu.Lock()
	t, ok := q.tasks[id]
	if !ok {
		q.mu.Unlock()
		return
	}
	if t.finished {
		q.mu.Unlock()
		return // 幂等: 首个终态生效, 后续覆盖忽略
	}
	t.finished = true
	t.row.State = state
	t.row.Err = errMsg
	t.row.UpAt = q.nowFn().Unix()
	row := t.row
	rele := t.semRele
	close(t.done)
	q.mu.Unlock()
	if rele != nil {
		rele() // 锁外释放(信号量接收可能阻塞)
	}
	q.publish(row)
}

// cancel 协作取消: queued(未拿槽)取消等待并直接终态; running 置 cancelled 终态,
// 代理循环经 task.done/copyCtx 感知后收尾(close(done) 后代理侧 finish 幂等跳过)。
func (q *queue) cancel(id string) error {
	q.mu.Lock()
	t, ok := q.tasks[id]
	var row TaskRow
	if ok {
		row = t.row // B10: 锁内快照(解锁后读 t.row.State 与并发更新构成竞争)
	}
	q.mu.Unlock()
	if !ok {
		return fmt.Errorf("任务不存在: %s", id)
	}
	select {
	case <-t.done:
		return fmt.Errorf("任务已终态: %s", row.State)
	default:
	}
	t.cancel() // 取消排队等待(acquire 的 ctx2)
	switch row.State {
	case "queued":
		q.finish(id, "cancelled", "已取消")
	default:
		// running: 置终态并释放槽; 代理写循环下次感知 copyCtx 取消即返回
		q.finish(id, "cancelled", "已取消")
	}
	return nil
}

// acquireUpload W6: 上传任务槽(独立于下载信号量的并发上限, max_upload_load)。
// 复用同一任务表/事件总线(传输页统一展示), Kind=upload。
func (q *queue) acquireUpload(ctx context.Context, pcspath, name string, total int64) (*dlTask, func(), error) {
	id := newTaskID()
	ctx2, cancel := context.WithCancel(ctx)
	t := &dlTask{row: TaskRow{
		ID: id, Kind: "upload", Name: name, Path: pcspath,
		State: "queued", Total: total, UpAt: q.nowFn().Unix(),
	}, cancel: cancel, done: make(chan struct{})}
	q.mu.Lock()
	q.tasks[id] = t
	q.order = append(q.order, id)
	q.mu.Unlock()
	q.publish(t.row)

	var release func()
	releaseOnce := false
	var onceMu sync.Mutex
	release = func() {
		onceMu.Lock()
		first := !releaseOnce
		releaseOnce = true
		onceMu.Unlock()
		if first {
			<-q.uploadSem
		}
	}

	select {
	case q.uploadSem <- struct{}{}:
	case <-ctx2.Done():
		q.finish(id, "cancelled", "已取消")
		cancel()
		return nil, nil, ctx2.Err()
	}

	q.mu.Lock()
	t.row.State = "running"
	t.row.UpAt = q.nowFn().Unix()
	t.semRele = release
	q.mu.Unlock()
	q.publish(t.row)
	return t, release, nil
}

func (q *queue) list() []TaskRow {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]TaskRow, 0, len(q.order))
	for _, id := range q.order {
		if t, ok := q.tasks[id]; ok {
			out = append(out, t.row)
		}
	}
	return out
}

// get 取任务行。
func (q *queue) get(id string) (TaskRow, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if t, ok := q.tasks[id]; ok {
		return t.row, true
	}
	return TaskRow{}, false
}

// subscribe 订阅事件(透传 eventBus)。
func (q *queue) subscribe(ctx context.Context, sub Subscribe) (<-chan Progress, error) {
	return q.bus.subscribe(ctx, sub), nil
}

// publish TaskRow → Progress 广播(SSE 载荷契约: 任务 id/速度/百分比/状态)。
func (q *queue) publish(r TaskRow) {
	q.bus.publish(Progress{
		TaskID:   r.ID,
		Kind:     r.Kind,
		State:    r.State,
		Percent:  percent(r.Done, r.Total),
		SpeedBps: r.Speed,
		Error:    r.Err,
	})
}

// percent 百分比(0~100, total≤0 时 0)。
func percent(done, total int64) float64 {
	if total <= 0 {
		return 0
	}
	p := float64(done) / float64(total) * 100
	if p > 100 {
		p = 100
	}
	return p
}

// purge 清除终态任务记录。
func (q *queue) purge(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, ok := q.tasks[id]
	if !ok {
		return fmt.Errorf("任务不存在: %s", id)
	}
	select {
	case <-t.done:
	default:
		return fmt.Errorf("任务未终态, 禁止清除: %s", id)
	}
	delete(q.tasks, id)
	for i, v := range q.order {
		if v == id {
			q.order = append(q.order[:i], q.order[i+1:]...)
			break
		}
	}
	return nil
}

// close 关闭事件总线(Manager 退出)。
func (q *queue) close() { q.bus.close() }
