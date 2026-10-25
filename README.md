# baidupcs-web

把百度网盘暴露成 **WebDAV** 的单二进制服务, 供 [OpenList](https://github.com/OpenListTeam/OpenList)(Alist 的社区 fork)/Alist 用 WebDAV 驱动挂载。
OpenList 负责统一的网页前端(以后还能并排挂 Google Drive 等), 本项目负责百度网盘这一侧:
基于 [BaiduPCS-Go](https://github.com/qjfoidnh/BaiduPCS-Go)(v4.0.2)的 Cookie/BDUSS 登录、秒传+分片上传、带 `netdisk` UA 的流式下载代理。
上游库仅被 `source/internal/xengine` import, 升级隔离。

```
浏览器 → tailscale serve https://<本机>.ts.net/files → OpenList(127.0.0.1:5244)
OpenList 存储: [WebDAV 驱动·本地代理] → http://127.0.0.1:8080/dav   ← 本服务(仅回环)
               [GoogleDrive 驱动]     → OpenList 原生(以后)
账号凭证:      OpenList 存储编辑页的"密码"栏 = 百度 Cookie
```

**对外入口只有 tailscale 的 `/files`**。本服务只监听回环地址、没有应用层鉴权也没有 Web 页面, 访问控制完全交给 tailnet。

## 构建与运行

依赖 Go ≥ 1.23, 无 Node/npm。

```bash
cd source
bash scripts/build.sh              # 产出 source/bin/baidupcs-web-linux-{amd64,arm64}(静态, CGO_ENABLED=0)
bash scripts/dev.sh                # 开发模式: go run, 127.0.0.1:8080, DEBUG 日志
bin/baidupcs-web-linux-amd64       # 正式运行, 默认 -listen 127.0.0.1:8080
```

选项: `-listen`(默认 `127.0.0.1:8080`, 环境变量 `BPW_LISTEN`)、`-data`(默认 `~/.local/share/baidupcs-web`,
存 config.json/accounts.json/tasks.json, 0600)、`-tmp`(上传临时目录, 默认 `~/.cache/baidupcs-web/tmp`, 水位 2GB)、
`-log-level`(`BPW_LOG_LEVEL`)、`-version`。端口被占会报错退出, 不静默换端口。
`BPW_FORCE_V4=1` 强制 IPv4 直连(启动时已会自动探测 IPv6 不通并回落)。

## 绑定百度账号(在 OpenList 里填 Cookie)

WebDAV 要求 HTTP Basic 认证: **用户名随意(当标签), 密码 = 百度整串 Cookie**。OpenList 里每个 WebDAV 存储就是一个百度账号,
所以"填 Cookie 的入口"就是 OpenList 的存储编辑页: 管理 → 存储 → `百度网盘` → 编辑 → **密码**栏粘贴 Cookie → 保存。
想接第二个百度账号, 再添加一个 WebDAV 存储(地址相同, 密码填另一份 Cookie)。服务端按 Cookie 里的 BDUSS 自动登记账号(存 `accounts.json`, 0600), 无需先在服务端注册。

获取 Cookie: 电脑 Chrome/Edge 登录 [pan.baidu.com](https://pan.baidu.com) → `F12` → Application → Cookies → `https://pan.baidu.com`
复制整串, 或在 Network 里任一 `pan.baidu.com` 请求头复制 `Cookie:`。接口/日志均不输出凭证。
列目录报"登录凭证已失效" = Cookie 复制不完整或已过期, 重新复制并保存即可; 密码栏为空则 401。

命令行(可选, 仅用于查看/清理 `accounts.json` 里的账号, 与 WebDAV 选哪个账号无关):
`baidupcs-web account list | add -cookie - | rm <uid>`(`switch` 只影响没有 Basic 认证的路径, 即测试桩)。

## WebDAV 行为

| 操作 | 实现 |
|---|---|
| PROPFIND | 列目录(>1000 对象分页由上游处理), 10 秒目录缓存; 不读文件内容猜类型 |
| GET / HEAD | 服务端流式代理百度直链, 不落盘, Range 透传, 断流自动续取 |
| PUT | 请求体落临时文件 → 秒传优先, 否则分片上传 → **等百度侧完成才返回**; 同名一律覆盖 |
| MKCOL / DELETE / MOVE | 对应网盘操作 |
| COPY | 服务端复制, 仅支持同名(百度 cp 不能同时改名, 否则 501) |

在 OpenList 中添加存储 → **WebDAV**: 地址 `http://127.0.0.1:8080/dav`, 无需账号密码, WebDAV 策略选**本地代理**
(百度直链必须带 `netdisk` UA, 不要用 302)。

## 已知限制

- 非 SVIP 账号下载并发 = 1(`max_parallel` 默认 1, 调大有限速/封号风险); 传输参数改 `config.json` 后重启生效
- 大文件 PUT 期间 HTTP 请求会一直挂到上传完成, 注意 OpenList/反代超时
- 上传断点续传不支持(上游 v4.0.0 起移除), 中断需重传; 秒传成功率不作承诺
- 仅支持 Linux(amd64/arm64)

## 测试

```bash
cd source
go build ./... && go vet ./... && go test -race ./...
bash scripts/smoke_all.sh      # 桩引擎端到端, 全部通过输出 SMOKE_ALL_OK
```

## 致谢

[BaiduPCS-Go (qjfoidnh)](https://github.com/qjfoidnh/BaiduPCS-Go) 提供核心能力(Apache-2.0, 见 `LICENSE-BaiduPCS-Go.txt`)。
`docs/` 下为早期(自带 Web 前端阶段)的需求/设计记录, 仅供历史参考。
