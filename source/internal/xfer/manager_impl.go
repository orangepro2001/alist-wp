// manager_impl.go W5: Manager 接口实现(复用 proxy 内部队列)。
// 上传入队为 W6 范围(此处返回"未实现", 接口签名不动); 本波落地下载任务行/SSE/取消/清除。
package xfer

import (
	"context"
	"errors"
	"fmt"
)

// ErrUploadNotReady 上传队列 W6 落地前的占位错误。
var ErrUploadNotReady = errors.New("xfer: 上传队列将在后续版本开放")

// manager Manager 接口实现。
type manager struct {
	p *proxy
	u *uploadManager
}

// NewManager 构造传输任务管理器(含下载代理与 W6 上传通道)。
// ProxyConfig.UploadEngine 为 nil 时上传通道未装配(EnqueueUpload 返回 ErrUploadNotReady);
// 也可装配后经 SetUploadEngine 注入(server 装配期使用)。
func NewManager(cfg ProxyConfig) *manager {
	p := NewProxy(cfg)
	m := &manager{p: p}
	m.u = newUploadManager(UploadConfig{
		MaxUploadLoad: cfg.MaxUploadLoad,
		RapidProbe:    cfg.RapidUploadProbe,
		FinishHook:    cfg.UploadFinishHook,
		Now:           cfg.Now,
	}, p.q)
	m.u.cfg.Engine = cfg.UploadEngine
	return m
}

// EnqueueUpload 实现 Manager(W6): 临时文件 → 引擎秒传/分片上传。
func (m *manager) EnqueueUpload(ctx context.Context, tempPath, savePath, policy string, noRapid bool) (string, error) {
	return m.u.EnqueueUpload(ctx, tempPath, savePath, policy, noRapid)
}

// EnqueueUploadWithRename W6/B9: 同 EnqueueUpload, renamer 在引擎任务 ID 确定后、
// worker 读文件前被调用(把临时文件改名挂到任务 ID 命名下); 失败保留原路径。
// 经可选接口 TempRenamer 透出, 不改 Manager 接口签名。
func (m *manager) EnqueueUploadWithRename(ctx context.Context, tempPath, savePath, policy string, noRapid bool, renamer func(taskID string) (string, error)) (string, error) {
	return m.u.enqueueUpload(ctx, tempPath, savePath, policy, noRapid, renamer)
}

// RenameTemp 实现 TempRenamer(暴露给 uploader 装配层, B9)。
func (m *manager) RenameTemp(taskID, tempPath string) (string, error) { return m.u.RenameTemp(taskID, tempPath) }

// Cancel 实现 Manager。
func (m *manager) Cancel(taskID string) error { return m.p.q.cancel(taskID) }

// Retry 实现 Manager(下载任务由浏览器重发起流; 无排队任务可重试)。
func (m *manager) Retry(taskID string) error {
	row, ok := m.p.q.get(taskID)
	if !ok {
		return fmt.Errorf("任务不存在: %s", taskID)
	}
	if row.State != "failed" && row.State != "cancelled" {
		return fmt.Errorf("仅失败/取消任务可重试: %s", row.State)
	}
	return errors.New("下载任务请在文件页重新发起下载")
}

// Purge 实现 Manager。
func (m *manager) Purge(taskID string) error { return m.p.q.purge(taskID) }

// List 实现 Manager。
func (m *manager) List() []Progress {
	rows := m.p.q.list()
	out := make([]Progress, 0, len(rows))
	for _, r := range rows {
		out = append(out, Progress{
			TaskID:   r.ID,
			Kind:     r.Kind,
			State:    r.State,
			Percent:  percent(r.Done, r.Total),
			SpeedBps: r.Speed,
			Error:    r.Err,
		})
	}
	return out
}

// Events 实现 Manager(SSE 数据源)。
func (m *manager) Events(ctx context.Context, sub Subscribe) (<-chan Progress, error) {
	return m.p.q.subscribe(ctx, sub)
}

// WaitIdle 实现 Manager(测试辅助: 等待全部任务终态)。
func (m *manager) WaitIdle(ctx context.Context) error {
	for {
		rows := m.p.q.list()
		busy := false
		for _, r := range rows {
			if r.State == "queued" || r.State == "running" {
				busy = true
				break
			}
		}
		if !busy {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.p.q.bus.goCtx.Done():
			return errors.New("xfer: manager 已关闭")
		}
	}
}

// Proxy 暴露下载代理接口(handler 装配用)。
func (m *manager) Proxy() Proxy { return m.p }

// Rows 任务行明细(展示层需要 total/done 等全字段, List() 的 Progress 不够)。
func (m *manager) Rows() []TaskRow { return m.p.q.list() }

// Row 单条任务行。
func (m *manager) Row(id string) (TaskRow, bool) { return m.p.q.get(id) }

// ResolveForURL 透传代理签名 URL 生成(文件页 <a> 下载用)。
func (m *manager) ResolveForURL(pcspath string) (string, error) { return m.p.ResolveForURL(pcspath) }

// Close 停止事件总线。
func (m *manager) Close() { m.p.Close() }

// SetUploadEngine 装配期注入上传引擎(server 装配调用一次; 必须在首次 EnqueueUpload 前)。
func (m *manager) SetUploadEngine(eng UploadEngine) { m.u.cfg.Engine = eng }
