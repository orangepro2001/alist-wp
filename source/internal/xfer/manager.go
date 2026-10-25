// Package xfer 传输任务管理器(服务层核心, FR-14/18/19/20)。
// 上传队列(全局并发 max_upload_load, 默认 2) + 下载代理并发(max_download_load, 默认 3)
// + 状态机驱动 + SSE 事件广播。
package xfer

import (
	"context"
	"time"
)

// Progress 任务进度快照(SSE task 事件载荷, 需求 §6 约定)。
type Progress struct {
	TaskID    string `json:"task_id"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	Percent   float64 `json:"percent"`
	SpeedBps  int64  `json:"speed_bps"`
	ETA       int64  `json:"eta_sec"`
	Error     string `json:"error,omitempty"`
}

// Subscribe 事件订阅选项。
type Subscribe struct {
	LastEventID string        // SSE Last-Event-ID, 断线重放(可选)
	Interval    time.Duration // 服务端最小推送间隔(≤500ms, NFR-02)
}

// Manager 传输任务管理器。线程安全; 方法均为非阻塞(长操作由内部 goroutine 承担)。
type Manager interface {
	// EnqueueUpload 建立上传任务: tempPath 为已接收完毕的临时文件(FR-15 complete 之后)。
	EnqueueUpload(ctx context.Context, tempPath, savePath, policy string, noRapid bool) (taskID string, err error)
	// DownloadWriter 见 download.go: 下载不走任务队列的 worker 模型, 单独接口。
	// Cancel 取消任务: queued 立即取消; running 发协作取消信号(FR-20)。
	Cancel(taskID string) error
	// Retry 重试失败任务(上传重试=重新入队, 临时文件须仍在; 否则报"已失效")。
	Retry(taskID string) error
	// Purge 清除已终结任务记录。
	Purge(taskID string) error
	// List 摘要列表。
	List() []Progress
	// Events 订阅进度事件(SSE 数据源, FR-19)。
	Events(ctx context.Context, sub Subscribe) (<-chan Progress, error)
	// WaitIdle 测试辅助: 等待全部任务终态。
	WaitIdle(ctx context.Context) error
}
