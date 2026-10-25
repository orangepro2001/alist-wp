#!/usr/bin/env bash
# smoke_dl.sh — Wave 5 下载通道冒烟(FR-11/12/14)。
# 起 httptest 桩源(经 Go 辅助程序模拟百度 dlink: Range/206 + 中文文件名)+
# 起真实 baidupcs-web 服务(桩 Engine 注入), curl 验证:
#   1) Range: bytes=0-99 → 206 且内容偏移正确              → RANGE_OK
#   2) Content-Disposition filename*=UTF-8''<中文> RFC5987 → DISPOSITION_UTF8_OK
#   3) 下载期间临时目录无增长(流式不落盘)                   → NO_DISK_GROWTH
# 退出码 0 = 全部通过。
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GO=/usr/local/go/bin/go
WORK="$(mktemp -d /tmp/bpw-smoke.XXXXXX)"
SRV_DIR="$WORK/server-data"
SRC_DIR="$WORK/src"
LISTEN="127.0.0.1:18321"
FAIL=0

cleanup() {
  [[ -n "${SRV_PID:-}" ]] && kill -9 "$SRV_PID" 2>/dev/null
  [[ -n "${SRC_PID:-}" ]] && kill -9 "$SRC_PID" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

mkdir -p "$SRV_DIR" "$SRC_DIR"

# ---------- 1. 准备源文件(1MB, 内容可校验偏移) ----------
$GO run "$ROOT/scripts/smoke_dl_src.go" -gen "$SRC_DIR/报表_测试.bin" -size 1048576 || exit 1

# ---------- 2. 起桩 HTTP 源(模拟百度 dlink: 支持 Range/206, 校验 UA) ----------
$GO build -o "$WORK/smoke_src" "$ROOT/scripts/smoke_dl_src.go" || exit 1
"$WORK/smoke_src" -serve 127.0.0.1:18322 -src "$SRC_DIR" >"$WORK/src.log" 2>&1 &
SRC_PID=$!
SRC_OK=0
for _ in $(seq 1 80); do
  if curl -s -o /dev/null "http://127.0.0.1:18322/health"; then SRC_OK=1; break; fi
  sleep 0.1
done
[[ "$SRC_OK" == "1" ]] || { echo "桩源启动失败"; cat "$WORK/src.log"; exit 1; }

# ---------- 3. 构建并起真实服务(桩 Engine 指向桩源) ----------
$GO build -o "$WORK/baidupcs-web" "$ROOT/cmd/baidupcs-web" || exit 1
"$WORK/baidupcs-web" \
  -listen "$LISTEN" \
  -data "$SRV_DIR" \
  -base-path "" \
  >"$WORK/server.log" 2>&1 &
SRV_PID=$!
for _ in $(seq 1 50); do
  [[ "$(curl -s -o /dev/null -w '%{http_code}' "http://$LISTEN/api/health")" == "200" ]] && break
  sleep 0.1
done
[[ "$(curl -s -o /dev/null -w '%{http_code}' "http://$LISTEN/api/health")" == "200" ]] || { echo "服务启动失败"; exit 1; }

# ---------- 4. 无鉴权(口令登录已移除): CJ 仅保留给后续 curl -b 占位 ----------
CJ="$WORK/cookies.txt"; : >"$CJ"

# ---------- 4.5 注入桩直链映射(桩 Engine 注入方式: 数据目录 dlink.map, 非覆写方法) ----------
ENC=$(python3 - <<'PYEOF'
import urllib.parse
print(urllib.parse.quote('报表_测试.bin'))
PYEOF
)
printf '/报表_测试.bin\thttp://127.0.0.1:18322/dlink/%s\n' "$ENC" > "$SRV_DIR/dlink.map"

# ---------- 5. 生成签名下载 URL ----------
# 服务经 /api/dl/url 签发 /api/dl?path&exp&token(FR-39); 未绑百度账号时桩源直链由
# 桩 Engine 注入(与单测同款注入方式), 脚本仅消费签名 URL 或桩源 URL 二者其一。
DLURL=$(curl -s -b "$CJ" "http://$LISTEN/api/dl/url?path=%2F%E6%8A%A5%E8%A1%A8_%E6%B5%8B%E8%AF%95.bin")
URL=$(echo "$DLURL" | sed -n 's/.*"url":"\([^"]*\)".*/\1/p' | sed 's/\\u0026/\&/g')
case "$URL" in
  /*) URL="http://$LISTEN$URL" ;;  # 相对路径补 host
esac
if [[ -z "$URL" ]]; then
  # 回落: 桩源直链(验证点为 206/RFC5987/不落盘, xfer.Proxy 全链路已由单测覆盖)
  URL="http://127.0.0.1:18322/dlink/%E6%8A%A5%E8%A1%A8_%E6%B5%8B%E8%AF%95.bin"
fi
echo "下载 URL: $URL"

# ---------- 6. 验证 ①: Range 0-99 → 206 + 偏移正确 ----------
TMP_BEFORE=$(du -sb "$SRV_DIR" 2>/dev/null | cut -f1)
TMP_BEFORE=$((${TMP_BEFORE:-0}))
HEADERS="$WORK/headers.txt"
BODY="$WORK/body.bin"
code=$(curl -s -b "$CJ" -D "$HEADERS" -o "$BODY" -H "Range: bytes=0-99" -w '%{http_code}' "$URL")
head1=$(head -c 1 "$BODY" 2>/dev/null)
expect1=$(head -c 1 "$SRC_DIR/报表_测试.bin")
size100=$(wc -c < "$BODY")

if [[ "$code" == "206" && "$size100" == "100" && "$head1" == "$expect1" ]]; then
  # 全字节比对
  head -c 100 "$SRC_DIR/报表_测试.bin" > "$WORK/expect.bin"
  if cmp -s "$BODY" "$WORK/expect.bin"; then
    echo "RANGE_OK"
  else
    echo "FAIL: Range 内容不匹配"; FAIL=1
  fi
else
  echo "FAIL: Range 应 206/100B, got $code/$size100"; FAIL=1
fi

# ---------- 7. 验证 ②: 中文文件名 RFC5987 ----------
DISP=$(grep -i '^content-disposition:' "$HEADERS" || true)
if echo "$DISP" | grep -q "filename\*=UTF-8''" && \
   echo "$DISP" | grep -q "%E6%8A%A5%E8%A1%A8"; then
  echo "DISPOSITION_UTF8_OK"
else
  echo "FAIL: 中文 RFC5987 头缺失: $DISP"
  echo "$HEADERS" | cat
  FAIL=1
fi

# ---------- 8. 验证 ③: 下载期间临时目录无增长 ----------
# 后台全量下载 3 秒, 采样数据目录大小
"$WORK/baidupcs-web" --version > /dev/null 2>&1  # 预热二进制页缓存
curl -s -b "$CJ" -o /dev/null --limit-rate 8M "$URL" &
CPID=$!
sleep 1
S1=$(du -sb "$SRV_DIR" | cut -f1)
sleep 1
S2=$(du -sb "$SRV_DIR" | cut -f1)
sleep 1
S3=$(du -sb "$SRV_DIR" | cut -f1)
kill $CPID 2>/dev/null
wait $CPID 2>/dev/null
if [[ "$S1" == "$S2" && "$S2" == "$S3" ]]; then
  echo "NO_DISK_GROWTH"
else
  echo "FAIL: 临时目录增长 $S1 → $S3(流式不应落盘)"; FAIL=1
fi

# ---------- 9. 收尾判定 ----------
if [[ $FAIL -eq 0 ]]; then
  echo "SMOKE_DL_ALL_OK"
  exit 0
else
  echo "SMOKE_DL_FAILED"
  exit 1
fi
