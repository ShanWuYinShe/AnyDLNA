package media

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Resolved 是 yt-dlp 解析在线视频得到的关键元数据。
type Resolved struct {
	Title       string  `json:"title"`
	DurationSec float64 `json:"durationSec"`
	IsLive      bool    `json:"isLive"`
	Extractor   string  `json:"extractor"`
	Uploader    string  `json:"uploader"`
	// VideoCodec / AudioCodec 是所选格式的编码，用于判断能否免转码直通。
	VideoCodec string `json:"videoCodec"`
	AudioCodec string `json:"audioCodec"`
}

// formatSelector 是 yt-dlp 的格式选择表达式。
//
// 首选顺序刻意把 H.264（avc1）与 AAC（mp4a）排在前面：这两个编码电视可原生
// 解码，从而让视频轨道能免转码直通（换封装）。yt-dlp 默认会挑 AV1/VP9 等
// 更高压缩率的编码，那类源在电视端必须完整转码——实测 4K 转码仅约 1.4 倍
// 实时，是播放卡顿的主因。
//
// 逐级回退，保证任何站点都能选出可用的格式：
//  1. H.264 视频 + AAC 音频：视频音频都可直通（最优）。
//  2. H.264 视频 + 任意音频：视频直通，音频按需转码。
//  3. 任意视频 + AAC 音频。
//  4. 任意视频 + 任意音频：不得已时的完整转码。
const formatSelector = "bv*[vcodec^=avc1]+ba[acodec^=mp4a]/" +
	"bv*[vcodec^=avc1]+ba/" +
	"bv*+ba[acodec^=mp4a]/" +
	"bv*+ba/b"

// HasYtDlp 报告 yt-dlp 是否可用（含常见安装目录，见 ResolveTool）。
func HasYtDlp() bool {
	_, ok := ResolveTool("yt-dlp")
	return ok
}

// ytDlpCommonArgs 构造代理与 Cookies 相关的公共参数。
// manual 模式显式指定代理；none 模式传空串强制直连（否则 yt-dlp 会自行读取
// 环境变量与操作系统代理）；system 模式由应用自己读出系统代理后显式传入。
// CookieFile（内置浏览器导出）优先于 CookieBrowser（读取本机浏览器）。
func ytDlpCommonArgs(opts Options) []string {
	var args []string
	switch opts.ProxyMode {
	case ProxyModeManual:
		args = append(args, "--proxy", opts.Proxy)
	case ProxyModeSystem:
		args = append(args, systemProxyArgs()...)
	case ProxyModeNone:
		args = append(args, "--proxy", "")
	}
	if opts.CookieFile != "" {
		args = append(args, "--cookies", opts.CookieFile)
	} else if opts.CookieBrowser != "" {
		args = append(args, "--cookies-from-browser", opts.CookieBrowser)
	}
	return args
}

// systemProxyArgs 返回 system 模式下应传给 yt-dlp 的代理参数。
//
// 不能让 yt-dlp 自行探测操作系统代理：macOS 上它经 Python 的 _scproxy 读取，
// 而 _scproxy 把系统 SOCKS 代理报成 {'socks': 'http://127.0.0.1:10808'}——
// 协议头是 http，端口却说的是 SOCKS，于是 yt-dlp 用 HTTP 去连 SOCKS 端口，
// 连接直接卡死（实测 5 分钟无任何输出）。这里改由应用按正确协议读取
// （见 parseScutilProxy，SOCKS 用 socks5://）并显式传入。
//
// 未能读出代理时不传参，保持 yt-dlp 原有的环境变量与系统探测行为。
func systemProxyArgs() []string {
	proxy := strings.TrimSpace(DetectSystemProxy())
	if proxy == "" {
		return nil
	}
	return []string{"--proxy", proxy}
}

// Resolve 用 yt-dlp 解析视频页面 URL，提取标题、时长与直播标记。
// 仅读取元数据（-J），不拉取媒体流；opts 语义见 ytDlpCommonArgs。
// yt-dlp 的报错（如站点验证提示）会截取关键内容返回，便于前端直接展示。
func Resolve(ctx context.Context, url string, opts Options) (*Resolved, error) {
	if !HasYtDlp() {
		return nil, MissingToolError("yt-dlp")
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	args := append([]string{"-J", "--no-playlist", "--no-warnings", "-f", formatSelector}, ytDlpCommonArgs(opts)...)
	cmd := toolCmdContext(ctx, "yt-dlp", append(args, url)...)
	var stderr limitBuffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := ytDlpErrTail(stderr.String()); msg != "" {
			return nil, fmt.Errorf("解析视频失败: %s", msg)
		}
		return nil, fmt.Errorf("解析视频失败（站点不支持、网络不可达或代理不可用）: %w", err)
	}

	var raw struct {
		Title     string  `json:"title"`
		Duration  float64 `json:"duration"`
		IsLive    bool    `json:"is_live"`
		Extractor string  `json:"extractor_key"`
		Uploader  string  `json:"uploader"`
		VCodec    string  `json:"vcodec"`
		ACodec    string  `json:"acodec"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("解析 yt-dlp 输出失败: %w", err)
	}
	return &Resolved{
		Title:       raw.Title,
		DurationSec: raw.Duration,
		IsLive:      raw.IsLive,
		Extractor:   raw.Extractor,
		Uploader:    raw.Uploader,
		VideoCodec:  raw.VCodec,
		AudioCodec:  raw.ACodec,
	}, nil
}

// ytDlpErrTail 提取 yt-dlp 报错的最后几行（错误摘要在末尾），最长 300 字符。
func ytDlpErrTail(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	lines := strings.Split(stderr, "\n")
	if n := len(lines); n > 3 {
		lines = lines[n-3:]
	}
	tail := strings.Join(lines, " ")
	if len(tail) > 300 {
		tail = tail[len(tail)-300:]
	}
	return tail
}

// streamConcurrentFragments 是下载 DASH/HLS 分片时的并发连接数。
//
// yt-dlp 默认一次只下一个分片，即整条流只占一条 TCP 连接。这对国内直连的
// 站点无所谓，但在需要代理的站点（YouTube 等）上是致命瓶颈：单连接的吞吐
// 由这条连接自身的延迟与丢包决定，而多路复用型代理（XHTTP、mux 等）单连接
// 往往只有几十 KB/s——浏览器之所以快，正因为它对同一域名会同时开多条连接。
//
// 实测（VLESS + XHTTP 代理，同一 YouTube 视频，45 秒采样）：
//   - 并发 1：仅下到 2.4 MB（104 KB/s，77 MB 预计 12 分钟）
//   - 并发 8：81 MB 完整下载完成（峰值 835 KB/s，音频段 4–8 MB/s）
//
// 取 8 是兼顾提速与不给代理造成过多并发压力的折中。
const streamConcurrentFragments = 8

// streamDownloader 强制分片下载走 yt-dlp 原生下载器，而非拉起 ffmpeg 子进程。
//
// 默认的 ffmpeg 下载器是单连接顺序拉流，且它的代理只能从环境变量继承
// --proxy 传不进去：终端里因 http_proxy 环境变量碰巧能用（但单连接仍慢），
// 从 Finder/Dock 启动的 GUI 应用没有这些环境变量，ffmpeg 子进程便直连
// 被墙站点，轻则几十 KB/s、重则直接退出（ffmpeg exited with code 196），
// 电视端表现为“一直在下载中、网速几十 K、永远无法起播”。
// native 下载器走 yt-dlp 自身的代理栈（--proxy 生效，支持 socks5）并配合
// --concurrent-fragments 并发分片；ffmpeg 只做本地合并，不再碰网络。
// 实测同一 YouTube 视频经 SOCKS+XHTTP 代理：ffmpeg 下载器 0 字节（直接失败），
// native 下载器平均 6 MB/s。yt-dlp 会在 native 不支持时自动回退，无需担心兼容。
const streamDownloader = "native"

// ytDlpStreamArgs 构造把在线视频（已合并音视频）写到 stdout 的 yt-dlp 参数。
// 与 Resolve 使用同一 formatSelector，保证解析阶段报告编码与实际拉流一致；
// startSec>0 且非直播时用 --download-sections 实现快进到指定位置；
// opts 语义见 ytDlpCommonArgs。
func ytDlpStreamArgs(url string, startSec float64, isLive bool, opts Options) []string {
	args := append([]string{
		"-q", "--no-playlist", "--no-warnings",
		"--concurrent-fragments", strconv.Itoa(streamConcurrentFragments),
		"--downloader", streamDownloader,
		"-f", formatSelector,
	}, ytDlpCommonArgs(opts)...)
	if startSec > 0 && !isLive {
		args = append(args, "--download-sections", "*"+strconv.FormatFloat(startSec, 'f', 2, 64)+"-inf")
	}
	return append(args, "-o", "-", url)
}
