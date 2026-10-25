// Package session 下载直链签名(FR-39)。
// 原 D1 口令+会话 Cookie 机制已移除: 服务仅监听回环地址, 由 tailscale serve 暴露到 tailnet。
// SignToken/VerifyToken 见 impl.go。
package session
