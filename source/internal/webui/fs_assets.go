package webui

import "io/fs"

// fs_assets.go W4: embed FS 辅助(剥离 dist/ 前缀与读取 index.html)。

// fsSub 返回剥离 dist/ 前缀后的静态资源 FS(供 http.FS 直接服务 /assets/*)。
func fsSub() (fs.FS, error) {
	return fs.Sub(Files, "dist")
}

// fsIndex 读取内嵌的 dist/index.html。
func fsIndex() ([]byte, error) {
	return fs.ReadFile(Files, "dist/index.html")
}
