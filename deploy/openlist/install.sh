#!/usr/bin/env bash
# install.sh — 把"绑定百度账号"入口(bind-baidu.js)装进 OpenList 的 customize_body 设置。可重复执行。
#   用法: OPENLIST=http://127.0.0.1:5244/files bash install.sh
#   管理员密码取自 ~/.local/share/openlist/admin-credentials.txt(或环境变量 OPENLIST_ADMIN_PASSWORD)。
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"
OPENLIST="${OPENLIST:-http://127.0.0.1:5244/files}"
DAV="${DAV_ADDRESS:-http://127.0.0.1:8080/dav}"
PW="${OPENLIST_ADMIN_PASSWORD:-$(sed -n 's/^password: //p' "$HOME/.local/share/openlist/admin-credentials.txt" 2>/dev/null)}"
[ -n "$PW" ] || { echo "缺少管理员密码" >&2; exit 1; }
python3 - "$OPENLIST" "$DAV" "$PW" "$HERE/bind-baidu.js" <<'PY'
import json, sys, urllib.request
base, dav, pw, js_path = sys.argv[1:5]
def call(path, body=None, tok=None):
    h = {"Content-Type": "application/json"}
    if tok: h["Authorization"] = tok
    req = urllib.request.Request(base + path, data=json.dumps(body).encode() if body is not None else None, headers=h)
    d = json.load(urllib.request.urlopen(req))
    if d.get("code") != 200: raise SystemExit("%s: %s" % (path, d.get("message")))
    return d["data"]
tok = call("/api/auth/login", {"username": "admin", "password": pw})["token"]
item = [s for s in call("/api/admin/setting/list", tok=tok) if s["key"] == "customize_body"][0]
js = open(js_path, encoding="utf-8").read().replace("__DAV_ADDRESS__", dav)
marker = "<!-- bpw-bind -->"
snippet = marker + "<script>" + js + "</script>"
item["value"] = snippet
call("/api/admin/setting/save", [item], tok)
print("已安装 customize_body (%d 字节)" % len(snippet))
PY
