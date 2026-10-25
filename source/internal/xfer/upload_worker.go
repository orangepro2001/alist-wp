// upload_worker.go W6: 上传通道(FR-15/16, §5.2)。
// EnqueueUpload 建任务并入全局上传队列(并发 max_upload_load, 默认 2);
// worker: 先试秒传(RapidUpload: 文件 MD5+slice-md5, 命中即完成, 不读盘上传)→
// 未命中走分片(Engine.Upload 内部 MultiUploader); policy/noRapid 透传;
// 进度经 queue.update 广播(SSE ≤500ms)。上传断点续传上游 v4.0.0 已移除, 如实不做
// (中断即 failed(interrupted))。秒传探测经 rapidProbe 函数字段注入, 便于桩测。

package xfer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"baidupcs-web/internal/xengine"
)

// RapidProbe 秒传探测钩子: 返回是否命中(桩测注入; nil=不单独探测, 由 Engine.Upload 内部试)。
type RapidProbe func(ctx context.Context, localPath, savePath, policy string, noRapid bool) (bool, error)

// FinishHook 任务终态钩子(FR-17: tempmgr 延迟删临时文件; server 装配注入)。
type FinishHook func(taskID string)

// UploadConfig 上传通道配置。
type UploadConfig struct {
	// Engine 上传引擎(必填)。
	Engine UploadEngine
	// MaxUploadLoad 同时上传文件数(config.MaxUploadLoad, 默认 2)。
	MaxUploadLoad int
	// RapidProbe 秒传探测钩子(nil=依赖 Engine.Upload 内部秒传逻辑)。
	RapidProbe RapidProbe
	// FinishHook 终态钩子(nil=无; FR-17 临时文件延迟删除登记)。
	FinishHook FinishHook
	// Now 单测注入时钟(nil 则 time.Now)。
	Now func() time.Time
}

// uploadManager 上传通道: 独立任务表(与下载共享 queue 的事件总线模型, 独立并发槽)。
type uploadManager struct {
	cfg    UploadConfig
	q      *queue // 复用任务表/事件总线/终态语义
	mu     sync.Mutex
	cancel map[string]context.CancelFunc // 运行中任务的取消函数
}

// newUploadManager 构造上传通道。
func newUploadManager(cfg UploadConfig, q *queue) *uploadManager {
	if cfg.MaxUploadLoad <= 0 {
		cfg.MaxUploadLoad = 2
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	// 上传并发槽独立于下载信号量: 经包装 queue 后由 worker 循环自管。
	return &uploadManager{cfg: cfg, q: q, cancel: make(map[string]context.CancelFunc)}
}

// TempRenamer B9: EnqueueUpload 侧"待改名"语义(可选接口, 不改 Manager 签名)。
// 上传实现(如 *manager)支持时, Complete 先 Enqueue 拿引擎任务 ID, 再由内部把
// 临时文件改名挂到该 ID 命名下, 保证 worker 读到最终路径且删除登记一致。
type TempRenamer interface {
	// RenameTemp 把 tempPath 改名为 uptask-<taskID>-<base>(返回最终路径)。
	RenameTemp(taskID, tempPath string) (string, error)
}

// EnqueueUpload 实现 Manager: 校验临时文件存在 → 建任务行 → 起 worker 协程。
// B9: renamer 非 nil 时(生产装配注入), worker 只在改名成功后才读 tempPath,
// 且改名发生在返回任务 ID 之后、上传开始之前(排队任务不再有"文件先删/旧路径"问题)。
func (u *uploadManager) EnqueueUpload(ctx context.Context, tempPath, savePath, policy string, noRapid bool) (string, error) {
	return u.enqueueUpload(ctx, tempPath, savePath, policy, noRapid, nil)
}

// enqueueUpload 内部入口(renamer 为 uploader 侧注入的改名回调, 可 nil)。
func (u *uploadManager) enqueueUpload(ctx context.Context, tempPath, savePath, policy string, noRapid bool, renamer func(taskID string) (string, error)) (string, error) {
	if savePath == "" {
		return "", fmt.Errorf("上传目标路径为空")
	}
	fi, err := os.Stat(tempPath)
	if err != nil {
		return "", fmt.Errorf("临时文件已失效: %w", err)
	}
	// 复用下载任务表(同一 queue 的事件总线/SSE); Kind=upload, 独立并发槽由 acquireUpload 管。
	slot, release, err := u.q.acquireUpload(ctx, savePath, filepath.Base(savePath), fi.Size())
	if err != nil {
		return "", err
	}
	id := slot.row.ID
	finalPath := tempPath
	if renamer != nil {
		// B9: 引擎任务 ID 已确定, 立即改名(StartUpload 前语义), worker 用最终路径。
		if np, rerr := renamer(id); rerr == nil && np != "" {
			finalPath = np
		}
		// 改名失败保留原路径(tempmgr 引用仍指向原文件, 删除链路不断)
	}
	// 上传脱离请求 ctx 在后台跑, 但仍要用发起请求的那个百度账号(WebDAV 多账号)。
	uctx, cancel := context.WithCancel(xengine.CarryAccount(ctx, context.Background()))
	u.mu.Lock()
	u.cancel[id] = cancel
	u.mu.Unlock()

	go func() {
		defer release()
		defer func() {
			u.mu.Lock()
			delete(u.cancel, id)
			u.mu.Unlock()
		}()
		// running 态由 acquireUpload 已置; 直接执行。
		state, errMsg := u.runUpload(uctx, id, finalPath, savePath, policy, noRapid)
		u.q.finish(id, state, errMsg)
		// FR-17: 任务终态 → tempmgr 登记延迟删除临时文件(10s 内删净)。
		if u.cfg.FinishHook != nil {
			u.cfg.FinishHook(id)
		}
	}()
	return id, nil
}

// RenameTemp 实现 TempRenamer(B9): 把临时文件改名挂到引擎任务 ID 命名下。
func (u *uploadManager) RenameTemp(taskID, tempPath string) (string, error) {
	dir := filepath.Dir(tempPath)
	enginePath := filepath.Join(dir, "uptask-"+taskID+"-"+filepath.Base(tempPath))
	if err := os.Rename(tempPath, enginePath); err != nil {
		return "", err
	}
	return enginePath, nil
}

// runUpload 秒传→分片主流程; 返回终态字符串与失败原因(B11: 局部变量随返回值传递,
// 消除包级 lastUploadErr 多 worker 竞争)。
func (u *uploadManager) runUpload(ctx context.Context, id, tempPath, savePath, policy string, noRapid bool) (string, string) {
	// ① 秒传优先(FR-16): 注入钩子可用时先探; 命中即跳过读盘上传。
	if u.cfg.RapidProbe != nil && !noRapid {
		hit, err := u.cfg.RapidProbe(ctx, tempPath, savePath, policy, noRapid)
		if err != nil {
			return "failed", err.Error()
		}
		if hit {
			u.q.update(id, func(row *TaskRow) { row.Done = row.Total })
			return "done", ""
		}
	}
	// ② 分片上传(Engine.Upload 内部: 未开 noRapid 时也会先试秒传, 与 ① 幂等)。
	param := xengine.UploadParam{
		LocalPath: tempPath,
		SavePath:  savePath,
		Policy:    policy,
		NoRapid:   noRapid,
	}
	// 进度不可从引擎粒度获取(上游引擎无回调透出), 上传任务粒度=整文件; 起步 0%。
	if _, err := u.cfg.Engine.Upload(ctx, param); err != nil {
		if ctx.Err() != nil {
			return "cancelled", "已取消"
		}
		return "failed", err.Error()
	}
	u.q.update(id, func(row *TaskRow) { row.Done = row.Total })
	return "done", ""
}

// cancelUpload 取消上传任务(协作式: ctx 取消, Engine 自行中断)。
func (u *uploadManager) cancelUpload(id string) bool {
	u.mu.Lock()
	cancel, ok := u.cancel[id]
	u.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}
