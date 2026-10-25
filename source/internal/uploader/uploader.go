// Package uploader 浏览器分片接收(FR-15): init → chunk → complete 三段式。
// 分片默认 8MB; 顺序拼接进临时文件; 接收过程流式计算 MD5(秒传探测提前, FR-16)。
package uploader

import "io"

// Session 一次上传会话(init 返回)。
type Session struct {
	TaskID     string `json:"task_id"`      // 兼作任务 ID(入队即同一 ID)
	ChunkSize  int64  `json:"chunk_size"`   // 建议浏览器分片大小(8MB)
	Received   int64  `json:"received"`     // 断点续传(浏览器→服务器层)已收字节; 分片按序号幂等重传
	TargetDir  string `json:"target_dir"`   // 网盘目标目录
	RelPath    string `json:"rel_path"`     // 目录上传时的相对路径(webkitRelativePath)
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
}

// Receiver 分片接收器。线程安全约束: 同一 Session 的 chunk 需按序调用(浏览器端保证);
// 跨 Session 可并发。
type Receiver interface {
	// Init 建立会话并创建临时文件(D4 目录, 0600); 磁盘水位 <2GB 时拒绝(FR-17)。
	Init(filename, relPath, targetDir string, size int64, policy string, noRapid bool) (*Session, error)
	// Chunk 写入第 seq 片; body 由调用方提供 Reader(服务层已解析 multipart/raw body)。
	// 单片校验失败返回 ErrChunkMismatch, 浏览器仅重传该片(FR-15 异常分支)。
	Chunk(sessionID string, seq int, body io.Reader) (received int64, err error)
	// Complete 结束接收: 校验总大小/MD5 → 交 xfer.Manager.EnqueueUpload 引擎上传 →
	// 任务终态后 10 秒内删除临时文件(FR-17, 由 tempmgr 承担)。
	Complete(sessionID string) (taskID string, err error)
	// Abort 浏览器中断时调用: 丢弃半成品临时文件(FR-15 异常分支)。
	Abort(sessionID string) error
}

// ErrChunkMismatch 单片校验失败(实现阶段定义 sentinel)。
var ErrChunkMismatch = errChunkMismatch{}

type errChunkMismatch struct{}

func (errChunkMismatch) Error() string { return "分片校验失败" }
