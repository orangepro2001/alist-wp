# baidupcs-web 部署指南

> 面向拿到发布包的朋友: 5 分钟把"百度网盘 Web 客户端"跑在自己/家庭的 Linux 机器上,
> 局域网任何设备打开浏览器即可使用。工程思路与架构详见包内 `docs/design/baidupcs-web.md`。

## 1. 这是什么(工程思路一页纸)

- **定位**: 给 [BaiduPCS-Go](https://github.com/qjfoidnh/BaiduPCS-Go)(命令行网盘工具)做一个 Web 壳,
  程序跑在一台 Linux 机器上, 全家/全组用浏览器访问, 体验接近官方图形客户端。
- **架构核心(三条)**:
  1. **下载必须服务器中转**: 浏览器无法自定义 User-Agent, 而百度直链必须伪装 `netdisk` UA,
     所以下载由服务端做流式代理(Range 透传), **不落盘**、支持断点续传;
  2. **上传临时落盘**: 浏览器 → 服务器临时目录(8MB 分片 + 流式 MD5)→ 复用上游秒传/分片引擎 →
     **任务终态 10 秒内删除本地临时文件**, 配磁盘水位保护(剩 2GB 拒收)与孤儿扫描;
  3. **凭证与本地机器绑定**: 你的百度 BDUSS/STOKEN 只存在服务器(0600), 浏览器只拿访问口令会话;
     下载直链是 30 分钟签名 URL, 等价于文件本身, 勿外传。
- **交付形态**: 单二进制(linux-amd64 / linux-arm64), 前端已 `go:embed` 内嵌, 无外部依赖,
  `CGO_ENABLED=0` 静态编译, 拷过去就能跑。
- **上游隔离**: BaiduPCS-Go v4.0.2 以 Go 库形式 import, 全部适配收敛在 `internal/xengine`,
  上游升级只动一个包。

## 2. 部署(三种场景)

### 场景 A: 最快上手(本机已编译好)

```bash
tar xzf baidupcs-web-release-*.tar.gz
cd baidupcs-web-release-*
# 看看机器是哪种架构:
uname -m        # x86_64 → 用 amd64 产物; aarch64 → 用 arm64 产物
./bin/baidupcs-web-linux-amd64 -listen 0.0.0.0:8080 -password 你的访问口令
```

浏览器打开 `http://<这台机器的IP>:8080` → 输入访问口令 → 顶栏 **“+ 账号”** 绑定百度凭证
(获取教程见 README"绑定百度账号"节)→ 完成。

### 场景 B: 用 systemd 常驻(推荐家用服务器)

```bash
# 1. 放置二进制
sudo install -m 755 bin/baidupcs-web-linux-amd64 /usr/local/bin/baidupcs-web

# 2. 创建服务(把 YOUR_PASSWORD 换掉; 数据/缓存目录默认在用户家目录,
#    若用系统账户运行建议显式指定 -data 与 -tmp 到固定路径)
sudo tee /etc/systemd/system/baidupcs-web.service >/dev/null <<'UNIT'
[Unit]
Description=BaiduPCS Web Client
After=network-online.target
Wants=network-online.target

[Service]
User=YOUR_USER
Environment=BPW_PASSWORD=YOUR_PASSWORD
ExecStart=/usr/local/bin/baidupcs-web -listen 0.0.0.0:8080 \
  -data /var/lib/baidupcs-web -tmp /var/cache/baidupcs-web
Restart=on-failure
RestartSec=3
# 加固: 只留必要能力
NoNewPrivileges=true
ProtectHome=read-only
ProtectSystem=strict
ReadWritePaths=/var/lib/baidupcs-web /var/cache/baidupcs-web

[Install]
WantedBy=multi-user.target
UNIT

sudo mkdir -p /var/lib/baidupcs-web /var/cache/baidupcs-web
sudo chown YOUR_USER /var/lib/baidupcs-web /var/cache/baidupcs-web
sudo systemctl daemon-reload && sudo systemctl enable --now baidupcs-web
systemctl status baidupcs-web    # active(running) 即成功
```

> 注意: `BPW_PASSWORD` 只在**首次初始化口令**时生效; 之后改口令请在网页"设置"页操作。

### 场景 C: 局域网其他设备访问

- 直接访问 `http://<服务器IP>:8080`; 手机/平板浏览器可用(尽力而为);
- 想用域名或跨公网: 自备反向代理(Nginx/Caddy)+ HTTPS, 本程序只提供 HTTP(见已知限制);
- 防火墙放行: `sudo ufw allow 8080/tcp`(或对应防火墙工具)。

## 3. 使用方法(日常操作地图)

| 想做什么 | 去哪 | 说明 |
|---|---|---|
| 浏览/搜索/排序网盘文件 | 文件页 | 面包屑导航; 搜索为全盘递归 |
| 上传文件/文件夹 | 文件页拖拽或"上传"按钮 | 传输页看实时进度; 秒传命中瞬间完成 |
| 下载文件 | 文件页点下载 | 服务器流式中转, 不占服务器磁盘; 目录=逐文件下载 |
| 分享文件给别人 | 分享页 → 创建 | 自定义提取码/有效期; 列表可取消 |
| 别人分享给我 | 分享页 → 转存框 | 粘贴链接+提取码转存到自己网盘 |
| 分享内容直接下载(不存网盘) | 分享页 → 直连下载面板 | 粘贴链接→勾选→下载; 临时转存自动清理(短暂占配额) |
| 磁力/HTTP 离线下载 | 离线页 | 添加磁力链/URL, 任务列表管理 |
| 误删恢复 | 回收站页 | 还原/删除/清空(清空需二次确认) |
| 改访问口令 | 设置页(第一区块) | 旧口令+新口令 |
| 改并发/限速/临时目录 | 设置页 | 白名单项即时生效; 风控敏感项只读 |
| 导出秒传/直链 | API: `/api/export`, `/api/dl/url` | 等价于文件本身, 30 分钟过期, 勿外传 |

## 4. 升级与运维

```bash
# 升级: 停服务 → 换二进制 → 起服务(数据目录不动, 配置/凭证/任务全保留)
sudo systemctl restart baidupcs-web

# 数据一览(默认位置, systemd 场景按上面 unit 指定的路径):
#   config.json   访问口令哈希/会话密钥/传输参数(0600)
#   accounts.json 百度凭证(0600)
#   tasks.json    传输任务库
#   tmp/          上传临时区(自动清理)

# 忘记口令: 停服 → 删除 config.json → 带新 -password 启动(凭证在 accounts.json 不受影响)

# 日志: journalctl -u baidupcs-web -f   (systemd 场景) 或启动时 -log-level DEBUG
```

## 5. 安全须知(务必读)

1. **访问口令是唯一门槛**, 请设强口令; 服务暴露在不可信网络时务必上反向代理 + HTTPS;
2. 百度凭证(BDUSS)等价于账号本身: 服务器要看好, `accounts.json` 泄露 = 网盘被接管;
3. 下载直链/秒传串等于文件本身, 分享给别人的话注意 30 分钟过期只是缓和而非保护;
4. 第三方客户端本质是模拟官方客户端协议, 存在账号风控风险(尤其非 SVIP 调大并发), 请自行权衡;
5. 本项目仅供学习与个人效率用途, 请遵守当地法律与百度用户协议。

## 6. 从源码构建(可选)

```bash
# 依赖: Go ≥ 1.23; 无需 Node/npm
git clone <本项目> && cd <项目目录>
bash scripts/build.sh          # 产出 bin/ 双平台
go test ./...                  # 全量单测
bash scripts/smoke_all.sh      # 端到端冒烟(输出 SMOKE_ALL_OK)
```

源码目录速览:

```
cmd/baidupcs-web/      入口(flag 解析/ldflags 版本注入)
internal/xengine/      上游适配层(唯一 import BaiduPCS-Go 的地方; IPv4 自愈在此)
internal/xfer/         下载流式代理/任务队列/事件总线/限速
internal/uploader/     三段式上传接收(8MB 分片/流式 MD5)
internal/tempmgr/      临时文件生命周期(终态 10s 删/孤儿扫/水位)
internal/sharetmp/     FR-40 分享直连下载状态机
internal/api/          REST handlers(按域分文件)
internal/server/       路由/中间件(鉴权/限速/日志脱敏)
internal/webui/dist/   前端(原生 ES Module, 已内嵌)
docs/                  需求/设计/开发日志(含全部工程决策记录)
```

## 7. 致谢与许可

- 核心 能力来自 [BaiduPCS-Go (qjfoidnh)](https://github.com/qjfoidnh/BaiduPCS-Go) v4.0.2, 以库形式复用;
- 本项目自身代码随包附带, 沿用上游开源精神, 仅限学习与个人效率用途。
