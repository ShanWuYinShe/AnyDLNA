#!/bin/sh
# 一键构建自带工具的 AnyDLNA.app：wails 构建 → 塞工具 → 重新签名。
# 用法：./scripts/build-app.sh
set -eu

cd "$(dirname "$0")/.."
export PATH="$HOME/go/bin:$PATH"

echo "== 1/3 wails 构建"
wails build

APP="build/bin/AnyDLNA.app"

echo "== 2/3 打包自带工具"
./scripts/bundle-tools.sh "$APP"

echo "== 3/3 重新签名（塞文件后原签名失效，adhoc 重签）"
codesign --force --deep --sign - "$APP"
codesign --verify --deep --strict "$APP" 2>&1 | head -n 3 || true

echo "== 完成：$APP"
ls -la "$APP/Contents/Resources/tools"
ls -la "$APP/Contents/MacOS/" | head -n 4
