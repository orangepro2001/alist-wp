// eventbus.go W5: SSE 进度事件总线(FR-19, §5.3)。
// publish 覆盖每订阅者"最新快照", 订阅协程按 ≤500ms 节流转发(慢消费者丢旧保新);
// 连接断开(ctx 取消)或 Manager Close 自动注销清理。
// Last-Event-ID 重放按 §4 简化: 每事件带全量进度, 重放=重发当前快照。
package xfer

import (
	"context"
	"sync"
	"time"
)

// MaxPushInterval 进度推送最大间隔(NFR-02: ≤500ms)。
const MaxPushInterval = 500 * time.Millisecond

// subscriber 单个 SSE 订阅者: 最新快照由 bus.mu 保护的 last/dirty 承载。
type subscriber struct {
	id     uint64
	ch     chan Progress // 缓冲 1; pump 投递, 消费端读
	ctx    context.Context
	cancel context.CancelFunc
	last   Progress
	dirty  bool
}

// eventBus 事件总线。锁秩序: queue.mu → eventBus.mu(单向, 无逆向)。
type eventBus struct {
	mu    sync.Mutex
	seq   uint64
	subs  map[uint64]*subscriber
	goCtx context.Context // bus 生命周期(随 Manager Close 全部退订)
	goCxl context.CancelFunc
	wg    sync.WaitGroup
}

func newEventBus() *eventBus {
	ctx, cxl := context.WithCancel(context.Background())
	return &eventBus{subs: make(map[uint64]*subscriber), goCtx: ctx, goCxl: cxl}
}

// subscribe 注册订阅者并启动转发协程(节流 ≤Interval, 缺省/非法取 MaxPushInterval)。
// 返回事件通道; parent 或 bus 退出时通道关闭。
func (b *eventBus) subscribe(parent context.Context, sub Subscribe) <-chan Progress {
	interval := sub.Interval
	if interval <= 0 || interval > MaxPushInterval {
		interval = MaxPushInterval
	}
	ctx, cxl := context.WithCancel(parent)
	s := &subscriber{ch: make(chan Progress, 1), ctx: ctx, cancel: cxl}
	b.mu.Lock()
	b.seq++
	s.id = b.seq
	b.subs[s.id] = s
	b.mu.Unlock()

	b.wg.Add(1)
	go b.pump(s, interval)
	return s.ch
}

// pump 订阅协程: 每 interval 取一次最新快照投递(≤500ms 节流), 断开清理。
func (b *eventBus) pump(s *subscriber, interval time.Duration) {
	defer b.wg.Done()
	defer close(s.ch)
	defer b.detach(s.id)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-b.goCtx.Done():
			return
		case <-tick.C:
			b.mu.Lock()
			p, dirty := s.last, s.dirty
			s.dirty = false
			b.mu.Unlock()
			if !dirty {
				continue
			}
			select {
			case s.ch <- p:
			default:
				// 消费慢: 丢旧保新(缓冲 1 腾位后覆盖)
				select {
				case <-s.ch:
				default:
				}
				select {
				case s.ch <- p:
				default:
				}
			}
		}
	}
}

// detach 注销订阅者。
func (b *eventBus) detach(id uint64) {
	b.mu.Lock()
	delete(b.subs, id)
	b.mu.Unlock()
}

// publish 广播最新进度(覆盖旧快照, 永不阻塞)。
func (b *eventBus) publish(p Progress) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.subs {
		s.last = p
		s.dirty = true
	}
}

// close 停止全部订阅协程(Manager 关闭时)。
func (b *eventBus) close() {
	b.goCxl()
	b.wg.Wait()
}
