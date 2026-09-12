package media

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

func TestYtDlpStreamArgs(t *testing.T) {
	// 普通视频：从 90 秒起播应带 --download-sections。
	args := ytDlpStreamArgs("https://example.com/watch?v=abc", 90, false, Options{ProxyMode: ProxyModeNone})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--download-sections *90.00-inf") {
		t.Errorf("缺少 download-sections 参数: %v", args)
	}
	if !strings.Contains(joined, "-o - https://example.com/watch?v=abc") {
		t.Errorf("缺少输出到 stdout 与目标 URL: %v", args)
	}

	// 起点 0：不应携带 download-sections。
	args = ytDlpStreamArgs("https://example.com/v", 0, false, Options{ProxyMode: ProxyModeNone})
	if strings.Contains(strings.Join(args, " "), "download-sections") {
		t.Errorf("起点为 0 不应有 download-sections: %v", args)
	}

	// 直播：不支持 download-sections。
	args = ytDlpStreamArgs("https://example.com/live", 120, true, Options{ProxyMode: ProxyModeNone})
	if strings.Contains(strings.Join(args, " "), "download-sections") {
		t.Errorf("直播流不应有 download-sections: %v", args)
	}

	// 全部模式都必须输出到 stdout 且禁用播放列表展开。
	for _, live := range []bool{false, true} {
		args = ytDlpStreamArgs("u", 0, live, Options{ProxyMode: ProxyModeNone})
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--no-playlist") || !strings.Contains(joined, "-o -") {
			t.Errorf("缺少统一参数: %v", args)
		}
	}
}

// TestYtDlpStreamArgsUseConcurrentFragments 回归测试：拉流必须开启分片并发下载。
//
// 背景：yt-dlp 默认一次只下一个分片，整条流只占一条 TCP 连接。对需要代理的
// 站点（YouTube 等）这是致命瓶颈——多路复用型代理单连接往往只有几十 KB/s。
// 实测同一代理下并发 1 为 104 KB/s（77 MB 要 12 分钟），并发 8 在 45 秒内
// 下完同一视频。该测试固定住「拉流必须带 --concurrent-fragments」这一约束，
// 避免日后调整参数时把它丢掉，使投屏悄悄退回极慢的单连接下载。
func TestYtDlpStreamArgsUseConcurrentFragments(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, start := range []float64{0, 90} {
			args := ytDlpStreamArgs("https://example.com/v", start, live, Options{ProxyMode: ProxyModeNone})
			joined := strings.Join(args, " ")
			want := "--concurrent-fragments " + strconv.Itoa(streamConcurrentFragments)
			if !strings.Contains(joined, want) {
				t.Errorf("live=%v start=%v 缺少并发分片参数 %q: %v", live, start, want, args)
			}
		}
	}
	if streamConcurrentFragments < 2 {
		t.Errorf("并发数应大于 1 才有意义，当前 %d", streamConcurrentFragments)
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
