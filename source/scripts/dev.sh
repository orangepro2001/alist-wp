#!/usr/bin/env bash
# dev.sh — 开发测试模式: 仅回环监听 127.0.0.1:8080 + DEBUG 日志(go run, 改代码重跑即生效)。
# 服务只提供 WebDAV(/dav/)与账号接口(/api/accounts*); 绑定账号用:
#   go run ./cmd/baidupcs-web account add -cookie - < cookie.txt
# 对外入口只有 OpenList 的 tailscale /files(见 README), 本服务不单独暴露。
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
exec go run ./cmd/baidupcs-web \
  -listen "${BPW_LISTEN:-127.0.0.1:8080}" \
  -log-level "${BPW_LOG_LEVEL:-DEBUG}" "$@"
