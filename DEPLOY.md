# 部署

目标形态: OpenList 作为唯一对外入口(tailscale `/files`), baidupcs-web 只在回环地址上给它提供 WebDAV。
所有服务只经 tailscale https 域名对外, 不另开路径/端口。

## 1. 运行 baidupcs-web

```bash
cd source && bash scripts/build.sh
install -m 755 bin/baidupcs-web-linux-amd64 ~/.local/bin/baidupcs-web
baidupcs-web            # 默认 127.0.0.1:8080; 数据在 ~/.local/share/baidupcs-web
baidupcs-web account add -cookie - < cookie.txt
```

常驻可用 systemd 用户服务(`ExecStart=%h/.local/bin/baidupcs-web`, `Restart=on-failure`), 保持监听回环。

## 2. 运行 OpenList

任选二进制或 Docker, 监听 `127.0.0.1:5244`, 在其配置里把站点地址设为子路径 `/files`(`site_url`)。
管理后台 → 存储 → 添加 **WebDAV**: 地址 `http://127.0.0.1:8080/dav`, 无账号密码, 挂载路径如 `/百度网盘`, WebDAV 策略**本地代理**。
以后加 Google Drive: 直接添加 GoogleDrive 存储即可。

## 3. 暴露到 tailnet

```bash
tailscale serve --bg --set-path /files http://127.0.0.1:5244/files
tailscale serve status        # 只应新增 /files, 不动其他路径
```

(Docker 部署留待后续: 容器内监听 `0.0.0.0:8080`(`BPW_LISTEN`), 宿主机至多发布到 `127.0.0.1`, 或仅容器网络内可见。)
