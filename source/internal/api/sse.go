package api

// sse.go W5: /api/events 进度推送(FR-19, §5.3)。
// 订阅 xfer Manager 事件总线(≤500ms 节流), event: task 全量进度快照;
// 连接断开经 r.Context() 自动注销清理; 心跳 15s 防代理空闲断连。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"baidupcs-web/internal/xfer"
)

// sseHeartbeat 心跳间隔(主流代理空闲超时 30~60s, 15s 足够安全)。
const sseHeartbeat = 15 * time.Second

// EventsSource SSE 进度源: 订阅事件 + 拉全量快照(重放用)。由 server 装配注入。
type EventsSource interface {
	Subscribe(ctx context.Context, interval time.Duration) (<-chan xfer.Progress, error)
	Snapshot() []xfer.Progress
}

// managerSource xfer.Manager 适配。
type managerSource struct{ m xfer.Manager }

func (s managerSource) Subscribe(ctx context.Context, interval time.Duration) (<-chan xfer.Progress, error) {
	return s.m.Events(ctx, xfer.Subscribe{Interval: interval})
}

func (s managerSource) Snapshot() []xfer.Progress { return s.m.List() }

// NewEvents 构造 GET /api/events 处理器(mgr 可为 nil→退化为纯心跳, 单测兼容)。
func NewEvents(mgr xfer.Manager) http.HandlerFunc {
	var src EventsSource
	if mgr != nil {
		src = managerSource{m: mgr}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeErr(w, http.StatusInternalServerError, "INTERNAL", "当前连接不支持流式响应")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, ": connected\n\n")
		flusher.Flush()

		var events <-chan xfer.Progress
		if src != nil {
			// Last-Event-ID 重放(§4 简化: 重发全量快照)
			if r.Header.Get("Last-Event-ID") != "" {
				for _, p := range src.Snapshot() {
					writeEvent(w, flusher, p)
				}
			}
			ch, err := src.Subscribe(r.Context(), xfer.MaxPushInterval)
			if err == nil {
				events = ch
			}
		}

		ticker := time.NewTicker(sseHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return // 断开自动清理(订阅协程随 ctx 退出)
			case p, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				writeEvent(w, flusher, p)
			case <-ticker.C:
				fmt.Fprintf(w, ": ping %d\n\n", time.Now().Unix())
				flusher.Flush()
			}
		}
	}
}

// writeEvent 输出一条 event: task。
func writeEvent(w http.ResponseWriter, f http.Flusher, p xfer.Progress) {
	data, err := json.Marshal(p)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: task\ndata: %s\n\n", data)
	f.Flush()
}
