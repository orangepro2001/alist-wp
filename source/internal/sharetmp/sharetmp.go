// Package sharetmp FR-40 分享直连下载编排器: 预览 → 临时转存 → 流式下载 → 终态即删。
// 独立于 FR-28 手动转存; 状态机见设计文档 §6.4。
package sharetmp

// Phase FR-40 状态机状态。
type Phase string

const (
	PhaseParsing    Phase = "parsing"     // 解析链接+提取码
	PhasePreviewing Phase = "previewing"  // 访问分享页/列出内容
	PhaseChecking   Phase = "checking"    // 配额预检(FR-40 异常分支 1)
	PhaseTransferring Phase = "transferring" // 转存到 /来自分享/.tmp-<shareid>
	PhaseDownloading  Phase = "downloading"  // 逐文件走下载代理
	PhaseCleaning     Phase = "cleaning"     // 等待终态 + 10s + 递归删除临时目录
	PhaseDone         Phase = "done"
	PhaseFailed       Phase = "failed"      // 已回删残留, 不留孤儿
)

// FileItem FR-40 转存产物条目(B5: API 响应回传 path/filename, 前端签名直下用)。
type FileItem struct {
	Path     string `json:"path"`
	Filename string `json:"filename"`
}

// Job 一次 FR-40 直连下载作业。
type Job struct {
	ID       string `json:"id"`
	ShareID  string `json:"share_id"`
	Link     string `json:"link"`
	Passcode string `json:"-"` // 不序列化(FR-38 脱敏原则的延伸)
	Phase    Phase  `json:"phase"`
	FSIDs    []string `json:"fs_ids"` // 用户勾选的文件
	// Files 转存后的下载清单(B5 新增字段: downloading 相位起有效, 前端经
	// GET /api/share/dl 取 path 逐个 /api/share/dl/url 签名直下)。
	Files   []FileItem `json:"files,omitempty"`
	ErrCode string     `json:"err_code,omitempty"`
	Message string     `json:"message,omitempty"`
}

// Orchestrator FR-40 编排器。线程安全; Cancel 幂等。
type Orchestrator interface {
	// Preview 解析并浏览分享内容(不创建任何网盘目录; 提取码错误/链接失效/禁止转存在此暴露)。
	Preview(link, passcode string) (*Job, error)
	// StartDownload 对勾选 fsids 执行: 配额预检→转存→逐文件入下载代理→全部终态后 10s 删目录。
	// 返回作业 ID, 下载进度经 xfer.Events 以 kind=share_tmp 推送。
	StartDownload(jobID string, fsids []string) error
	// Cancel 取消作业: running 中的下载逐个取消, 之后仍走 cleaning(FR-40: 终态后删目录)。
	Cancel(jobID string) error
	// List 列出活跃作业(前端展示短暂占用配额提示用)。
	List() []Job
}
