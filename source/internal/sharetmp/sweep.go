// sweep.go W8: FR-40 异常分支 ③ — 服务重启孤儿清理(FR-17 同一机制)。
// 启动时枚举网盘 /来自分享/ 下全部 .tmp- 前缀目录并删除(服务崩溃残留)。
// SweepPanTmp 在 server 装配期调用一次; ListFn/RemoveFn 注入便于桩测。

package sharetmp

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SweepDeps 孤儿扫描依赖。
type SweepDeps struct {
	// ListTmpDirs 枚举现存 .tmp- 前缀网盘目录(生产: Engine.List("/来自分享") 过滤)。
	ListTmpDirs func(ctx context.Context) ([]string, error)
	// RemoveDirs 删除网盘目录(生产: Engine.Remove)。
	RemoveDirs func(ctx context.Context, paths []string) error
	// Log 扫描日志钩子(可 nil)。
	Log func(format string, args ...interface{})
}

// SweepPanTmp 启动孤儿清理: 全删 .tmp-* 目录(FR-40 异常 ③: 重启后 5min 内清掉,
// 本实现为启动即扫一次, 满足"5 分钟内"上限)。返回删除的目录数。
func SweepPanTmp(ctx context.Context, deps SweepDeps) (int, error) {
	dirs, err := deps.ListTmpDirs(ctx)
	if err != nil {
		return 0, fmt.Errorf("枚举临时目录失败: %w", err)
	}
	victims := make([]string, 0, len(dirs))
	for _, d := range dirs {
		base := d
		if i := strings.LastIndexByte(base, '/'); i >= 0 {
			base = base[i+1:]
		}
		if strings.HasPrefix(base, ".tmp-") {
			victims = append(victims, d)
		}
	}
	if len(victims) == 0 {
		return 0, nil
	}
	if err := deps.RemoveDirs(ctx, victims); err != nil {
		return 0, fmt.Errorf("回删孤儿目录失败: %w", err)
	}
	if deps.Log != nil {
		deps.Log("FR-40 孤儿清理: 已删除 %d 个 .tmp- 残留目录", len(victims))
	}
	return len(victims), nil
}

// OrphanGrace 重启孤儿清理宽限上限(需求验收 ⑤: 5min 内)。仅作文档常量, 启动即扫。
const OrphanGrace = 5 * time.Minute
