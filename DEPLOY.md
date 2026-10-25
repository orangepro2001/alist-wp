# 部署

目标形态: OpenList 作为唯一对外入口(tailscale `/files`), baidupcs-web 只在回环地址上给它提供 WebDAV。
所有服务只经 tailscale https 域名对外, 不另开路径/端口。

## 1. 运行 baidupcs-web

```bash
cd source && bash scripts/build.sh
install -m 755 bin/baidupcs-web-linux-amd64 ~/.local/bin/baidupcs-web
baidupcs-web            # 默认 127.0.0.1:8080; 数据在 ~/.local/share/baidupcs-web
```

常驻可用 systemd 用户服务(`ExecStart=%h/.local/bin/baidupcs-web`, `Restart=on-failure`), 保持监听回环。

## 2. 运行 OpenList(Docker, host 网络, 仅回环)

`--network host` 让容器能访问本机 `127.0.0.1:8080` 的 baidupcs-web, 同时 OpenList 自己只绑 `127.0.0.1:5244`, 不发布任何宿主机端口。
先预置配置(首次启动前), 再起容器(镜像按 digest 固定, v4.2.9):

```bash
mkdir -p ~/.local/share/openlist && cat > ~/.local/share/openlist/config.json <<'JSON'
{ "site_url": "https://<本机>.ts.net/files",
  "scheme": { "address": "127.0.0.1", "http_port": 5244, "https_port": -1, "force_https": false } }
JSON
docker run -d --name openlist --restart unless-stopped --network host --user "$(id -u):$(id -g)" \
  -v ~/.local/share/openlist:/opt/openlist/data -e UMASK=022 -e TZ=Asia/Shanghai \
  -e OPENLIST_ADMIN_PASSWORD='<初始管理员密码>' \
  openlistteam/openlist@sha256:897563beb674e7ad04edff14b0037dc8443ca3978570614005519df7eae06434
# 管理员已建好后可去掉 OPENLIST_ADMIN_PASSWORD 重建容器(密码在数据库里)
```

`site_url` 带子路径 `/files`, OpenList 的所有路由与静态资源都会挂在该前缀下。
(注意: 不带 `OPENLIST_ADMIN_PASSWORD` 的首次启动在 v4.2.9 里没有创建 admin 用户, 日志报 `failed get admin user`。)

登录 `https://<本机>.ts.net/files` → 管理 → 存储 → 添加 **WebDAV**: 地址 `http://127.0.0.1:8080/dav`,
用户名随意(如 `baidu`), **密码栏填百度整串 Cookie**(每个存储一个账号), 挂载路径如 `/百度网盘`, WebDAV 策略**本地代理**(`native_proxy`, 默认)。
"绑定百度账号"弹窗入口: `bash deploy/openlist/install.sh`(把 `bind-baidu.js` 写进 OpenList 的 `customize_body`, 需要 admin 密码, 取自 `~/.local/share/openlist/admin-credentials.txt` 或环境变量 `OPENLIST_ADMIN_PASSWORD`)。
以后加 Google Drive: 直接添加 GoogleDrive 存储即可。

## 3. 暴露到 tailnet

```bash
tailscale serve --bg --set-path /files http://127.0.0.1:5244/files
tailscale serve status        # 只应新增 /files, 不动其他路径
```

(Docker 部署留待后续: 容器内监听 `0.0.0.0:8080`(`BPW_LISTEN`), 宿主机至多发布到 `127.0.0.1`, 或仅容器网络内可见。)
