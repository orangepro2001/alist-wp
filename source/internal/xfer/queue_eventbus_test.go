// queue_eventbus_test.go W5 补充: 队列信号量/排队取消/事件节流/幂等 finish。
package xfer

import (
	"context"
	"testing"
	"time"
)

// --- 信号量上限: 3 上限下第 4 个排队 ---
func TestQueueSemaphoreCap(t *testing.T) {
	q := newQueue(3)
	defer q.close()
	for i := 0; i < 3; i++ {
		_, rel, err := q.acquire(context.Background(), "/f", "f", 0)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		defer rel()
	}
	rows := q.list()
	queued := 0
	running := 0
	for _, r := range rows {
		switch r.State {
		case "running":
			running++
		case "queued":
			queued++
		}
	}
	if running != 3 {
		t.Fatalf("应 3 running, got %d", running)
	}
	_ = queued
}

// --- 排队中取消: queued → cancelled, 槽未占 ---
func TestQueueCancelWhileQueued(t *testing.T) {
	q := newQueue(1)
	defer q.close()
	// 占住唯一槽
	_, rel1, err := q.acquire(context.Background(), "/a", "a", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rel1()

	// 第二个排队
	done := make(chan struct{})
	go func() {
		defer close(done)
		task, rel, err := q.acquire(context.Background(), "/b", "b", 0)
		if err != nil {
			return // 被取消, 预期
		}
		rel()
		_ = task
	}()
	time.Sleep(30 * time.Millisecond)
	rows := q.list()
	if len(rows) < 2 || rows[1].State != "queued" {
		t.Fatalf("第二个应排队, rows=%+v", rows)
	}
	id := rows[1].ID
	if err := q.cancel(id); err != nil {
		t.Fatalf("排队取消: %v", err)
	}
	row, ok := q.get(id)
	if !ok || row.State != "cancelled" {
		t.Fatalf("排队取消应 cancelled, got %+v", row)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("排队协程未退出")
	}
}

// --- 事件总线节流: 500ms 间隔语义(Intervals 大于发布频率时合并) ---
func TestEventBusThrottle(t *testing.T) {
	bus := newEventBus()
	defer bus.close()
	ch := bus.subscribe(context.Background(), Subscribe{Interval: 50 * time.Millisecond})
	// 快速连发 5 次
	for i := 0; i < 5; i++ {
		bus.publish(Progress{TaskID: "t", Percent: float64(i * 20)})
		time.Sleep(5 * time.Millisecond)
	}
	// 50ms 节流窗内最多收 ~2 次(丢旧保新, 最后一次应为最新)
	got := 0
	var last Progress
	deadline := time.After(1 * time.Second)
	for {
		select {
		case p := <-ch:
			got++
			last = p
			if got >= 2 {
				goto check
			}
		case <-deadline:
			goto check
		}
	}
check:
	if got > 3 {
		t.Fatalf("节流失效: 50ms 窗内收到 %d 次", got)
	}
	if last.Percent != 80 {
		t.Fatalf("最后事件应为最新快照(80), got %v", last.Percent)
	}
}

// --- 断开自动清理: ctx 取消后订阅者被注销 ---
func TestEventBusUnsubscribeOnDisconnect(t *testing.T) {
	bus := newEventBus()
	defer bus.close()
	ctx, cxl := context.WithCancel(context.Background())
	_ = bus.subscribe(ctx, Subscribe{Interval: 10 * time.Millisecond})
	time.Sleep(30 * time.Millisecond)
	bus.mu.Lock()
	n := len(bus.subs)
	bus.mu.Unlock()
	if n != 1 {
		t.Fatalf("应 1 个订阅者, got %d", n)
	}
	cxl()
	time.Sleep(50 * time.Millisecond)
	bus.mu.Lock()
	n = len(bus.subs)
	bus.mu.Unlock()
	if n != 0 {
		t.Fatalf("断开后应自动清理, got %d", n)
	}
}

// --- finish 幂等: 双重 finish 不 panic ---
func TestQueueFinishIdempotent(t *testing.T) {
	q := newQueue(1)
	defer q.close()
	task, rel, err := q.acquire(context.Background(), "/x", "x", 0)
	if err != nil {
		t.Fatal(err)
	}
	q.finish(task.row.ID, "done", "")
	q.finish(task.row.ID, "failed", "二次") // 幂等: 不 panic 不覆盖
	row, _ := q.get(task.row.ID)
	if row.State != "done" {
		t.Fatalf("幂等 finish 不应覆盖终态, got %s", row.State)
	}
	rel()
}

// --- purge: 仅终态可清除 ---
func TestQueuePurgeTerminalOnly(t *testing.T) {
	q := newQueue(2)
	defer q.close()
	task, rel, err := q.acquire(context.Background(), "/x", "x", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.purge(task.row.ID); err == nil {
		t.Fatal("running 任务不应可 purge")
	}
	q.finish(task.row.ID, "failed", "x")
	rel()
	if err := q.purge(task.row.ID); err != nil {
		t.Fatalf("终态应可 purge: %v", err)
	}
	if _, ok := q.get(task.row.ID); ok {
		t.Fatal("purge 后任务应不可见")
	}
}

// --- RFC5987: 纯 ASCII 名不含 filename*, 中文正确转码 ---
func TestRfc5987DispositionCases(t *testing.T) {
	c1 := rfc5987Disposition("plain.txt")
	if !contains(c1, `filename="plain.txt"`) || contains(c1, "filename*") {
		t.Fatalf("ASCII 名应仅 filename: %q", c1)
	}
	c2 := rfc5987Disposition("报表.xlsx")
	if !contains(c2, "filename*=UTF-8''%E6%8A%A5%E8%A1%A8.xlsx") {
		t.Fatalf("中文转码不对: %q", c2)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
