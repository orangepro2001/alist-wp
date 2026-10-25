// Package tempmgr 上传临时文件生命周期(FR-17)与 FR-40 网盘临时目录命名。
package tempmgr

import "time"

// State 磁盘水位检查结果(FR-17: 剩余 <2GB 拒绝新任务入队)。
type DiskState struct {
	FreeBytes  int64
	BelowLimit bool
}

// ErrBelowWatermark 磁盘水位不足错误(服务层转为中文提示并拒绝入队)。
type ErrBelowWatermark struct{ FreeBytes int64 }

func (e ErrBelowWatermark) Error() string { return "临时目录磁盘剩余空间不足" }

// Manager 临时文件管理器。线程安全。
type Manager interface {
	// Dir 返回临时目录绝对路径(D4)。
	Dir() string
	// NewTaskFile 为上传任务创建唯一临时文件(0600), 返回绝对路径。
	NewTaskFile(taskID, filename string) (string, error)
	// DeleteTaskFile 任务终态后调用; 实现须保证 10 秒内删除(FR-17), 幂等。
	DeleteTaskFile(taskID string) error
	// StartOrphanSweep 启动孤儿扫描: 每 5 分钟, 无任务引用且 mtime>24h 即删(FR-17);
	// 服务启动时额外扫描全部 .tmp- 前缀网盘临时目录的本地映射并清理(FR-40 异常分支)。
	// cancel 用于优雅退出; 返回值供测试等待扫描完成。
	StartOrphanSweep(referenced func(taskIDs map[string]struct{}), cancel <-chan struct{})
	// DiskState 读取水位。
	DiskState() (DiskState, error)
	// Usage 统计临时目录占用(FR-36 状态页)。
	Usage() (int64, error)
}

// PanTmpDirPrefix FR-40 网盘侧临时目录固定前缀。
const PanTmpDirPrefix = "/来自分享/.tmp-"

// PanTmpDir 生成 FR-40 网盘临时目录路径。
func PanTmpDir(shareID string) string { return PanTmpDirPrefix + shareID }

// OrphanMaxAge 孤儿判定阈值(24h)。
const OrphanMaxAge = 24 * time.Hour

// SweepInterval 孤儿扫描周期(5min)。
const SweepInterval = 5 * time.Minute

// DeleteDelay 任务终态到删除的延迟(10s, FR-17/FR-40)。
const DeleteDelay = 10 * time.Second
