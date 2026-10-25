#!/usr/bin/env bash
# smoke_all.sh — W9 全量回归(需求 §3.12 W6 验收清单/NFR-03/FR-01/06/11/15 冒烟):
#   1) health                              → HEALTH_OK
#   2) 无鉴权直接访问受保护 API                → NOAUTH_OK
#   3) 文件列表/配额(桩直链)                  → FILES_OK / QUOTA_OK
#   4) 签名下载直链 + Range/206               → DL_OK
#   5) 三段式上传(init→chunk→complete)        → UPLOAD_OK
#   6) 空载 RSS 采样 ≤100MB(NFR-03)           → RSS_OK (<N> KB)
#   7) 双平台产物存在性检查(build.sh 产物)    → ARTIFACTS_OK
# 全部通过输出 SMOKE_ALL_OK, 退出码 0。
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GO=/usr/local/go/bin/go
WORK="$(mktemp -d /tmp/bpw-smoke-all.XXXXXX)"
SRV_DIR="$WORK/server-data"
TMP_DIR="$WORK/tmp"
LISTEN="127.0.0.1:18360"
FAIL=0

cleanup() {
  [[ -n "${SRV_PID:-}" ]] && kill -9 "$SRV_PID" 2>/dev/null
  [[ -n "${SRC_PID:-}" ]] && kill -9 "$SRC_PID" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT
mkdir -p "$SRV_DIR" "$TMP_DIR"

# ---------- 1. 桩下载源(模拟百度 dlink: Range/206 + 固定内容) ----------
cat > "$WORK/src.go" <<'EOF'
package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		body := []byte(strings.Repeat("A", 65536))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "65536")
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-65535/65536")
			w.WriteHeader(206)
		}
		_, _ = w.Write(body)
	})
	srv := &http.Server{Addr: "127.0.0.1:18361", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	fmt.Println("SRC_READY")
	_ = srv.ListenAndServe()
}
EOF
"$GO" build -o "$WORK/src" "$WORK/src.go" || exit 1
"$WORK/src" >"$WORK/src.log" 2>&1 &
SRC_PID=$!
for _ in $(seq 1 40); do curl -s -o /dev/null "http://127.0.0.1:18361/dl/x" && break; sleep 0.1; done

# ---------- 2. 起真实服务(桩 Engine: 直链走桩源, 上传走桩引擎) ----------
"$GO" build -o "$WORK/srv" "$ROOT/scripts/smoke_all_srv/main.go" || exit 1
"$WORK/srv" -listen "$LISTEN" -data "$SRV_DIR" -tmp "$TMP_DIR" >"$WORK/server.log" 2>&1 &
SRV_PID=$!
for _ in $(seq 1 80); do
  [[ "$(curl -s -o /dev/null -w '%{http_code}' "http://$LISTEN/api/health")" == "200" ]] && break
  sleep 0.1
done

# ---------- 3. health ----------
H=$(curl -s "http://$LISTEN/api/health")
if echo "$H" | grep -q '"status":"ok"' && echo "$H" | grep -q '"version":"smoke-w9"'; then
  echo "HEALTH_OK"
else
  echo "FAIL: health 异常: $H"; FAIL=1
fi

# ---------- 4. 无鉴权(口令登录已移除) ----------
CJ="$WORK/cookies.txt"; : >"$CJ"
code=$(curl -s -o /dev/null -w '%{http_code}' "http://$LISTEN/api/quota")
if [[ "$code" == "200" ]]; then
  echo "NOAUTH_OK"
else
  echo "FAIL: 无鉴权访问 /api/quota 应 200, got $code"; FAIL=1
fi

# ---------- 5. 文件列表 + 配额(FR-05/06) ----------
F=$(curl -s -b "$CJ" "http://$LISTEN/api/files?dir=/")
if echo "$F" | grep -q '冒烟文件.bin'; then
  echo "FILES_OK"
else
  echo "FAIL: 文件列表异常: $F"; FAIL=1
fi
Q=$(curl -s -b "$CJ" "http://$LISTEN/api/quota")
if echo "$Q" | grep -q '"quota"'; then
  echo "QUOTA_OK"
else
  echo "FAIL: 配额异常: $Q"; FAIL=1
fi

# ---------- 6. 签名直链下载 + Range/206(FR-11/12/39②) ----------
DLURL=$(curl -s -b "$CJ" "http://$LISTEN/api/dl/url?path=%2F%E5%86%92%E7%83%9F%E6%96%87%E4%BB%B6.bin")
URL=$(echo "$DLURL" | sed -n 's/.*"url":"\([^"]*\)".*/\1/p' | sed 's/\\u0026/\&/g')
case "$URL" in /*) URL="http://$LISTEN$URL" ;; esac
code=$(curl -s -b "$CJ" -o /dev/null -w '%{http_code}' -H "Range: bytes=0-1023" "$URL")
if [[ "$code" == "206" || "$code" == "200" ]]; then
  echo "DL_OK (range=$code)"
else
  echo "FAIL: 下载应 206/200, got $code (url=$URL)"; FAIL=1
fi

# ---------- 7. 三段式上传(FR-15) ----------
INIT=$(curl -s -b "$CJ" -H 'Content-Type: application/json' \
  -d '{"filename":"w9smoke.bin","size":16,"policy":"overwrite"}' "http://$LISTEN/api/upload/init")
TASKID=$(echo "$INIT" | sed -n 's/.*"task_id":"\([^"]*\)".*/\1/p')
if [[ -z "$TASKID" ]]; then
  echo "FAIL: upload/init 异常: $INIT"; FAIL=1
else
  head -c 16 /dev/zero > "$WORK/chunk.bin"
  CH=$(curl -s -b "$CJ" -o /dev/null -w '%{http_code}' -X POST \
    -F "file=@$WORK/chunk.bin" "http://$LISTEN/api/upload/$TASKID?seq=0")
  CM=$(curl -s -b "$CJ" -o /dev/null -w '%{http_code}' -X POST \
    "http://$LISTEN/api/upload/$TASKID/complete")
  if [[ "$CH" == "200" ]] && [[ "$CM" == "200" ]]; then
    echo "UPLOAD_OK"
  else
    echo "FAIL: 上传链异常 chunk=$CH complete=$CM"; FAIL=1
  fi
fi

# ---------- 8. 空载 RSS 采样 ≤100MB(NFR-03) ----------
RSS_KB=$(ps -o rss= -p "$SRV_PID" 2>/dev/null | tr -d ' ')
RSS_KB=${RSS_KB:-0}
if (( RSS_KB <= 100 * 1024 )); then
  echo "RSS_OK (${RSS_KB} KB)"
else
  echo "FAIL: RSS ${RSS_KB} KB 超 100MB 预算"; FAIL=1
fi

# ---------- 9. 双平台产物存在性(NFR-05) ----------
if [[ -x "$ROOT/bin/baidupcs-web-linux-amd64" && -x "$ROOT/bin/baidupcs-web-linux-arm64" ]]; then
  echo "ARTIFACTS_OK"
else
  echo "FAIL: 缺 bin/baidupcs-web-linux-{amd64,arm64}, 请先 bash scripts/build.sh"; FAIL=1
fi

# ---------- 10. 判定 ----------
if [[ $FAIL -eq 0 ]]; then
  echo "SMOKE_ALL_OK"
  exit 0
fi
exit 1
