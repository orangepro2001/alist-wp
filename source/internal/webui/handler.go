package webui

import (
	"bytes"
	"net/http"
	"strings"
)

// handler.go W4: embed 静态资源 HTTP 挂载。
// 约定(§11 W4): 非遗留路径 → dist/index.html(SPA hash 路由单入口);
// /assets/* → 静态文件(app.js/pv.js/style.css, go:embed all:dist)。
// 好处: app.js 以 ES Module 加载, Content-Type 由 mime 按扩展名给出(.js→text/javascript),
// 同源无 CORS 问题; /assets 前缀天然与 /api/*、顶栏路由无冲突。

// Handler 返回前端静态资源处理器(index.html 回落 + /assets 静态文件)。
func Handler() http.Handler { return HandlerWithBase("") }

// HandlerWithBase 同 Handler, 但向 index.html 注入 <base href="{base}/">,
// 使前端相对路径(api/...、assets/...)落在 base 前缀下; base="" 即根路径。
func HandlerWithBase(base string) http.Handler {
	sub, err := fsSub()
	if err != nil {
		panic(err) // embed 内容编译期已固定, 只可能编程错误
	}
	fileServer := http.StripPrefix("/assets/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA 单入口: 其余全部路径回落 index.html(hash 路由由前端处理)
		data, err := fsIndex()
		if err != nil {
			http.Error(w, "前端资源缺失", http.StatusInternalServerError)
			return
		}
		data = bytes.Replace(data, []byte("<head>"), []byte(`<head><base href="`+base+`/">`), 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache") // index.html 不缓存, 静态资源由 embed 不可变
		_, _ = w.Write(data)
	})
}
