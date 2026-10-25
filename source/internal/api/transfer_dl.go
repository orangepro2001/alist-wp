package api

// transfer_dl.go W5: GET /api/dl 流式下载代理入口(FR-11, §5.1)。
// path 参数 + 签名 token(xfer 内校验); 数据面全部委托 xfer.Proxy。
// 错误映射: 签名无效 403 / 参数缺失 400 / 上游失败 502(中文)。

import (
	"errors"
	"net/http"

	"baidupcs-web/internal/xfer"
)

// dlAPI 下载域 handler。
type dlAPI struct {
	p xfer.Proxy
}

// NewDL 构造 /api/dl handler(p 为 xfer.Proxy 实例)。
func NewDL(p xfer.Proxy) *dlAPI { return &dlAPI{p: p} }

// HandleDL GET /api/dl?path&exp&token。
// 签名 token 过期/无效 → 403 DLINK_EXPIRED(§4 机器码)。
func (d *dlAPI) HandleDL(w http.ResponseWriter, r *http.Request) {
	err := d.p.ServeDownload(w, r, "")
	if err == nil {
		return // 流式完成
	}
	if errors.Is(err, xfer.ErrBadToken) {
		writeErr(w, http.StatusForbidden, "DLINK_EXPIRED", "下载签名无效或已过期, 请刷新页面重试")
		return
	}
	var pe *xfer.ProxyError
	if errors.As(err, &pe) {
		writeErr(w, pe.Status, pe.Code, pe.Msg)
		return
	}
	// 浏览器断开/取消: 响应已提交或连接已断, 无需写头
	select {
	case <-r.Context().Done():
	default:
		writeErr(w, http.StatusBadGateway, "UPSTREAM_ERROR", "下载中断: "+err.Error())
	}
}
