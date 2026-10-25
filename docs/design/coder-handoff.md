# Coder 交接提示词(由 orchestrator 原样下发)

> 背景: 本设计会话无 Task 工具,本文件即"应下发给 subagent:coder 的完整 prompt"。

## Prompt(复制以下全部内容给 @coder)

你在 /home/time/doc/ai/make-tools 实现 baidupcs-web(Go 1.27.1 位于 /usr/local/go/bin/go)。
设计文档: docs/design/baidupcs-web.md(必读,尤其 §0 上游调研、§11 波次表)。
接口契约已落盘(internal/xengine、xfer、uploader、sharetmp、tempmgr、taskdb、session、config、server),
实现体只允许填充 TODO(Wn) 标注处与新增 *_impl.go 文件;禁止修改接口签名;禁止在 xengine 之外
import github.com/qjfoidnh/*。环境无 Node/npm,前端只用纯静态文件(ES Module + petite-vue 本地内嵌)。

严格按 docs/design/baidupcs-web.md §11 波次表顺序执行 W1→W9,每波完成必须跑该波"验收"列命令,
全部退出码 0 才允许进入下一波;验收失败先修复再重跑,不得跳过。全局回归命令:

    cd /home/time/doc/ai/make-tools && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./... && /usr/local/go/bin/go test ./...

依赖拉取失败时先执行 `go env -w GOPROXY=https://goproxy.cn,direct` 再重试(设计文档 §10)。
关键上游事实(勿凭记忆改写): 核心包在仓库根 baidupcs/(非 internal/),pcsconfig 不可 import,
分享转存是 8 步编排(设计文档 §0.3 转存行),下载直链必须 NetdiskUA,错误统一 pcserror.Error。

## 给 orchestrator 的验收提示

W1 骨架已由设计者落盘并静态自查(全部 import 有引用、无未定义符号、go.mod 声明 go 1.23 零依赖)。
因设计会话无 shell,请在下发 W1 前先代跑一次:

    cd /home/time/doc/ai/make-tools && /usr/local/go/bin/go build ./... ; echo "exit=$?"

期望 exit=0。若失败,把完整报错回传设计者修订契约(预期为空操作)。
