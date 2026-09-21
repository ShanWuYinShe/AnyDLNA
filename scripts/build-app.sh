#!/bin/sh
# 一键构建自带工具的 AnyDLNA.app：wails 构建 → 塞工具 → 重新签名。
# 用法：./scripts/build-app.sh
set -eu

cd "$(dirname "$0")/.."
# wails 由 go install 装在 $HOME/go/bin：目录存在才追加 PATH，干净机器上
# 不盲目展开不存在的目录。
if [ -d "$HOME/go/bin" ]; then
	export PATH="$HOME/go/bin:$PATH"
fi

# 提前失败：缺 wails CLI 时给出清晰指引，而不是跑到一半报 command not found。
if ! command -v wails >/dev/null 2>&1; then
	echo "错误：未找到 wails CLI（wails build 需要）。" >&2
	echo "安装命令（版本须与 go.mod 中 wails/v2 一致，当前为 v2.15.0）：" >&2
	echo "  go install github.com/wailsapp/wails/v2/cmd/wails@v2.15.0" >&2
	echo "若已安装：请确认 \$HOME/go/bin 在 PATH 中后重试。" >&2
	exit 1
fi

echo "== 1/3 wails 构建"
wails build

APP="build/bin/AnyDLNA.app"

echo "== 2/3 打包自带工具"
./scripts/bundle-tools.sh "$APP"

# adhoc 签名仅本机可用：拷给他人会被 Gatekeeper 拦截。若要对外分发，把
# `--sign -` 换成 Developer ID Identity 并补公证（xcrun notarytool），完整
# 手动流程见 RELEASE.md（需要 Apple Developer 账号）。
echo "== 3/3 重新签名（塞文件后原签名失效，adhoc 重签）"
codesign --force --deep --sign - "$APP"
codesign --verify --deep --strict "$APP" 2>&1 | head -n 3 || true

echo "== 完成：$APP"
ls -la "$APP/Contents/Resources/tools"
ls -la "$APP/Contents/MacOS/" | head -n 4
