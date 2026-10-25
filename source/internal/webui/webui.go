// Package webui 前端静态资源(D7: SPA + go:embed 内嵌, 无构建步骤)。
// 前端选型结论: 纯 HTML + 原生 ES Module JS + petite-vue(约 6KB, 无构建 CDN 内嵌本地副本)。
// 理由见设计文档 §7。
package webui

import "embed"

// Files 内嵌前端产物(dist/ 由纯静态文件构成, 无编译)。
//
//go:embed all:dist
var Files embed.FS
