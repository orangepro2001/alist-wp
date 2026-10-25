package api

// dlurl.go W5: GET /api/dl/url?path → 生成带签名 token 的下载 URL(FR-12/FR-39)。
// 前端 <a href> 经此换签名直链, 浏览器原生下载; token 30min 有效。

import (
	"net/http"
	"strings"
)

// dlURLAPI 签名 URL 生成 handler。
type dlURLAPI struct {
	resolver interface {
		ResolveForURL(pcspath string) (string, error)
	}
}

// NewDLURL 构造 /api/dl/url handler。
func NewDLURL(resolver interface {
	ResolveForURL(pcspath string) (string, error)
}) *dlURLAPI {
	return &dlURLAPI{resolver: resolver}
}

// HandleDLURL GET /api/dl/url?path=/网盘/路径。
func (d *dlURLAPI) HandleDLURL(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimSpace(r.URL.Query().Get("path"))
	if p == "" || !strings.HasPrefix(p, "/") {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "缺少 path 参数")
		return
	}
	u, err := d.resolver.ResolveForURL(p)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "生成下载链接失败: "+err.Error())
		return
	}
	writeOK(w, map[string]string{"url": u})
}

// shareDLURLAPI FR-40 浏览器直下 handler(GET /api/share/dl/url?job_id=&path=)。
// 复用 /api/dl/url 的 resolver 签名模式(xfer ResolveForURL), B5: 前端在作业
// downloading 相位对每个文件签发签名直链, 浏览器 <a> 直下拿到真实文件内容。
type shareDLURLAPI struct {
	resolver interface {
		ResolveForURL(pcspath string) (string, error)
	}
}

// NewShareDLURL 构造 /api/share/dl/url handler。
func NewShareDLURL(resolver interface {
	ResolveForURL(pcspath string) (string, error)
}) *shareDLURLAPI {
	return &shareDLURLAPI{resolver: resolver}
}

// HandleShareDLURL GET /api/share/dl/url?job_id=&path=/来自分享/.tmp-xxx/文件。
// job_id 必填(与作业绑定, 防任意路径探测); path 须位于该作业转存临时目录内。
func (d *shareDLURLAPI) HandleShareDLURL(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimSpace(r.URL.Query().Get("job_id"))
	p := strings.TrimSpace(r.URL.Query().Get("path"))
	if jobID == "" {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "缺少 job_id 参数")
		return
	}
	if p == "" || !strings.HasPrefix(p, "/") {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "缺少 path 参数")
		return
	}
	type pathChecker interface{ HasFilePath(jobID, path string) bool }
	if c, ok := d.resolver.(pathChecker); ok && !c.HasFilePath(jobID, p) {
		writeErr(w, http.StatusNotFound, "INTERNAL", "文件不在该作业下载清单中")
		return
	}
	u, err := d.resolver.ResolveForURL(p)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "生成下载链接失败: "+err.Error())
		return
	}
	writeOK(w, map[string]string{"url": u})
}
