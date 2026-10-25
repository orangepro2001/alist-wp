# baidupcs-web

BaiduPCS-Go 的 Web 客户端: 单二进制、内嵌前端(go:embed)、零浏览器插件。
把 [github.com/qjfoidnh/BaiduPCS-Go](https://github.com/qjfoidnh/BaiduPCS-Go)(v4.0.2)的网盘能力
以浏览器形式提供: 文件管理、流式下载(不落盘)、分片上传(秒传优先)、分享/转存/直连下载、
离线下载、回收站、配置白名单与状态页。上游库仅被 `internal/xengine` 适配层 import, 升级隔离。

## 构建

依赖: Go ≥ 1.23(本机 1.27.1 已验证), 无 Node/npm(前端为纯静态文件, 已内嵌)。

```bash
bash scripts/build.sh
# 产出: bin/baidupcs-web-linux-amd64 与 bin/baidupcs-web-linux-arm64
# CGO_ENABLED=0 静态编译; -ldflags "-s -w -X main.version=<git describe 或日期>"
bin/baidupcs-web-linux-amd64 --version
```

## 运行

```bash
bin/baidupcs-web-linux-amd64 -listen 0.0.0.0:8080 [选项]
```

- `-listen`: 监听地址, 默认 `0.0.0.0:8080`(端口被占会报错退出, 不静默换端口)
- `-data`: 数据目录, 默认 `~/.local/share/baidupcs-web`(config.json / accounts.json / tasks.json / session.key, 全部 0600)
- `-tmp`: 上传临时目录, 默认 `~/.cache/baidupcs-web/tmp`(水位 2GB, 终态 10s 清理)
- `-password`: **口令初始化**(仅未设置口令时生效), 亦可经环境变量 `BPW_PASSWORD`; 未初始化时首次访问页面强制设置
- `-log-level`: 日志级别 `DEBUG/INFO/WARN/ERROR`(默认 INFO; 可用环境变量 `BPW_LOG_LEVEL`)

口令以 bcrypt 存储; 会话 Cookie(HttpOnly, SameSite=Lax)7 天有效, 重启后会话不失效;
连续 5 次口令错误锁定 5 分钟。

## 功能清单(需求 FR 对照)

| 功能 | 说明 | FR |
|---|---|---|
| 百度账号管理 | Cookie 或 BDUSS+STOKEN 添加/切换/删除; 凭证仅存服务器(0600), API/日志/前端均脱敏 | FR-01~04, FR-38 |
| 配额 | 总量/已用/剩余(60s 缓存) | FR-05 |
| 文件管理 | 列表(>1000 对象)/排序/分页/树/搜索/元信息; 新建/删除/复制/移动/改名 | FR-06~10, FR-21~24 |
| 流式下载 | 服务器代理直链, 不落盘; Range/断流续取; 目录=逐文件触发浏览器下载 | FR-11/12/14 |
| 秒传导出 | `/api/export` 导出秒传三元组(带"勿外传"警告) | FR-13 |
| 上传 | 三段式(8MB 分片)分片接收+秒传探测; skip/overwrite/rsync 策略; 磁盘水位保护 | FR-15/16/17 |
| 任务中心 | 队列/取消/重试/清除; SSE 实时进度(≤500ms) | FR-18~20 |
| 分享 | 创建/列表/取消; 链接+提取码转存 | FR-25~28 |
| 分享直连下载 | 粘贴链接→预览→临时转存→流式下载→终态 10s 删临时目录(短暂占用配额) | FR-40 |
| 离线下载 | 添加/列表/查询/取消/删除 | FR-29/30 |
| 回收站 | 列表/还原/删除/清空(二次确认) | FR-31~33 |
| 配置管理 | `/api/config` 白名单读写(限速/并发/临时目录/同名策略/user_agent); appid/pcs_ua 等风控敏感项**只读, 修改返回 403** | FR-34 |
| 通配符测试 | `/api/match`(只读调试, 与 CLI `match` 同集合) | FR-35 |
| 状态页 | `/api/status` 版本/上游基线/运行时长/临时目录占用/磁盘剩余 | FR-36 |
| 会话安全 | 口令登录/修改; 全部 `/api/*` 强制会话; 登录与上传接口每 IP 10 次/分钟限速(超限 429) | FR-37, FR-39 |
| 工具 | `/api/tool/cipher` 上游同款文件加解密(aes-\*-\{ctr,cfb,ofb\}, 设置页提供入口) | FR-35 |

## 已知限制

- **非 SVIP 账号下载并发 = 1**(决策 D6): `max_parallel` 默认 1, 调大有限速/封号风险
- **直链安全警告**: `/api/dl` 签名 URL 与秒传信息等价于凭证/文件本身, **勿外传**(30 分钟过期)
- **无转码播放**: 视频不支持在线播放(百度转码接口未接入), 请下载后本地播放
- 上传断点续传不支持(上游 v4.0.0 起官方接口变化已移除); 中断任务标记失败, 手动重传
- 秒传成功率不作承诺(上游声明官方已封禁部分秒传)
- 仅支持 Linux(amd64/arm64), 内核 ≥3.10; 移动端浏览器尽力而为

## 绑定百度账号(手把手)

1. 登录 Web 界面(自己设的访问口令)后, 点顶栏右上角 **“+ 账号”**;
2. 获取百度凭证(二选一):
   - **整串 Cookie(推荐, 最不易错)**: 电脑 Chrome/Edge 登录 [pan.baidu.com](https://pan.baidu.com) → 按 `F12` → 顶部 **应用/Application** 标签 → 左侧 **Cookie** → `https://pan.baidu.com` → 在任意 Cookie 行上右键或全选复制; 也可在 **网络/Network** 标签随便点一个 `pan.baidu.com` 请求 → 请求头里复制完整的 `Cookie:` 一整串;
   - **BDUSS + STOKEN**: 同样在 Cookie 列表里分别找名为 `BDUSS` 与 `STOKEN` 的行, 复制其值。注意 STOKEN 必须取 **pan.baidu.com 域**下的(浏览器里同名 Cookie 可能有多条, 值以 pan.baidu.com 域为准); STOKEN 只影响分享转存/秒传, 文件浏览与上传下载只依赖 BDUSS;
3. 粘贴进弹窗 → 点"绑定" → 顶栏出现账号名和配额条即为成功;
4. 换号/多号: 再点"+ 账号"添加, 顶栏下拉切换; 弹窗内凭证框是密码型输入, 不被旁人偷看。

> 凭证只保存在服务器本地 `accounts.json`(0600), 浏览器不保存; 绑定成功但接口报"登录凭证已失效"= 复制不完整或已过期, 重新复制即可。

## 智能网络自愈(IPv6)

部分环境"有 IPv6 地址但到百度实际不通"(路由/防火墙问题), 表现为列表加载不出、提示网络错误。
本程序启动加载账号时会自动探测(3 秒预算), 检测到该情况会**自动强制 IPv4 直连**并在日志提示;
也可显式指定环境变量 `BPW_FORCE_V4=1`。对 IPv6 正常的环境零影响。

## 常见问题(FAQ)

| 现象 | 原因与处理 |
|---|---|
| 登录页输入口令后又跳回登录页 | 旧版本问题, 已修复; 请用本包 v20261025+ |
| `-password` 传了但登录不上 | `-password` 只在**首次初始化口令**时生效; 已初始化时启动日志会提示被忽略。改口令请用设置页, 或停服后删除 `<data>/config.json` 重新初始化 |
| 提示"尚未登录任何百度账号" | 正常流程: 点顶栏"+ 账号"绑定百度凭证(见上节) |
| 文件列表加载不出/网络错误 | 多为 IPv6 环境问题, 程序会自愈; 若仍出现, 用 `BPW_FORCE_V4=1` 启动 |
| 连续输错口令后正确密码也 429 | 安全锁定 5 分钟, 等待或重启服务 |
| 上传成功但临时目录没马上清 | 终态 10 秒内删除; 服务重启后 5 分钟内孤儿扫描兜底 |
| 下载速度慢 | 受百度账号限速策略约束; 非 SVIP 保持 `max_parallel=1`, SVIP 可在设置页调大 |

## 冒烟与回归

```bash
bash scripts/smoke_all.sh   # health/登录/文件/配额/下载(Range 206)/上传 + 空载 RSS≤100MB + 双平台产物检查
                            # 全部通过输出 SMOKE_ALL_OK
/usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./... && /usr/local/go/bin/go test ./...
```

## 致谢

- [BaiduPCS-Go (qjfoidnh)](https://github.com/qjfoidnh/BaiduPCS-Go) — 本项目的核心能力来源, 上游以库形式 import(v4.0.2 锁定), 加解密/秒传/转存编排均为上游同款逻辑
- 感谢上游及 BaiduPCS 系列项目的持续维护
