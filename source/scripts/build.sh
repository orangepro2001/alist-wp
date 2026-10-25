#!/usr/bin/env bash
# build.sh — W9 交付构建(NFR-05): CGO_ENABLED=0 双平台 linux/amd64+linux/arm64,
# -ldflags 注入版本(git describe 失败回落日期), 产出 bin/baidupcs-web-linux-{amd64,arm64}。
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GO=${GO:-/usr/local/go/bin/go}

# 版本串: git describe(有 tag 用 tag) → 失败/非 git 目录回落 UTC 日期。
VERSION="$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || true)"
if [[ -z "$VERSION" ]]; then
  VERSION="$(date -u +%Y%m%d)"
fi
LDFLAGS="-s -w -X main.version=$VERSION"

echo "版本: $VERSION"
mkdir -p "$ROOT/bin"

for arch in amd64 arm64; do
  echo "构建 linux/$arch ..."
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
    "$GO" build -trimpath -ldflags "$LDFLAGS" -o "bin/baidupcs-web-linux-$arch" ./cmd/baidupcs-web)
done

ls -l "$ROOT"/bin/baidupcs-web-linux-*
echo "BUILD_OK"
