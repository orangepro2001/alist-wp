// impl.go W8: FR-40 分享直连下载编排器实现(§5.4 状态机)。
// parsing→previewing(复用 Engine.SharePreview, 提取码错/禁止转存在此暴露)→
// checking(配额预检: 需求空间=勾选文件总大小)→transferring(TransferToTmp→/来自分享/.tmp-<shareid>)→
// downloading(逐文件经下载代理解析器流式回传, 记任务行)→cleaning(全部终态+延迟删除)→done/failed。
// 异常分支: 配额不足(无 .tmp- 残留)/禁止转存/中途失败回删/重启孤儿扫(.tmp-* 网盘目录)。
// 纪律: 引擎/下载/删除能力全部函数字段注入, 便于桩测(不覆写方法); 定时用短注入值。

package sharetmp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// 默认时序(生产): 终态后 10s 删目录; 重启孤儿扫兜底见 SweepPanTmp。
const (
	DefaultDeleteDelay  = 10 * time.Second
	DefaultPollInterval = 50 * time.Millisecond // 下载终态轮询(内部, 非对百度)
)

// Deps 编排器依赖注入面(全部桩可替)。
type Deps struct {
	// PreviewFn 浏览分享内容(Engine.SharePreview; 提取码错/禁止转存返回错误)。
	PreviewFn func(ctx context.Context, link, passcode string) (items []Item, shareID string, err error)
	// FreeBytesFn 网盘剩余空间(配额预检)。
	FreeBytesFn func(ctx context.Context) (int64, error)
	// TransferFn 临时转存(Engine.TransferToTmp): 返回转存后文件清单。
	TransferFn func(ctx context.Context, link, passcode string, fsids []string) (targetDir string, files []Item, err error)
	// RemoveFn 删除网盘路径(回删残留/终态删目录; 幂等)。
	RemoveFn func(ctx context.Context, paths []string) error
	// DownloadFn 单文件下载(FR-11 流式代理): 阻塞至终态, 返回终态错误(nil=成功)。
	// cancel 经 ctx 透传(协作取消)。
	DownloadFn func(ctx context.Context, pcspath string) error
	// DeleteDelay 终态→删目录延迟(默认 10s; 用例注入短值)。
	DeleteDelay time.Duration
	// Now 单测时钟(nil 则 time.Now)。
	Now func() time.Time
}

// Item 分享内容/转存文件条目(xengine.SharePreviewItem 镜像子集)。
type Item struct {
	FsID     string
	Path     string
	Filename string
	Size     int64
}

// jobState 作业运行态。
type jobState struct {
	job     Job
	items   []Item // previewing 后的分享内容清单(勾选候选)
	target  string // 转存后的网盘临时目录
	files   []Item // 转存产物(下载清单)
	cancel  context.CancelFunc
	done    chan struct{} // 关闭即作业终态
	finished bool
}

// orch Orchestrator 实现。
type orch struct {
	deps Deps

	mu      sync.Mutex
	jobs    map[string]*jobState
	order   []string
	nowFn   func() time.Time
}

// New 构造编排器。
func New(deps Deps) (Orchestrator, error) {
	if deps.PreviewFn == nil || deps.TransferFn == nil || deps.RemoveFn == nil || deps.DownloadFn == nil {
		return nil, fmt.Errorf("sharetmp: PreviewFn/TransferFn/RemoveFn/DownloadFn 必填")
	}
	if deps.FreeBytesFn == nil {
		deps.FreeBytesFn = func(context.Context) (int64, error) { return 1 << 62, nil } // 缺省不设限
	}
	if deps.DeleteDelay <= 0 {
		deps.DeleteDelay = DefaultDeleteDelay
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &orch{deps: deps, jobs: make(map[string]*jobState), nowFn: deps.Now}, nil
}

// Preview 实现 Orchestrator: parsing→previewing(不创建任何网盘目录)。
func (o *orch) Preview(link, passcode string) (*Job, error) {
	if link == "" {
		return nil, fmt.Errorf("分享链接为空")
	}
	job := &Job{ID: newJobID(), Link: link, Passcode: passcode, Phase: PhaseParsing}
	items, shareID, err := o.deps.PreviewFn(context.Background(), link, passcode)
	if err != nil {
		job.Phase = PhaseFailed
		job.ErrCode = "PREVIEW_FAILED"
		job.Message = err.Error()
		o.track(job, items)
		return job, err
	}
	job.ShareID = shareID
	job.Phase = PhasePreviewing
	o.track(job, items)
	out := *job
	return &out, nil
}

// StartDownload 实现 Orchestrator: checking→transferring→downloading→cleaning(异步执行)。
func (o *orch) StartDownload(jobID string, fsids []string) error {
	o.mu.Lock()
	st, ok := o.jobs[jobID]
	if !ok {
		o.mu.Unlock()
		return fmt.Errorf("作业不存在: %s", jobID)
	}
	// B2: finished 判定与启动态赋值(cancel/done/FSIDs)必须同在 o.mu 临界区,
	// 否则与 Cancel/List 锁内读写构成竞争, 且同作业可能二次启动导致双 close(done)。
	if st.finished {
		o.mu.Unlock()
		return fmt.Errorf("作业已终态: %s", st.job.Phase)
	}
	if st.cancel != nil {
		o.mu.Unlock()
		return fmt.Errorf("作业已在运行: %s", jobID)
	}
	if len(fsids) == 0 {
		o.mu.Unlock()
		return fmt.Errorf("至少勾选 1 个文件")
	}
	ctx, cancel := context.WithCancel(context.Background())
	st.cancel = cancel
	st.done = make(chan struct{})
	st.job.FSIDs = append([]string(nil), fsids...)
	st.job.Phase = PhaseChecking
	o.mu.Unlock()

	go o.run(ctx, st)
	return nil
}

// finishLocked 首次置 finished=true 并 close(done)(B2: 唯一终态收口, 调用方持 o.mu)。
func (o *orch) finishLocked(st *jobState) {
	if st.finished {
		return
	}
	st.finished = true
	close(st.done)
}

// run 主状态机(独立协程; 全部终态路径都保证回删临时目录)。
// B2: 唯一的 close(done)/置 finished 出口在本函数各 return 前(经 finishLocked)。
func (o *orch) run(ctx context.Context, st *jobState) {
	defer func() {
		o.mu.Lock()
		o.finishLocked(st)
		o.mu.Unlock()
	}()
	fail := func(code, msg string) {
		o.mu.Lock()
		st.job.Phase = PhaseFailed
		st.job.ErrCode = code
		st.job.Message = msg
		o.mu.Unlock()
	}
	// checking 相位已由 StartDownload 在锁内置定。
	need := int64(0)
	o.mu.Lock()
	for _, it := range st.items {
		for _, fid := range st.job.FSIDs {
			if it.FsID == fid {
				need += it.Size
			}
		}
	}
	o.mu.Unlock()
	free, err := o.deps.FreeBytesFn(ctx)
	if err != nil {
		fail("QUOTA_CHECK_FAILED", "配额查询失败: "+err.Error())
		return
	}
	if free < need {
		// 未转存即失败: 网盘无 .tmp- 残留(FR-40 异常 1 前半)。
		fail("QUOTA_EXCEEDED", "网盘剩余空间不足, 无法转存分享内容")
		return
	}
	// ── transferring: 转存到 /来自分享/.tmp-<shareid> ──
	o.mu.Lock()
	st.job.Phase = PhaseTransferring
	link, passcode, fsids := st.job.Link, st.job.Passcode, append([]string(nil), st.job.FSIDs...)
	o.mu.Unlock()
	target, files, err := o.deps.TransferFn(ctx, link, passcode, fsids)
	if err != nil {
		// B3: 转存失败(含部分成功)也要回删 .tmp- 目录, 防止残留孤儿
		// (目录可能已建且已有部分文件落地; RemoveFn 幂等, 目录未建时静默)。
		if target != "" {
			_ = o.deps.RemoveFn(context.Background(), []string{target})
		}
		fail("TRANSFER_FAILED", "转存失败: "+err.Error())
		return
	}
	o.mu.Lock()
	st.target = target
	st.files = files
	// B5: 转存产物 path/filename 回填作业, GET /api/share/dl 响应据此回传前端。
	st.job.Files = make([]FileItem, 0, len(files))
	for _, f := range files {
		st.job.Files = append(st.job.Files, FileItem{Path: f.Path, Filename: f.Filename})
	}
	st.job.Phase = PhaseDownloading
	o.mu.Unlock()
	// ── downloading: 逐文件流式下载(串行, 单连接语义)──
	var dlErr error
	for _, f := range files {
		if ctx.Err() != nil {
			dlErr = ctx.Err()
			break
		}
		if err := o.deps.DownloadFn(ctx, f.Path); err != nil {
			dlErr = err
			break
		}
	}
	// ── cleaning: 全部终态(含失败/取消)→ 延迟 → 回删临时目录(FR-40)──
	o.mu.Lock()
	st.job.Phase = PhaseCleaning
	target = st.target
	o.mu.Unlock()
	delay := o.deps.DeleteDelay
	if dlErr != nil || ctx.Err() != nil {
		delay = 0 // 失败/取消: 立即回删, 不留孤儿
	}
	timer := time.NewTimer(delay)
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
	timer.Stop()
	_ = o.deps.RemoveFn(context.Background(), []string{target})
	if dlErr != nil {
		if ctx.Err() != nil {
			fail("CANCELLED", "已取消, 临时目录已删除")
			return
		}
		fail("DOWNLOAD_FAILED", "下载失败: "+dlErr.Error())
		return
	}
	o.mu.Lock()
	st.job.Phase = PhaseDone
	o.mu.Unlock()
}

// Cancel 实现 Orchestrator: 协作取消(running 下载经 ctx 感知, cleaning 照常删目录)。幂等。
func (o *orch) Cancel(jobID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	st, ok := o.jobs[jobID]
	if !ok {
		return fmt.Errorf("作业不存在: %s", jobID)
	}
	if st.cancel != nil && !st.finished {
		st.cancel()
	}
	return nil
}
// List 实现 Orchestrator。
func (o *orch) List() []Job {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]Job, 0, len(o.order))
	for _, id := range o.order {
		if st, ok := o.jobs[id]; ok {
			out = append(out, st.job)
		}
	}
	return out
}

// Get 单作业查询(测试/API 用)。
func (o *orch) Get(jobID string) (Job, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if st, ok := o.jobs[jobID]; ok {
		return st.job, true
	}
	return Job{}, false
}

// PreviewItems 勾选候选清单(Preview 后有效; API 层回显用, 非接口方法)。
func (o *orch) PreviewItems(jobID string) []Item {
	o.mu.Lock()
	defer o.mu.Unlock()
	if st, ok := o.jobs[jobID]; ok {
		return append([]Item(nil), st.items...)
	}
	return nil
}

// HasFilePath 报告 path 是否位于作业的下载清单(B5: /api/share/dl/url 鉴别用)。
func (o *orch) HasFilePath(jobID, pcspath string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	st, ok := o.jobs[jobID]
	if !ok {
		return false
	}
	for _, f := range st.files {
		if f.Path == pcspath {
			return true
		}
	}
	// 转存未完成时以预览清单+目标目录推断(路径不可伪造: 前缀须为 .tmp-<shareid>)
	if st.target == "" {
		return false
	}
	return strings.HasPrefix(pcspath, st.target+"/")
}

// track 登记作业(preview 即入表, 前端可展示)。
func (o *orch) track(job *Job, items []Item) {
	o.mu.Lock()
	st := &jobState{job: *job, items: items}
	o.jobs[job.ID] = st
	o.order = append(o.order, job.ID)
	o.mu.Unlock()
}

// newJobID 随机作业 ID。
func newJobID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("s%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
