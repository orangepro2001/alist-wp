#!/usr/bin/env bash
# smoke_sharetmp.sh — Wave 8 FR-40 分享直连下载冒烟。
# 真实起服(桩 Engine 注入, 同 W4~W6 桩模式)+ curl 走 /api/share/preview → /api/share/dl:
#   1) 浏览分享内容(share_id+文件清单返回)                      → PREVIEW_OK
#   2) 直连下载完成后 10s 内临时目录回删(STUB_REMOVE 可查)       → TMP_DIR_GONE_10S
#   3) 配额不足: 中文报错且无 .tmp- 转存残留                     → QUOTA_PRECHECK_OK
#   4) 提取码错: 明确提示; 禁止转存: 明确提示(附加断言)
# 退出码 0 = 全部通过。
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GO=/usr/local/go/bin/go
WORK="$(mktemp -d /tmp/bpw-smoke-st.XXXXXX)"
SRV_DIR="$WORK/server-data"
TMP_DIR="$WORK/tmp"
LISTEN="127.0.0.1:18340"
FAIL=0

cleanup() {
  [[ -n "${SRV_PID:-}" ]] && kill -9 "$SRV_PID" 2>/dev/null
  [[ -n "${POOR_PID:-}" ]] && kill -9 "$POOR_PID" 2>/dev/null
  [[ -n "${SRC_PID:-}" ]] && kill -9 "$SRC_PID" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT
mkdir -p "$SRV_DIR" "$TMP_DIR"

# ---------- 0. 桩下载源(模拟百度 dlink: 18341, 返回固定内容流) ----------
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
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(200)
		w.Write([]byte(strings.Repeat("D", 1024)))
	})
	srv := &http.Server{Addr: "127.0.0.1:18341", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	fmt.Println("SRC_READY")
	_ = srv.ListenAndServe()
}
EOF
/usr/local/go/bin/go build -o "$WORK/src" "$WORK/src.go" || exit 1
"$WORK/src" >"$WORK/src.log" 2>&1 &
SRC_PID=$!
for _ in $(seq 1 40); do curl -s -o /dev/null http://127.0.0.1:18341/dl/x && break; sleep 0.1; done

login() {  # $1=port → cookie 占位文件(口令登录已移除, 无需鉴权)
  CJ="$WORK/ck_$1.txt"; : >"$CJ"
  echo "$CJ"
}

start_srv() {  # $1=port $2=poor → pid
  $GO build -o "$WORK/w8srv_$1" "$ROOT/scripts/smoke_sharetmp_srv/main.go" || exit 1
  local poorflag=()
  [[ "$2" == "poor" ]] && poorflag=(-poor)
  "$WORK/w8srv_$1" -listen "127.0.0.1:$1" -data "$SRV_DIR" -tmp "$TMP_DIR" \
    "${poorflag[@]}" >"$WORK/srv_$1.log" 2>&1 &
  local pid=$!
  for _ in $(seq 1 80); do
    [[ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$1/api/health")" == "200" ]] && break
    sleep 0.1
  done
  [[ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$1/api/health")" == "200" ]] \
    || { echo "服务启动失败"; cat "$WORK/srv_$1.log"; exit 1; }
  echo "$pid"
}

# ========== 实例 1: 正常路径(预览+直连下载+目录回删) ==========
SRV_PID=$(start_srv 18340 ok)
CJ=$(login 18340)

# --- 用例 ①: 预览 ---
PREV=$(curl -s -b "$CJ" -H 'Content-Type: application/json' \
  -d '{"link":"http://pan.baidu.com/s/smoke1","passcode":"a1b2"}' "http://$LISTEN/api/share/preview")
JOB=$(echo "$PREV" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p')
if [[ -n "$JOB" ]] && echo "$PREV" | grep -q '"share_id":"smoke-share-1"' && echo "$PREV" | grep -q '文件1.bin'; then
  echo "PREVIEW_OK"
else
  echo "FAIL: 预览应答异常: $PREV"; exit 1
fi

# --- 用例 ②: 直连下载(勾选 fsid 1,2)→ done → 10s 内目录回删 ---
DL=$(curl -s -b "$CJ" -H 'Content-Type: application/json' \
  -d "{\"job_id\":\"$JOB\",\"fs_ids\":[\"1\",\"2\"]}" "http://$LISTEN/api/share/dl")
echo "$DL" | grep -q '"ok":true' || { echo "FAIL: 启动直连下载失败: $DL"; exit 1; }
# 等作业终态
STATE=""
for _ in $(seq 1 60); do
  STATE=$(curl -s -b "$CJ" "http://$LISTEN/api/share/dl" | python3 -c "
import json,sys
d=json.load(sys.stdin)
for j in d.get('list',[]):
    if j['id']=='$JOB': print(j['phase'])
" 2>/dev/null)
  [[ "$STATE" == "done" || "$STATE" == "failed" ]] && break
  sleep 0.3
done
if [[ "$STATE" == "done" ]] && grep -q "STUB REMOVE /来自分享/.tmp-smoke-share-1" "$WORK/srv_18340.log"; then
  echo "TMP_DIR_GONE_10S"
else
  echo "FAIL: 作业终态=$STATE, 回删日志:"; grep "REMOVE" "$WORK/srv_18340.log"; FAIL=1
fi

# --- 附加: 提取码错 ---
BADPC=$(curl -s -b "$CJ" -H 'Content-Type: application/json' \
  -d '{"link":"http://pan.baidu.com/s/smoke1","passcode":"0000"}' "http://$LISTEN/api/share/preview")
if echo "$BADPC" | grep -q "提取码错误"; then
  echo "BAD_PASSCODE_OK"
else
  echo "FAIL: 提取码错提示缺失: $BADPC"; FAIL=1
fi

# --- 附加: 禁止转存 ---
BAN=$(curl -s -b "$CJ" -H 'Content-Type: application/json' \
  -d '{"link":"http://pan.baidu.com/s/ban-smoke","passcode":"a1b2"}' "http://$LISTEN/api/share/preview")
if echo "$BAN" | grep -q "该分享不支持转存"; then
  echo "TRANSFER_FORBIDDEN_OK"
else
  echo "FAIL: 禁止转存提示缺失: $BAN"; FAIL=1
fi

# ========== 实例 2: 配额不足(独立进程, -poor) ==========
POOR_PID=$(start_srv 18342 poor)
CJ2=$(login 18342)
P2=$(curl -s -b "$CJ2" -H 'Content-Type: application/json' \
  -d '{"link":"http://pan.baidu.com/s/poor1","passcode":"a1b2"}' "http://127.0.0.1:18342/api/share/preview")
JOB2=$(echo "$P2" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p')
DL2=$(curl -s -b "$CJ2" -H 'Content-Type: application/json' \
  -d "{\"job_id\":\"$JOB2\",\"fs_ids\":[\"1\",\"2\"]}" "http://127.0.0.1:18342/api/share/dl")
ST2=""
for _ in $(seq 1 60); do
  ST2=$(curl -s -b "$CJ2" "http://127.0.0.1:18342/api/share/dl" | python3 -c "
import json,sys
d=json.load(sys.stdin)
for j in d.get('list',[]):
    if j['id']=='$JOB2': print(j['phase']+'|'+j.get('message','')+'|'+j.get('err_code',''))
" 2>/dev/null)
  [[ "$ST2" == failed* ]] && break
  sleep 0.3
done
if echo "$ST2" | grep -q "网盘剩余空间不足, 无法转存分享内容" && \
   ! grep -q "STUB TRANSFER_TMP" "$WORK/srv_18342.log"; then
  echo "QUOTA_PRECHECK_OK"
else
  echo "FAIL: 配额预检分支异常: $ST2 / 转存日志:"; grep "TRANSFER_TMP" "$WORK/srv_18342.log"; FAIL=1
fi

if [[ $FAIL -eq 0 ]]; then
  echo "SMOKE_SHARETMP_ALL_OK"
  exit 0
fi
exit 1
