#!/usr/bin/env bash
# smoke_all.sh — 全量冒烟(桩 Engine + 桩下载源, 无网络):
#   1) health                                     → HEALTH_OK
#   2) 账号子命令 + 旧页面/旧接口已移除             → ACCOUNT_CLI_OK / REMOVED_SURFACES_OK
#   3) WebDAV PROPFIND/HEAD/Range GET/PUT/MKCOL/COPY → DAV_*_OK
#   4) 空载 RSS 采样 ≤100MB(NFR-03)                → RSS_OK (<N> KB)
#   5) 双平台产物存在性检查(build.sh 产物)          → ARTIFACTS_OK
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

# ---------- 4. 无鉴权 + 账号子命令(服务没有 Web 页面, 账号经命令行管理) ----------
$GO build -o "$WORK/bpw" "$ROOT/cmd/baidupcs-web" || exit 1
A=$("$WORK/bpw" account list -addr "$LISTEN" 2>&1)
if echo "$A" | grep -q 'smoke-w9'; then
  echo "ACCOUNT_CLI_OK"
else
  echo "FAIL: account list 异常: $A"; FAIL=1
fi
# 已删除的页面/旧接口不应再存在
bad=""
for p in / /assets/app.js /api/files /api/quota /api/login /api/upload/init /api/events; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "http://$LISTEN$p")
  [[ "$code" == "404" || "$code" == "405" ]] || bad="$bad $p=$code"
done
if [[ -z "$bad" ]]; then
  echo "REMOVED_SURFACES_OK"
else
  echo "FAIL: 旧入口仍可访问:$bad"; FAIL=1
fi

# ---------- 5. WebDAV(OpenList 挂载入口): PROPFIND / HEAD / Range GET / PUT / MKCOL / COPY ----------
P=$(curl -s -X PROPFIND -H 'Depth: 1' "http://$LISTEN/dav/")
if echo "$P" | grep -q '冒烟文件.bin' && echo "$P" | grep -q 'collection'; then
  echo "DAV_PROPFIND_OK"
else
  echo "FAIL: PROPFIND 异常: $P"; FAIL=1
fi
HL=$(curl -s -I "http://$LISTEN/dav/%E5%86%92%E7%83%9F%E6%96%87%E4%BB%B6.bin" | tr -d '\r')
if echo "$HL" | grep -qi '^content-length: 65536' && echo "$HL" | grep -qi '^accept-ranges: bytes'; then
  echo "DAV_HEAD_OK"
else
  echo "FAIL: DAV HEAD 异常: $HL"; FAIL=1
fi
code=$(curl -s -o "$WORK/dav_range.bin" -w '%{http_code}' -H "Range: bytes=0-99" \
  "http://$LISTEN/dav/%E5%86%92%E7%83%9F%E6%96%87%E4%BB%B6.bin")
if [[ "$code" == "206" && -s "$WORK/dav_range.bin" ]]; then
  echo "DAV_RANGE_OK"
else
  echo "FAIL: DAV Range GET 应 206 且有内容, got $code"; FAIL=1
fi
code=$(curl -s -o /dev/null -w '%{http_code}' -X PUT --data-binary 'dav-smoke' "http://$LISTEN/dav/w9dav.bin")
if [[ "$code" == "201" || "$code" == "204" ]]; then
  echo "DAV_PUT_OK ($code)"
else
  echo "FAIL: DAV PUT 应 201/204, got $code"; FAIL=1
fi
code=$(curl -s -o /dev/null -w '%{http_code}' -X MKCOL "http://$LISTEN/dav/%E6%96%B0%E7%9B%AE%E5%BD%95")
if [[ "$code" == "201" ]]; then
  echo "DAV_MKCOL_OK ($code)"
else
  echo "FAIL: DAV MKCOL 应 201, got $code"; FAIL=1
fi
code=$(curl -s -o /dev/null -w '%{http_code}' -X COPY \
  -H "Destination: http://$LISTEN/dav/%E7%85%A7%E7%89%87/%E5%86%92%E7%83%9F%E6%96%87%E4%BB%B6.bin" \
  "http://$LISTEN/dav/%E5%86%92%E7%83%9F%E6%96%87%E4%BB%B6.bin")
if [[ "$code" == "201" || "$code" == "204" ]]; then
  echo "DAV_COPY_OK ($code)"
else
  echo "FAIL: DAV COPY 应 201/204, got $code"; FAIL=1
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
