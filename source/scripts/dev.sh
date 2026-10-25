#!/usr/bin/env bash
# dev.sh — 开发测试模式: 回环监听 + /baidu 前缀 + DEBUG 日志(go run, 改代码重跑即生效)。
# 对外暴露(tailnet HTTPS)另行执行一次:
#   tailscale serve --bg --set-path /baidu http://127.0.0.1:8080/baidu
# 取消: tailscale serve --set-path /baidu off
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
exec go run ./cmd/baidupcs-web \
  -listen "${BPW_LISTEN:-127.0.0.1:8080}" \
  -base-path "${BPW_BASE_PATH:-/baidu}" \
  -log-level "${BPW_LOG_LEVEL:-DEBUG}" "$@"
