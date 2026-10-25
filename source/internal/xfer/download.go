// download.go 流式下载代理核心接口(FR-11, 决策 D2)。
// 数据面: dlink(HMAC token 签名) → 本服务 → netdisk UA 请求百度 → io.Copy 流式回传浏览器。
// 本地不落盘; 固定环形缓冲 ≤32MB(NFR-04); 断流时对百度按 Range 重建连(对浏览器透明)。
package xfer

import (
	"context"
	"io"
	"net/http"
)

// Proxy 下载代理接口。
type Proxy interface {
	// ServeDownload 处理一次浏览器下载请求。
	// 入参 dlink 前必须经 session.VerifyToken 校验(FR-39); pcspath 用于续取 dlink 与日志。
	// 行为契约:
	//   - 透传标准 Range 头 → 响应 200/206 + Content-Length + Accept-Ranges: bytes
	//   - Content-Disposition 按 RFC 5987 编码中文文件名(FR-11 验收 ④)
	//   - 百度断流: 内部按已发字节数对百度重新 Range 请求续写, 浏览器连接不断
	//   - dlink 失效: 重解一次; 仍失败回 502 + 中文错误
	ServeDownload(w http.ResponseWriter, r *http.Request, pcspath string) error
	// ResolveForURL 生成带签名 token 的下载 URL(FR-39, 30min 有效)。
	ResolveForURL(pcspath string) (signedURL string, err error)
	// PumpLocal W8/FR-40: 服务器侧把单个网盘文件拉到终态(流式, 内容不落盘),
	// 供分享直连下载的下载环节驱动状态机; ctx 取消即中断。
	PumpLocal(ctx context.Context, pcspath string) error
}

// RateLimiter 全局限速接口(FR-14/FR-34; 0=不限速)。实现可复用 golang.org/x/time/rate 或自研令牌桶。
type RateLimiter interface {
	WaitN(n int64) error // 阻塞直至允许发送 n 字节; ctx 取消时返回错误
}

// 环形缓冲上限(NFR-04: 单任务下载临时增长 ≤32MB)。
const RingBufferSize = 32 << 20

// 确保包级导入不悬空(骨架期占位, W3 实现阶段移除)。
var _ io.Reader = nil

// NewRateLimiter 构造令牌桶限速器(rate B/s; ≤0 返回 nil 即不限速)。
func NewRateLimiter(rateBps int64) RateLimiter { return newRateLimiter(rateBps) }
