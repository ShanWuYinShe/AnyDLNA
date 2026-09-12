package media

import (
	"context"
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

func TestYtDlpCommonArgs(t *testing.T) {
	// system 模式：不传代理参数，交给 yt-dlp 自行读取系统/环境代理。
	if args := ytDlpCommonArgs(Options{ProxyMode: ProxyModeSystem}); args != nil {
		t.Errorf("system 模式不应有参数: %v", args)
	}
	// manual 模式：显式传代理地址。
	joined := strings.Join(ytDlpCommonArgs(Options{ProxyMode: ProxyModeManual, Proxy: "http://127.0.0.1:10809"}), " ")
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
