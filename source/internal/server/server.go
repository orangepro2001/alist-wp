// Package server HTTP 服务装配: 路由、中间件、embed 前端挂载。
// 路由选型: Go 1.22+ 标准库 net/http 增强路由(方法+路径参数, 如 POST /api/tasks/{id}/cancel),
// 零第三方依赖、满足全部端点需求; 权衡: 无中间件生态, 自建 60 行中间件链(见 middleware.go)。
package server

import (
	"net/http"

	"baidupcs-web/internal/config"
	"baidupcs-web/internal/xengine"
)

// Options 服务装配选项。
type Options struct {
	Config  *config.Config // 运行配置(监听/目录/传输默认值)
	Version string         // 状态页(FR-36)展示
}

// engineOverride 测试/冒烟注入桩 Engine 用(nil=生产路径按配置构造真实引擎)。
// 纪律: 不经包级全局, 装配参数显式传递(server_files_test.go W4)。
var engineOverride xengine.Engine

// Server HTTP 服务。
type Server struct {
	httpSrv *http.Server
	mux     *http.ServeMux
	root    http.Handler // 中间件链包裹后的根 Handler
}

// New 装配全部路由与中间件; 不监听。
func New(opt Options) (*Server, error) { return newServer(opt) }

// NewWithEngine 装配并注入给定 Engine(桩测试/冒烟用); 生产用 New。
func NewWithEngine(opt Options, eng xengine.Engine) (*Server, error) {
	old := engineOverride
	engineOverride = eng
	defer func() { engineOverride = old }()
	return newServer(opt)
}

// ListenAndServe 监听并服务; 端口被占直接返回错误(NFR-06 不静默换端口)。
func (s *Server) ListenAndServe() error { return s.httpSrv.ListenAndServe() }

// Shutdown 优雅退出(供信号处理调用)。
func (s *Server) Shutdown() error {
	if s.httpSrv == nil {
		return nil
	}
	return s.httpSrv.Close()
}

// Handler 暴露根 Handler(含中间件链, 测试用)。
func (s *Server) Handler() http.Handler { return s.root }
