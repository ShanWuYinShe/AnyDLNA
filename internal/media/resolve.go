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
//  1. H.264 视频 + 小体积 AAC 音频：视频音频都可直通（最优）。
//  2. H.264 视频 + AAC 音频。
//  3. H.264 视频 + 任意音频：视频直通，音频按需转码。
//  4. 任意视频 + 小体积 AAC 音频。
//  5. 任意视频 + AAC 音频。
//  6. 任意视频 + 任意音频：不得已时的完整转码。
//
// 音频优先小体积（abr<=160，如 YouTube 的 140 约 10MB，而非 258 约 30MB）：
// 在线拉流是边下边合边播，大体积音频在慢代理下跟不上视频，合并输出的音频轨
// 损坏丢失（电视有画面无声音）；小体积 AAC 下载快、不断流，且 128k 在电视
// 端听感无差。abr 缺失的站点会自动落到下一级兜底，不影响可用性。
const formatSelector = "bv*[vcodec^=avc1]+ba[acodec^=mp4a][abr<=160]/" +
	"bv*[vcodec^=avc1]+ba[acodec^=mp4a]/" +
	"bv*[vcodec^=avc1]+ba/" +
	"bv*+ba[acodec^=mp4a][abr<=160]/" +
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
//
// fail-fast：慢代理下 yt-dlp 默认重试 10 次、单次 socket 等待 20 秒，
// 一次解析能拖几分钟才报错（实测某 YouTube 视频 -J 耗时 68 秒以上）。
// 这里收紧为 15 秒超时、3 次重试：真有问题早报错，而不是让电视一直转圈。
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
	args = append(args, bundledJSRuntimeArgs()...)
	return append(args, "--socket-timeout", "15", "--retries", "3")
}

// bundledJSRuntimeArgs 在自带 qjs 存在时显式启用它解 JS challenge。
//
// yt-dlp 默认只启用 deno；干净机器没有 deno/node 时 challenge 无解、
// 部分视频直接无格式。自带 qjs（2.6MB，quickjs 官方行为）经实测 8.2 秒
// 解出同视频（deno 本机 6.9 秒）。显式传二进制路径，不依赖 PATH 碰运气；
// 该 flag 是增量启用（deno 仍优先），有 deno 的机器行为不变。
// 无自带 qjs（开发环境）时返回空，保持原有逻辑。
func bundledJSRuntimeArgs() []string {
	if path, ok := ResolveTool("qjs"); ok {
		if dir := bundledToolDir(); dir != "" && strings.HasPrefix(path, dir) {
			return []string{"--js-runtimes", "quickjs:" + path}
		}
	}
	return nil
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
// 结果与 ResolveDirect 共用解析缓存：「解析预览 → 投屏」是同一 URL 的连续
// 两次解析，预览命中/预热缓存后投屏直接复用，省 7~30 秒（见 resolvecache.go）。
// yt-dlp 的报错（如站点验证提示）会截取关键内容返回，便于前端直接展示。
func Resolve(ctx context.Context, url string, opts Options) (*Resolved, error) {
	// 缓存命中不依赖 yt-dlp 在 PATH 上，提前返回。
	if resolved, _, ok := lookupResolveCache(url); ok {
		return resolved, nil
	}
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

	// 直链一并入缓存：随后投屏（ResolveDirect）命中即免二次解析。
	resolved, urls, err := parseResolveJSON(out)
	if err != nil {
		return nil, err
	}
	storeResolveCache(url, resolved, urls)
	return resolved, nil
}

// resolveRaw 是 yt-dlp -J 输出中本应用关心的子集。
// requested_formats 是格式选择式命中后的音视频分轨（含直链 url），
// 有它就能一次调用同时拿到元数据与直链，省掉第二次 -g 调用
// （慢代理下每次调用都可能是几十秒，两次串行就是失败翻倍）。
type resolveRaw struct {
	Title     string  `json:"title"`
	Duration  float64 `json:"duration"`
	IsLive    bool    `json:"is_live"`
	Extractor string  `json:"extractor_key"`
	Uploader  string  `json:"uploader"`
	VCodec    string  `json:"vcodec"`
	ACodec    string  `json:"acodec"`
	Formats   []struct {
		URL string `json:"url"`
	} `json:"requested_formats"`
}

// parseResolveJSON 从 -J 输出解析元数据与直链（1 条一体流或 2 条分离音视频）。
// 直链缺失或超过 2 条时返回空 urls，调用方回退到 DirectURLs 再取一次。
func parseResolveJSON(out []byte) (*Resolved, []string, error) {
	var raw resolveRaw
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, nil, fmt.Errorf("解析 yt-dlp 输出失败: %w", err)
	}
	resolved := &Resolved{
		Title:       raw.Title,
		DurationSec: raw.Duration,
		IsLive:      raw.IsLive,
		Extractor:   raw.Extractor,
		Uploader:    raw.Uploader,
		VideoCodec:  raw.VCodec,
		AudioCodec:  raw.ACodec,
	}
	var urls []string
	for _, f := range raw.Formats {
		if u := strings.TrimSpace(f.URL); strings.HasPrefix(u, "http") {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 || len(urls) > 2 {
		urls = nil
	}
	return resolved, urls, nil
}

// ResolveDirect 一次 -J 调用同时返回元数据与直链。
// 直链随 -J 附带返回（见 parseResolveJSON），不再单独调 -g，
// 把慢代理下两次串行调用的耗时与失败率都砍掉一半。
func ResolveDirect(ctx context.Context, url string, opts Options) (*Resolved, []string, error) {
	if !HasYtDlp() {
		return nil, nil, MissingToolError("yt-dlp")
	}
	// 缓存命中直接返回（重复投屏省 7~30 秒）；直链 TTL 内有效，见 resolvecache.go。
	if resolved, urls, ok := lookupResolveCache(url); ok {
		Diagf("解析命中缓存 站点=%s 直链=%d条 url=%.80s", resolved.Extractor, len(urls), url)
		return resolved, urls, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	t0 := time.Now()
	args := append([]string{"-J", "--no-playlist", "--no-warnings", "-f", formatSelector}, ytDlpCommonArgs(opts)...)
	cmd := toolCmdContext(ctx, "yt-dlp", append(args, url)...)
	var stderr limitBuffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		Diagf("解析失败 %.1fs url=%.80s err=%v 舍尾=%.200s", time.Since(t0).Seconds(), url, err, ytDlpErrTail(stderr.String()))
		if msg := ytDlpErrTail(stderr.String()); msg != "" {
			return nil, nil, fmt.Errorf("解析视频失败: %s", msg)
		}
		return nil, nil, fmt.Errorf("解析视频失败（站点不支持、网络不可达或代理不可用）: %w", err)
	}
	resolved, urls, perr := parseResolveJSON(out)
	if perr != nil {
		Diagf("解析失败 %.1fs url=%.80s err=%v", time.Since(t0).Seconds(), url, perr)
		return nil, nil, perr
	}
	Diagf("解析成功 %.1fs 站点=%s 直链=%d条 url=%.80s", time.Since(t0).Seconds(), resolved.Extractor, len(urls), url)
	storeResolveCache(url, resolved, urls)
	return resolved, urls, nil
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
// 管道模式（见 Transcoder）下 yt-dlp 负责下载：默认一次只下一个分片，整条流
// 只占一条 TCP 连接。对需要代理的站点（YouTube 等）这是致命瓶颈——多路复用型
// 代理单连接往往只有几十 KB/s。并发 8 是兼顾提速与代理压力的折中。
const streamConcurrentFragments = 8

// streamDownloader 强制分片下载走 yt-dlp 原生下载器，而非拉起 ffmpeg 子进程。
//
// 默认的 ffmpeg 下载器是单连接顺序拉流，且它的代理只能从环境变量继承
// --proxy 传不进去：从 Finder/Dock 启动的 GUI 应用没有这些环境变量，
// ffmpeg 子进程便直连被墙站点，轻则几十 KB/s、重则直接退出
// （ffmpeg exited with code 196）。native 下载器走 yt-dlp 自身的代理栈
// （--proxy 生效，支持 socks5）并配合 --concurrent-fragments 并发分片。
const streamDownloader = "native"

// videoOnlySelector / audioOnlySelector 是管道模式音视频分开取用的格式选择式，
// 编码偏好与 formatSelector 一致（H.264 视频、AAC 音频优先）。
//
// 必须分开取：native 下载器在多路格式同时输出到同一 stdout 时会跳过合并、
// 把音视频混写在一根管道里，下游无法解析出音频轨（电视有画面无声音）。
// 分开后两路各走一根管道，由本机的 ffmpeg 按双输入合并。
// 音频优先小体积 AAC，理由见 formatSelector。
const videoOnlySelector = "bv*[vcodec^=avc1]/bv*"

const audioOnlySelector = "ba[acodec^=mp4a][abr<=160]/ba[acodec^=mp4a]/ba"

// ytDlpSingleStreamArgs 构造把单一格式（纯视频或纯音频）写到 stdout 的参数，
// 供管道模式使用：视频路与音频路各起一个 yt-dlp 进程，互不干扰。
func ytDlpSingleStreamArgs(url, selector string, startSec float64, isLive bool, opts Options) []string {
	args := append([]string{
		"-q", "--no-playlist", "--no-warnings",
		"--concurrent-fragments", strconv.Itoa(streamConcurrentFragments),
		"--downloader", streamDownloader,
		"-f", selector,
	}, ytDlpCommonArgs(opts)...)
	if startSec > 0 && !isLive {
		args = append(args, "--download-sections", "*"+strconv.FormatFloat(startSec, 'f', 2, 64)+"-inf")
	}
	return append(args, "-o", "-", url)
}

// DirectURLs 用 yt-dlp 解析出音视频直链（-g，只取地址不下载）。
// 与 Resolve 使用同一 formatSelector，保证报告的编码与实际拉流一致。
// 返回 1 条 URL 表示一体流（progressive，音视频已合并），2 条为分离的
// 视频与音频直链；其他情况返回错误。opts 语义见 ytDlpCommonArgs。
func DirectURLs(ctx context.Context, url string, opts Options) ([]string, error) {
	if !HasYtDlp() {
		return nil, MissingToolError("yt-dlp")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := toolCmdContext(ctx, "yt-dlp", ytDlpDirectArgs(url, opts)...)
	var stderr limitBuffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := ytDlpErrTail(stderr.String()); msg != "" {
			return nil, fmt.Errorf("获取直链失败: %s", msg)
		}
		return nil, fmt.Errorf("获取直链失败（站点不支持、网络不可达或代理不可用）: %w", err)
	}
	var urls []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "http") {
			urls = append(urls, line)
		}
	}
	if len(urls) == 0 || len(urls) > 2 {
		return nil, fmt.Errorf("获取直链失败（返回 %d 条地址）", len(urls))
	}
	return urls, nil
}

// ytDlpDirectArgs 构造取直链（-g）的 yt-dlp 参数：只取地址不下载。
// 与 Resolve 使用同一 formatSelector，保证报告的编码与实际拉流一致。
func ytDlpDirectArgs(url string, opts Options) []string {
	args := append([]string{
		"-g", "--no-playlist", "--no-warnings",
		"-f", formatSelector,
	}, ytDlpCommonArgs(opts)...)
	return append(args, url)
}
