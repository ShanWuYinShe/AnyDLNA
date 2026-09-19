#!/bin/sh
# 把 pin 好版本的外部工具打进 .app，供 ResolveTool 自带目录优先查找。
# 用法：./scripts/bundle-tools.sh [AnyDLNA.app 路径，默认 build/bin/AnyDLNA.app]
# 失败即停（set -e），任一步 download 失败都不要产出半成品包。
set -eu

# ---- 版本 pin（升级工具只改这里；ffmpeg 按架构分开 pin，见下） ----
YTDLP_VERSION="${YTDLP_VERSION:-2026.08.19}"
QUICKJS_VERSION="${QUICKJS_VERSION:-2026-06-04}"
# quickjs 官方只发源码包，构建机需有 make 与 clang（Xcode CLT 即可）。
QUICKJS_URL="https://bellard.org/quickjs/quickjs-${QUICKJS_VERSION}.tar.xz"

APP="${1:-build/bin/AnyDLNA.app}"
# 绝对路径：后面要 cd 到缓存目录，相对路径会失效。
case "$APP" in
/*) ;;
*) APP="$(pwd)/$APP" ;;
esac
TOOLS="$APP/Contents/Resources/tools"
# 锚定仓库根（脚本可在任意 cwd 调用），避免缓存目录随调用位置漂移
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CACHE="${CACHE_DIR:-$ROOT/.workwork/toolcache}"
ARCH="$(uname -m)"

echo "== bundle-tools: $APP (arch=$ARCH)"
# 架构检查必须在任何拷贝/下载落盘之前：ffmpeg 只支持 arm64，晚检查会在
# 已塞入 yt-dlp 的 .app 上中止（set -e），留下签名失效的半成品。
if [ "$ARCH" != "arm64" ]; then
	echo "仅支持 arm64 打包，当前 $ARCH" >&2
	exit 1
fi
mkdir -p "$TOOLS" "$CACHE"
cd "$CACHE"

fetch() { # fetch <url> <output>
	curl -sSL --retry 5 --retry-all-errors --max-time 1200 -o "$2" "$1"
}

# fetch_big <url> <output>：8 路 Range 并行下载后拼接。
# 大文件（ffmpeg 静态包几十 MB）经抖动代理单连接必断：实测 -C - 续传会被
# 中间环节截断归零，并行分段每段独立重试，坏一段只重下那一段。
fetch_big() {
	url="$1"; out="$2"
	# 只取 1 字节读 Content-Range 拿长度（此前用 size_download 会下完整文件）。
	size="$(curl -sSL -D - -o /dev/null --max-time 60 -r 0-0 "$url" | awk 'tolower($1)=="content-range:" {split($3,a,"/"); print a[2]}' | tail -n 1 | tr -d '\r')"
	# 拿不到长度就退化为普通下载。
	case "$size" in ''|0) fetch "$url" "$out"; return ;; esac
	# part 文件带本进程 PID 后缀：被 kill 的旧任务若残留，会写同一目录，
	# 实测混写后尺寸碰巧正确、内容损坏（unzip 中段报错），必须隔离。
	tag="$$"
	n=8; seg=$(( (size + n - 1) / n )); i=0
	while [ "$i" -lt "$n" ]; do
		start=$(( i * seg )); end=$(( start + seg - 1 ))
		[ "$end" -ge "$size" ] && end=$(( size - 1 ))
		(
			# 卡住的连接不等它：20 秒低于 5KB/s 直接毙掉重建。
			for _ in 1 2 3 4 5 6 7 8; do
				if curl -sSL --max-time 900 --speed-limit 5000 --speed-time 20 \
				   -r "$start-$end" -o "$out.part$tag.$i" "$url" && \
				   [ "$(wc -c < "$out.part$tag.$i" 2>/dev/null || echo 0)" -eq "$(( end - start + 1 ))" ]; then
					exit 0
				fi
				sleep 2
			done
			exit 1
		) &
		i=$(( i + 1 ))
	done
	wait
	i=0; : > "$out"
	while [ "$i" -lt "$n" ]; do
		cat "$out.part$tag.$i" >> "$out"; rm -f "$out.part$tag.$i"; i=$(( i + 1 ))
	done
	# zip 完整性校验：坏包在此失败，不产出半成品。
	unzip -t -q "$out"
}

# ---- yt-dlp：官方独立二进制（无 Python 依赖） ----
if [ ! -x "$TOOLS/yt-dlp" ]; then
	echo "-- yt-dlp $YTDLP_VERSION"
	fetch "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/yt-dlp_macos" yt-dlp
	chmod +x yt-dlp
	cp yt-dlp "$TOOLS/yt-dlp"
fi
"$TOOLS/yt-dlp" --version

# ---- ffmpeg：osxexperts arm64 静态构建（含 libx264/aac，转码必需）。
# ffprobe 已不需要：本地探测是 Go 原生（见 internal/media/goprobe.go）。
# 只支持 arm64（evermeet 只有 x86_64 实测弃用；Intel Mac 不再维护），
# 检查已前移至脚本开头。
FFMPEG_URL="https://www.osxexperts.net/ffmpeg9arm.zip"
# cached_zip <file>：缓存包完好时跳过下载。
cached_zip() {
	[ -f "$1" ] && unzip -t -q "$1" >/dev/null 2>&1
}
if [ ! -x "$TOOLS/ffmpeg" ]; then
	echo "-- ffmpeg ($ARCH) $FFMPEG_URL"
	fetch_big "$FFMPEG_URL" ffmpeg.zip
	unzip -o -q ffmpeg.zip -d ffmpeg-ex
	cp ffmpeg-ex/ffmpeg "$TOOLS/ffmpeg"
	chmod +x "$TOOLS/ffmpeg"
fi
"$TOOLS/ffmpeg" -version 2>/dev/null | head -n 1

# ---- qjs：quickjs 源码构建（仅 libSystem 依赖，约 1MB，解 JS challenge 用） ----
if [ ! -x "$TOOLS/qjs" ]; then
	echo "-- qjs $QUICKJS_VERSION (源码构建)"
	fetch "$QUICKJS_URL" quickjs.tar.xz
	rm -rf "quickjs-${QUICKJS_VERSION}"
	tar xf quickjs.tar.xz
	(cd "quickjs-${QUICKJS_VERSION}" && make qjs -j"$(sysctl -n hw.ncpu)")
	cp "quickjs-${QUICKJS_VERSION}/build/qjs" "$TOOLS/qjs" 2>/dev/null || \
	cp "quickjs-${QUICKJS_VERSION}/qjs" "$TOOLS/qjs"
	chmod +x "$TOOLS/qjs"
fi
"$TOOLS/qjs" --help 2>&1 | head -n 1

# 迁移清理：ffprobe 已被 Go 原生探测替代，旧包残留的删掉（省 51MB）。
rm -f "$TOOLS/ffprobe"

echo "== 自带工具就绪：$(ls "$TOOLS")"
