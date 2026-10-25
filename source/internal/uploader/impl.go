// impl.go W6: Receiver 三段式实现(FR-15/16/17)。
// init(水位检查+建 0600 临时文件)→ chunk(顺序拼接 8MB 分片+流式 MD5)→
// complete(校验大小 → 交 xfer 队列, 上传 worker 先秒传后分片)。
// 浏览器断链: API 层经 r.Context() 感知后调 Abort, 清理半成品(FR-15 异常分支)。
// 引擎上传启动经函数字段注入(工程纪律: 便于桩测, 不覆写方法)。

package uploader

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"sync"
	"time"
)

// ChunkSize 建议分片大小(8MB, 任务书定稿)。
const ChunkSize int64 = 8 << 20

// ErrBelowWatermark 磁盘水位不足(FR-17), API 层转为 DISK_LOW 507。
type ErrBelowWatermark struct{ FreeBytes int64 }

func (e *ErrBelowWatermark) Error() string { return "临时目录磁盘剩余空间不足 2GB, 已拒绝上传" }

// UploadStarter complete 后启动引擎上传的钩子(xfer.Manager.EnqueueUpload 适配)。
// 返回引擎侧 taskID(与任务表/传输页一致); 临时文件由 tempmgr 终态延迟删除。
type UploadStarter func(ctx context.Context, tempPath, savePath, policy string, noRapid bool) (string, error)

// WatermarkChecker 磁盘水位检查钩子(tempmgr.DiskState 适配, FR-17)。
type WatermarkChecker func() (freeBytes int64, belowLimit bool, err error)

// TempManager Receiver 对 tempmgr 的最小依赖面(*tempmgr.Impl 满足即可)。
type TempManager interface {
	NewTaskFile(taskID, filename string) (string, error)
	DeleteTaskFile(taskID string) error
}

// Config Receiver 装配配置。
type Config struct {
	TempMgr   TempManager      // 临时文件创建/延迟删除(必填)
	StartUpload UploadStarter  // 引擎上传启动钩子(必填)
	Watermark WatermarkChecker // 水位检查(nil=不检查)
}

// sessionState 单个上传会话运行态(锁: 各会话独立 mu, 接收串行由 API 层保证)。
type sessionState struct {
	mu       sync.Mutex
	spec     Session
	tempPath string
	f        *os.File
	h        hash.Hash // 全量流式 MD5(FR-16)
	nextSeq  int       // 期望的下一分片序号
	written  int64     // 已收字节
	closed   bool      // complete/abort 后拒绝续写
}

// receiver Receiver 实现。
type receiver struct {
	cfg  Config
	mu   sync.Mutex
	sess map[string]*sessionState
}

// New 构造三段式 Receiver。
func New(cfg Config) (Receiver, error) {
	if cfg.TempMgr == nil || cfg.StartUpload == nil {
		return nil, fmt.Errorf("uploader: TempMgr 与 StartUpload 必填")
	}
	return &receiver{cfg: cfg, sess: make(map[string]*sessionState)}, nil
}

// Init 实现 Receiver: 水位检查 → 建临时文件(0600) → 登记会话(FR-17/D3/D4)。
func (r *receiver) Init(filename, relPath, targetDir string, size int64, policy string, noRapid bool) (*Session, error) {
	if filename == "" {
		return nil, fmt.Errorf("文件名不能为空")
	}
	if size < 0 {
		return nil, fmt.Errorf("非法文件大小: %d", size)
	}
	if r.cfg.Watermark != nil {
		free, below, err := r.cfg.Watermark()
		if err != nil {
			return nil, fmt.Errorf("磁盘水位检查失败: %w", err)
		}
		if below {
			return nil, &ErrBelowWatermark{FreeBytes: free}
		}
	}
	taskID := newSessionID()
	tempPath, err := r.cfg.TempMgr.NewTaskFile(taskID, filename)
	if err != nil {
		return nil, err
	}
	s := &sessionState{spec: Session{
		TaskID: taskID, ChunkSize: ChunkSize, TargetDir: targetDir,
		RelPath: relPath, Filename: filename, Size: size,
	}, tempPath: tempPath}
	r.mu.Lock()
	r.sess[taskID] = s
	r.mu.Unlock()
	out := s.spec
	return &out, nil
}

// Chunk 实现 Receiver: append 模式顺序写第 seq 片并同步流式算 MD5;
// 幂等: 重复序号(浏览器重传)视为已收, 返回当前字节数; 乱序报错等重传。
func (r *receiver) Chunk(sessionID string, seq int, body io.Reader) (int64, error) {
	r.mu.Lock()
	s, ok := r.sess[sessionID]
	r.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("上传会话不存在: %s", sessionID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, fmt.Errorf("上传会话已结束: %s", sessionID)
	}
	if seq < s.nextSeq {
		return s.written, nil
	}
	if seq != s.nextSeq {
		return s.written, fmt.Errorf("分片乱序: 期望第 %d 片, 实收第 %d 片", s.nextSeq, seq)
	}
	if s.f == nil {
		f, err := os.OpenFile(s.tempPath, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return s.written, fmt.Errorf("打开临时文件: %w", err)
		}
		s.f = f
		s.h = md5.New()
	}
	n, err := io.Copy(io.MultiWriter(s.f, s.h), body)
	s.written += n
	if err != nil {
		return s.written, fmt.Errorf("%w: 第 %d 片写入中断(已收 %d 字节)", ErrChunkMismatch, seq, n)
	}
	s.nextSeq++
	return s.written, nil
}

// Complete 实现 Receiver: 校验总大小 → 停写 → 交上传 worker → tempmgr 终态 10s 删。
func (r *receiver) Complete(sessionID string) (string, error) {
	r.mu.Lock()
	s, ok := r.sess[sessionID]
	if ok {
		delete(r.sess, sessionID)
	}
	r.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("上传会话不存在: %s", sessionID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", fmt.Errorf("上传会话已结束: %s", sessionID)
	}
	if s.spec.Size > 0 && s.written != s.spec.Size {
		s.closed = true
		r.dropTemp(s)
		return "", fmt.Errorf("文件大小不符: 期望 %d 实收 %d", s.spec.Size, s.written)
	}
	if s.f != nil {
		if err := s.f.Sync(); err != nil {
			s.closed = true
			return "", fmt.Errorf("刷盘失败: %w", err)
		}
		s.f.Close()
		s.f = nil
	}
	s.closed = true
	savePath := path.Join(s.spec.TargetDir, s.spec.Filename)
	// B9: rename 移到入队内部(引擎任务 ID 确定后、worker 读文件前):
	// StartUpload 前先改名 → worker 拿最终路径; 删除只由 xfer 终态 FinishHook 触发
	// (Complete 不再调 DeleteTaskFile, 排队任务文件不再被提前删)。
	taskID, err := r.cfg.StartUpload(context.Background(), s.tempPath, savePath, "", false)
	if err != nil {
		r.dropTemp(s) // 入队失败: 半成品无主, 立即清理(延迟删除兜底幂等)
		return "", err
	}
	if taskID == "" {
		taskID = s.spec.TaskID
	}
	return taskID, nil
}

// Abort 实现 Receiver: 浏览器断链/用户取消 → 删半成品并注销会话(FR-15 异常分支)。幂等。
func (r *receiver) Abort(sessionID string) error {
	r.mu.Lock()
	s, ok := r.sess[sessionID]
	if ok {
		delete(r.sess, sessionID)
	}
	r.mu.Unlock()
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.f != nil {
		s.f.Close()
		s.f = nil
	}
	r.dropTemp(s)
	return nil
}

// dropTemp 清理半成品临时文件(文件未建/已删均静默)。
func (r *receiver) dropTemp(s *sessionState) {
	if s.tempPath != "" {
		_ = os.Remove(s.tempPath)
	}
	_ = r.cfg.TempMgr.DeleteTaskFile(s.spec.TaskID)
}

// newSessionID 随机会话 ID。
func newSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("u%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
