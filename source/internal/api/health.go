// Package api REST handler 层(§8): 按域分文件, 由 internal/server 装配;
// server 包只做路由与中间件装配, 响应构造与业务都在本包。
package api

import (
	"encoding/json"
	"net/http"
)

// HealthResponse GET /api/health 响应体(FR-36 状态页最小子集, W9 扩展时长/临时目录占用)。
type HealthResponse struct {
	Version string `json:"version"`
	Status  string `json:"status"`
}

// NewHealth 构造 GET /api/health 处理器: 返回 {"version":"<v>","status":"ok"}。
// version 本波传常量 "dev", W9 经 -ldflags 注入。
func NewHealth(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(HealthResponse{Version: version, Status: "ok"})
	}
}
