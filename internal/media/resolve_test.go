package media

import (
	"context"
	"strings"
	"testing"
)

// TestYtDlpDirectArgs 校验取直链参数：只取地址不下载。
//
// 背景：在线投屏已改为 yt-dlp 只解析（-g）、ffmpeg 直连直链负责下载/定位/
// 合并。取链参数必须与 Resolve 同一 formatSelector（编码一致），且不能带
// 任何下载相关参数（-o、--downloader、--concurrent-fragments、
// --download-sections 都会让 -g 变味或报错）。
func TestYtDlpDirectArgs(t *testing.T) {
	args := ytDlpDirectArgs("https://example.com/watch?v=abc", Options{ProxyMode: ProxyModeNone})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-g") {
		t.Errorf("取直链必须带 -g: %v", args)
	}
	if !strings.Contains(joined, "-f "+formatSelector) {
		t.Errorf("取链应与解析用同一格式选择式: %v", args)
	}
	if !strings.Contains(joined, "--no-playlist") {
		t.Errorf("缺少 --no-playlist: %v", args)
	}
	for _, banned := range []string{"-o ", "--downloader", "--concurrent-fragments", "download-sections"} {
		if strings.Contains(joined, banned) {
			t.Errorf("取链不应带下载参数 %q: %v", banned, args)
		}
	}
	if args[len(args)-1] != "https://example.com/watch?v=abc" {
		t.Errorf("目标 URL 应为最后一个参数: %v", args)
	}
}

// TestDirectURLsRequiresYtDlp 缺 yt-dlp 时取链应直接报错。
func TestDirectURLsRequiresYtDlp(t *testing.T) {
	if HasYtDlp() {
		t.Skip("本机已安装 yt-dlp，跳过缺失场景")
	}
	if _, err := DirectURLs(context.Background(), "https://example.com/v", Options{}); err == nil {
		t.Fatal("yt-dlp 缺失时应返回错误")
	}
}

func TestYtDlpCommonArgs(t *testing.T) {
	// system 模式：由应用读出系统代理后显式传入，而不交给 yt-dlp 自行探测。
	// 这里借 HTTPS_PROXY 驱动 DetectSystemProxy，使断言不依赖本机设置。
	t.Setenv("HTTPS_PROXY", "socks5://127.0.0.1:10808")
	t.Setenv("https_proxy", "")
	joined := strings.Join(ytDlpCommonArgs(Options{ProxyMode: ProxyModeSystem}), " ")
	if !strings.Contains(joined, "--proxy socks5://127.0.0.1:10808") {
		t.Errorf("system 模式应显式传入检测到的代理: %s", joined)
	}
	// manual 模式：显式传代理地址。
	joined = strings.Join(ytDlpCommonArgs(Options{ProxyMode: ProxyModeManual, Proxy: "http://127.0.0.1:10809"}), " ")
	if !strings.Contains(joined, "--proxy http://127.0.0.1:10809") || strings.Contains(joined, "cookies") {
		t.Errorf("manual 模式参数错误: %s", joined)
	}
	// none 模式：必须显式传空串，否则 yt-dlp 会自行使用环境变量里的代理。
	joined = strings.Join(ytDlpCommonArgs(Options{ProxyMode: ProxyModeNone}), " ")
	if !strings.Contains(joined, "--proxy") {
		t.Errorf("none 模式必须显式传 --proxy 空串，否则无法真正直连: %s", joined)
	}
	// 内置浏览器 Cookies 文件优先于浏览器名。
	joined = strings.Join(ytDlpCommonArgs(Options{
		ProxyMode:     ProxyModeManual,
		Proxy:         "http://p:1",
		CookieFile:    "/tmp/cookies.txt",
		CookieBrowser: "chrome",
	}), " ")
	if !strings.Contains(joined, "--cookies /tmp/cookies.txt") || strings.Contains(joined, "cookies-from-browser") {
		t.Errorf("Cookies 文件应优先于浏览器名: %s", joined)
	}
	// 仅浏览器名。
	joined = strings.Join(ytDlpCommonArgs(Options{ProxyMode: ProxyModeSystem, CookieBrowser: "chrome"}), " ")
	if !strings.Contains(joined, "--cookies-from-browser chrome") {
		t.Errorf("缺少 cookies-from-browser: %s", joined)
	}
}

// TestSystemProxyArgsUsesCorrectScheme 回归测试：system 模式必须按正确协议
// 传递系统代理，SOCKS 尤其不能被写成 http://。
//
// 背景：macOS 上 yt-dlp 经 Python 的 _scproxy 读系统 SOCKS 代理会得到
// {'socks': 'http://127.0.0.1:10808'}——协议头与实际端口不符，yt-dlp 于是用
// HTTP 去连 SOCKS 端口，连接永久卡死（实测 5 分钟无任何输出）。
// 应用自己读取时对 SOCKS 用 socks5://，并且必须显式传给 yt-dlp 才生效。
func TestSystemProxyArgsUsesCorrectScheme(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "socks5://127.0.0.1:10808")
	t.Setenv("https_proxy", "")
	args := systemProxyArgs()
	if len(args) != 2 || args[0] != "--proxy" {
		t.Fatalf("应返回 --proxy <地址>，实际 %v", args)
	}
	if !strings.HasPrefix(args[1], "socks5://") {
		t.Errorf("SOCKS 代理必须用 socks5:// 协议头，实际 %q", args[1])
	}
}

func TestYtDlpErrTail(t *testing.T) {
	if got := ytDlpErrTail(""); got != "" {
		t.Errorf("空 stderr 应返回空串: %q", got)
	}
	msg := "ERROR: [youtube] abc: Sign in to confirm you're not a bot."
	if got := ytDlpErrTail("line1\nline2\n" + msg); !strings.Contains(got, "Sign in to confirm") {
		t.Errorf("应保留末尾错误行: %q", got)
	}
}

func TestResolveRequiresYtDlp(t *testing.T) {
	if HasYtDlp() {
		t.Skip("本机已安装 yt-dlp，跳过缺失场景")
	}
	if _, err := Resolve(context.Background(), "https://example.com/v", Options{}); err == nil {
		t.Fatal("yt-dlp 缺失时应返回错误")
	}
}

func TestTestProxyValidation(t *testing.T) {
	// none 模式：不测试代理，直接给出直连说明。
	msg, err := TestProxy(context.Background(), Options{ProxyMode: ProxyModeNone})
	if err != nil {
		t.Fatalf("直连模式不应报错: %v", err)
	}
	if !strings.Contains(msg, "直连") {
		t.Errorf("直连模式应说明未使用代理: %q", msg)
	}
	// manual 模式但未填地址：应提示填写。
	if _, err := TestProxy(context.Background(), Options{ProxyMode: ProxyModeManual}); err == nil {
		t.Fatal("manual 模式未填地址应返回错误")
	}
	// manual 模式地址非法：应提示无效。
	if _, err := TestProxy(context.Background(), Options{ProxyMode: ProxyModeManual, Proxy: "not-a-url"}); err == nil {
		t.Fatal("无效代理地址应返回错误")
	}
}

// TestEffectiveProxyAndIsHTTPProxy 校验生效代理读取与 http 判定。
//
// 背景：直链模式下 ffmpeg 直连 CDN，必须走设置里的代理且只能是 http(s)
// （ffmpeg 不支持 socks）。manual 用填的地址，system 用检测到的，
// none 为空。
func TestEffectiveProxyAndIsHTTPProxy(t *testing.T) {
	if got := EffectiveProxy(Options{ProxyMode: ProxyModeManual, Proxy: " http://127.0.0.1:10809 "}); got != "http://127.0.0.1:10809" {
		t.Errorf("manual 应返回去空格后的地址: %q", got)
	}
	if got := EffectiveProxy(Options{ProxyMode: ProxyModeNone}); got != "" {
		t.Errorf("none 应返回空: %q", got)
	}
	t.Setenv("HTTPS_PROXY", "socks5://127.0.0.1:10808")
	t.Setenv("https_proxy", "")
	if got := EffectiveProxy(Options{ProxyMode: ProxyModeSystem}); got != "socks5://127.0.0.1:10808" {
		t.Errorf("system 应返回检测到的代理: %q", got)
	}
	for _, tc := range []struct {
		proxy string
		want  bool
	}{
		{"http://127.0.0.1:10809", true},
		{"https://proxy:8443", true},
		{" HTTP://x ", true},
		{"socks5://127.0.0.1:10808", false},
		{"socks://127.0.0.1:10808", false},
		{"", false},
	} {
		if got := IsHTTPProxy(tc.proxy); got != tc.want {
			t.Errorf("IsHTTPProxy(%q) = %v, 期望 %v", tc.proxy, got, tc.want)
		}
	}
}

// TestEnvWithHTTPProxy 确认注入覆盖大小写代理变量、保留其余变量。
func TestEnvWithHTTPProxy(t *testing.T) {
	if got := EnvWithHTTPProxy([]string{"A=1"}, ""); len(got) != 1 || got[0] != "A=1" {
		t.Errorf("空代理应原样返回: %v", got)
	}
	got := EnvWithHTTPProxy([]string{"A=1", "http_proxy=old", "HTTPS_PROXY=old"}, "http://127.0.0.1:10809")
	joined := strings.Join(got, "\n")
	for _, want := range []string{"A=1", "http_proxy=http://127.0.0.1:10809", "https_proxy=http://127.0.0.1:10809"} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺少 %q: %v", want, got)
		}
	}
	if strings.Contains(joined, "old") {
		t.Errorf("旧代理值应被覆盖: %v", got)
	}
}

// TestOutputArgsDualInput 校验双输入时音频映射指向第 1 路输入（pipe:3）。
func TestOutputArgsDualInput(t *testing.T) {
	joined := strings.Join(outputArgs(Plan{Mode: OutputRemux, Container: ContainerMPEGTS, CopyVideo: true, CopyAudio: true}, 1), " ")
	if !strings.Contains(joined, "0:v:0") || !strings.Contains(joined, "1:a:0?") {
		t.Errorf("双输入应映射 0:v:0 与 1:a:0?: %s", joined)
	}
	single := strings.Join(outputArgs(Plan{Mode: OutputRemux, Container: ContainerMPEGTS, CopyVideo: true, CopyAudio: true}, 0), " ")
	if !strings.Contains(single, "0:a:0?") || strings.Contains(single, "1:a:0?") {
		t.Errorf("单输入音频仍应在第 0 路: %s", single)
	}
}

// TestNeedsPipeMode 校验拉流模式路由：CDN 拒绝 ffmpeg 直连的站点走管道。
//
// 背景：B 站 mcdn 拒绝非浏览器 HTTP 客户端（ffmpeg/curl 返回 403 或拒绝连接，
// yt-dlp 的 Python 下载栈正常），这些站点只能由 yt-dlp 下载经管道喂给 ffmpeg；
// 其余站点走直链模式（yt-dlp 只解析，ffmpeg 直连直链负责全部操作）。
func TestNeedsPipeMode(t *testing.T) {
	if !NeedsPipeMode("bilibili") {
		t.Error("bilibili 应走管道模式")
	}
	for _, ext := range []string{"youtube", "youtube_music", "generic", "", "twitter"} {
		if NeedsPipeMode(ext) {
			t.Errorf("%q 不应走管道模式", ext)
		}
	}
	if NewURLTranscoder("u", false, Options{}, Plan{}, "bilibili", nil).pipe != true {
		t.Error("bilibili 的 Transcoder 应为管道模式")
	}
	if NewURLTranscoder("u", false, Options{}, Plan{}, "youtube", nil).pipe != false {
		t.Error("youtube 的 Transcoder 应为直链模式")
	}
	if tc := NewURLTranscoder("u", false, Options{}, Plan{}, "youtube", []string{"http://a/v", "http://a/au"}); len(tc.cachedURLs) != 2 {
		t.Error("传入直链应预填缓存")
	}
}

// TestYtDlpSingleStreamSelectors 回归测试：管道模式音视频分开取。
//
// 背景：native 下载器在多路格式同时输出到同一 stdout 时会跳过合并、把音视频
// 混写在一根管道里，下游解析不出音频轨；分开取时每路都是干净的单流。
func TestYtDlpSingleStreamSelectors(t *testing.T) {
	vArgs := strings.Join(ytDlpSingleStreamArgs("u", videoOnlySelector, 0, false, Options{ProxyMode: ProxyModeNone}), " ")
	if !strings.Contains(vArgs, "-f "+videoOnlySelector) {
		t.Errorf("视频路应使用视频选择式: %s", vArgs)
	}
	aArgs := strings.Join(ytDlpSingleStreamArgs("u", audioOnlySelector, 0, false, Options{ProxyMode: ProxyModeNone}), " ")
	if !strings.Contains(aArgs, "-f "+audioOnlySelector) {
		t.Errorf("音频路应使用音频选择式: %s", aArgs)
	}
	// 音频优先小体积 AAC（大体积音频在慢代理下跟不上视频会导致合并丢轨）。
	if !strings.Contains(audioOnlySelector, "abr<=160") {
		t.Errorf("音频选择式应优先小体积 AAC: %s", audioOnlySelector)
	}
}
