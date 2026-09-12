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
// 环境变量与操作系统代理）；system 模式不传参，交给 yt-dlp 自行探测。
// CookieFile（内置浏览器导出）优先于 CookieBrowser（读取本机浏览器）。
func ytDlpCommonArgs(opts Options) []string {
	var args []string
	switch opts.ProxyMode {
	case ProxyModeManual:
		args = append(args, "--proxy", opts.Proxy)
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

// ytDlpStreamArgs 构造把在线视频（已合并音视频）写到 stdout 的 yt-dlp 参数。
// 与 Resolve 使用同一 formatSelector，保证解析阶段报告编码与实际拉流一致；
// startSec>0 且非直播时用 --download-sections 实现快进到指定位置；
// opts 语义见 ytDlpCommonArgs。
func ytDlpStreamArgs(url string, startSec float64, isLive bool, opts Options) []string {
	args := append([]string{"-q", "--no-playlist", "--no-warnings", "-f", formatSelector}, ytDlpCommonArgs(opts)...)
	if startSec > 0 && !isLive {
		args = append(args, "--download-sections", "*"+strconv.FormatFloat(startSec, 'f', 2, 64)+"-inf")
	}
	return append(args, "-o", "-", url)
}
