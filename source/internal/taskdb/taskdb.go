// Package taskdb 传输任务持久化(FR-18/20)。
// 存储选型: JSON 文件(<DataDir>/tasks.json)而非 SQLite——任务量级小(数千条内)、
// 免 CGO(NFR-05 静态编译)、读写频率低(终态写盘, 进度仅内存); 权衡: 无并发事务,
// 以进程内互斥锁串行化落盘。
package taskdb

// Kind 任务种类。
type Kind string

const (
	KindUpload   Kind = "upload"
	KindDownload Kind = "download"
	KindShareTmp Kind = "share_tmp" // FR-40 临时转存下载, 终态联动删临时目录
)

// State 任务状态机状态(见设计文档 §6)。
type State string

const (
	StateQueued  State = "queued"
	StateRunning State = "running"
	StateDone    State = "done"
	StateFailed  State = "failed"
	StateCancel  State = "cancelled"
)

// Record 任务记录(终态持久化; 进度字段仅运行期, 不落盘)。
type Record struct {
	ID        string `json:"id"`
	Kind      Kind   `json:"kind"`
	State     State  `json:"state"`
	Account   uint64 `json:"account_uid"` // 归属百度账号(FR-03 任务不随切换中断)
	Name      string `json:"name"`        // 展示名(文件名)
	SrcPath   string `json:"src_path"`    // 上传: 本地临时路径 / 下载: 网盘路径
	DstPath   string `json:"dst_path"`    // 上传: 网盘路径 / 下载: 无
	TotalSize int64  `json:"total_size"`
	Error     string `json:"error,omitempty"`   // 终态失败原因(中文)
	ShareID   string `json:"share_id,omitempty"` // Kind=share_tmp 时的临时目录关联键
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// DB 任务库接口。线程安全; 全部方法可并发调用; 不适合在信号处理函数调用。
type DB interface {
	List() []Record
	Get(id string) (Record, bool)
	Upsert(r Record) error // 终态/状态跃迁时调用; 内部串行写盘
	Delete(id string) error
	// CountActiveByAccount 返回该账号 queued+running 数(FR-04 退出账号拦截用)。
	CountActiveByAccount(uid uint64) int
}
