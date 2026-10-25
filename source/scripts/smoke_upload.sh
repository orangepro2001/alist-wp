#!/usr/bin/env bash
# smoke_upload.sh — Wave 6 上传通道冒烟(FR-15/16/17)。
# 真实起服(server.NewWithEngine 注入桩 Engine, 与 W4/W5 桩注入模式同源)+
# curl 走三段式 REST(init→chunk→complete), 验证:
#   1) 小文件上传成功且临时目录 10s 内清理              → TMP_DELETED_10S
#   2) 秒传路径命中(rapid 文件名)或分片路径成功          → RAPID_OR_UPLOAD_OK
#   3) 孤儿文件(25h 前 mtime)被服务启动全扫清除          → ORPHAN_PURGED
# 退出码 0 = 全部通过。
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GO=/usr/local/go/bin/go
WORK="$(mktemp -d /tmp/bpw-smoke-up.XXXXXX)"
SRV_DIR="$WORK/server-data"
TMP_DIR="$WORK/tmp"
LISTEN="127.0.0.1:18331"
FAIL=0

cleanup() {
  [[ -n "${SRV_PID:-}" ]] && kill -9 "$SRV_PID" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

mkdir -p "$SRV_DIR" "$TMP_DIR"

# ---------- 0. 预置孤儿文件(25h 前 mtime, 服务启动全扫应清除) ----------
ORPHAN="$TMP_DIR/uptask-orphan-smoke-ghost.bin"
echo "ghost" > "$ORPHAN"
touch -d "25 hours ago" "$ORPHAN"

# ---------- 1. 构建并起真实服务(桩 Engine 注入) ----------
$GO build -o "$WORK/smoke_srv" "$ROOT/scripts/smoke_upload_srv/main.go" || exit 1
"$WORK/smoke_srv" \
  -listen "$LISTEN" \
  -data "$SRV_DIR" \
  -tmp "$TMP_DIR" \
  >"$WORK/server.log" 2>&1 &
SRV_PID=$!
for _ in $(seq 1 80); do
  [[ "$(curl -s -o /dev/null -w '%{http_code}' "http://$LISTEN/api/health")" == "200" ]] && break
  sleep 0.1
done
[[ "$(curl -s -o /dev/null -w '%{http_code}' "http://$LISTEN/api/health")" == "200" ]] || { echo "服务启动失败"; cat "$WORK/server.log"; exit 1; }

# 孤儿应已被启动全扫删除(mtime>24h 且无任务引用)
if [[ ! -e "$ORPHAN" ]]; then
  echo "ORPHAN_PURGED"
else
  echo "FAIL: 孤儿文件未被启动扫描清除: $ORPHAN"
  FAIL=1
fi

# ---------- 2. 无鉴权(口令登录已移除): CJ 仅保留给后续 curl -b 占位 ----------
CJ="$WORK/cookies.txt"; : >"$CJ"

# ---------- 3. 用例①: 小文件(分片路径, 3MB < 8MB 单片) ----------
HEAD_A="$WORK/init_a.json"
INIT_A=$(curl -s -b "$CJ" -H 'Content-Type: application/json' \
  -d '{"filename":"报表_v1.bin","target_dir":"/","size":3145728}' "http://$LISTEN/api/upload/init")
SID_A=$(echo "$INIT_A" | sed -n 's/.*"task_id":"\([^"]*\)".*/\1/p')
[[ -n "$SID_A" ]] || { echo "FAIL: init 未返回 task_id: $INIT_A"; exit 1; }
head -c 3145728 /dev/urandom > "$WORK/a.bin"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -X POST \
  -H 'X-Chunk-Seq: 0' --data-binary @"$WORK/a.bin" \
  "http://$LISTEN/api/upload/$SID_A?seq=0")
[[ "$code" == "200" ]] || { echo "FAIL: 分片上传 $code"; exit 1; }
COMP_A=$(curl -s -b "$CJ" -X POST "http://$LISTEN/api/upload/$SID_A/complete")
TASK_A=$(echo "$COMP_A" | sed -n 's/.*"task_id":"\([^"]*\)".*/\1/p')
[[ -n "$TASK_A" ]] || { echo "FAIL: complete 未返回 task_id: $COMP_A"; exit 1; }
echo "分片路径任务: $TASK_A"

# ---------- 4. 用例②: 秒传路径(桩: 文件名含 .rapid. 即命中) ----------
INIT_B=$(curl -s -b "$CJ" -H 'Content-Type: application/json' \
  -d '{"filename":"相册_rapid_.zip","target_dir":"/","size":1048576}' "http://$LISTEN/api/upload/init")
SID_B=$(echo "$INIT_B" | sed -n 's/.*"task_id":"\([^"]*\)".*/\1/p')
head -c 1048576 /dev/urandom > "$WORK/b.zip"
curl -s -o /dev/null -b "$CJ" -X POST -H 'X-Chunk-Seq: 0' \
  --data-binary @"$WORK/b.zip" "http://$LISTEN/api/upload/$SID_B?seq=0"
COMP_B=$(curl -s -b "$CJ" -X POST "http://$LISTEN/api/upload/$SID_B/complete")
TASK_B=$(echo "$COMP_B" | sed -n 's/.*"task_id":"\([^"]*\)".*/\1/p')
[[ -n "$TASK_B" ]] || { echo "FAIL: 秒传用例 complete 失败: $COMP_B"; exit 1; }

# ---------- 5. 等任务终态(桩上传即时完成), 查任务行 ----------
final_state() {  # $1=taskID → 输出 state
  curl -s -b "$CJ" "http://$LISTEN/api/tasks" | tr ',' '\n' | grep -A0 "\"id\":\"$1\"" >/dev/null || true
  curl -s -b "$CJ" "http://$LISTEN/api/tasks" | python3 -c "
import json,sys
d=json.load(sys.stdin)
for row in d.get('list',[]):
    if row['id']=='$1': print(row['state'])
"
}
ST_A=""; ST_B=""
for _ in $(seq 1 50); do
  ST_A=$(final_state "$TASK_A"); ST_B=$(final_state "$TASK_B")
  [[ "$ST_A" == "done" && "$ST_B" == "done" ]] && break
  sleep 0.2
done
if [[ "$ST_A" == "done" && "$ST_B" == "done" ]]; then
  if grep -q "STUB_UPLOAD /报表_v1.bin rapid=false" "$WORK/server.log" && \
     grep -q "STUB_UPLOAD /相册_rapid_.zip rapid=true" "$WORK/server.log"; then
    echo "RAPID_OR_UPLOAD_OK"
  else
    echo "FAIL: 桩引擎调用记录不符"; grep STUB_UPLOAD "$WORK/server.log"; FAIL=1
  fi
else
  echo "FAIL: 任务终态不符: $TASK_A=$ST_A $TASK_B=$ST_B"; FAIL=1
fi

# ---------- 6. 用例③: 终态后 10s 内临时目录清空(检查分片文件消失) ----------
# 服务装配用 tempmgr.DeleteDelay=10s; 任务 done 后由 complete 登记删除, 窗口内检查。
DEADLINE=$((SECONDS + 14))
TMP_GONE=0
while (( SECONDS < DEADLINE )); do
  # 分片落盘文件名: uptask-<engineTaskID>-*
  if ! ls "$TMP_DIR"/uptask-* >/dev/null 2>&1; then
    TMP_GONE=1; break
  fi
  sleep 0.5
done
if [[ "$TMP_GONE" == "1" ]]; then
  echo "TMP_DELETED_10S"
else
  echo "FAIL: 终态 10s 后临时目录仍有文件:"; ls -la "$TMP_DIR"; FAIL=1
fi

# ---------- 7. 水位拒绝(507 DISK_LOW)冒烟: 磁盘剩余必然 <1EB 阈值不成立, 改为单测覆盖; 此处仅验证 init 参数非法拒绝 ----------
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$CJ" -H 'Content-Type: application/json' \
  -d '{"filename":"","target_dir":"/"}' "http://$LISTEN/api/upload/init")
[[ "$code" == "400" ]] || { echo "FAIL: 空 filename 应 400, got $code"; FAIL=1; }

# ---------- 8. 断链清理: 上传一半后 abort ----------
INIT_C=$(curl -s -b "$CJ" -H 'Content-Type: application/json' \
  -d '{"filename":"中断.bin","target_dir":"/","size":99999999}' "http://$LISTEN/api/upload/init")
SID_C=$(echo "$INIT_C" | sed -n 's/.*"task_id":"\([^"]*\)".*/\1/p')
head -c 1048576 /dev/urandom > "$WORK/c.bin"
curl -s -o /dev/null -b "$CJ" -X POST -H 'X-Chunk-Seq: 0' \
  --data-binary @"$WORK/c.bin" "http://$LISTEN/api/upload/$SID_C?seq=0"
curl -s -o /dev/null -b "$CJ" -X POST "http://$LISTEN/api/upload/$SID_C/abort"
if ! ls "$TMP_DIR"/uptask-*-"中断.bin" >/dev/null 2>&1; then
  echo "ABORT_CLEANED_OK"
else
  echo "FAIL: abort 后半成品仍在"; ls "$TMP_DIR"; FAIL=1
fi

if [[ $FAIL -eq 0 ]]; then
  echo "SMOKE_UPLOAD_ALL_OK"
  exit 0
fi
exit 1
