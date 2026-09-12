package media

import (
	"context"
	"strings"
	"testing"
)

func TestYtDlpStreamArgs(t *testing.T) {
	// 普通视频：从 90 秒起播应带 --download-sections。
	args := ytDlpStreamArgs("https://example.com/watch?v=abc", 90, false, "", "")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--download-sections *90.00-inf") {
		t.Errorf("缺少 download-sections 参数: %v", args)
	}
	if !strings.Contains(joined, "-o - https://example.com/watch?v=abc") {
		t.Errorf("缺少输出到 stdout 与目标 URL: %v", args)
	}

	// 起点 0：不应携带 download-sections。
	args = ytDlpStreamArgs("https://example.com/v", 0, false, "", "")
	if strings.Contains(strings.Join(args, " "), "download-sections") {
		t.Errorf("起点为 0 不应有 download-sections: %v", args)
	}

	// 直播：不支持 download-sections。
	args = ytDlpStreamArgs("https://example.com/live", 120, true, "", "")
	if strings.Contains(strings.Join(args, " "), "download-sections") {
		t.Errorf("直播流不应有 download-sections: %v", args)
	}

	// 全部模式都必须输出到 stdout 且禁用播放列表展开。
	for _, live := range []bool{false, true} {
		args = ytDlpStreamArgs("u", 0, live, "", "")
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--no-playlist") || !strings.Contains(joined, "-o -") {
			t.Errorf("缺少统一参数: %v", args)
		}
	}
}

func TestYtDlpCommonArgs(t *testing.T) {
	// 全空：不携带任何可选参数。
	if args := ytDlpCommonArgs("", ""); args != nil {
		t.Errorf("空配置不应有参数: %v", args)
	}
	// 仅代理。
	joined := strings.Join(ytDlpCommonArgs("http://127.0.0.1:10809", ""), " ")
	if !strings.Contains(joined, "--proxy http://127.0.0.1:10809") || strings.Contains(joined, "cookies") {
		t.Errorf("仅代理时参数错误: %s", joined)
	}
	// 代理 + Cookie 来源。
	joined = strings.Join(ytDlpCommonArgs("http://p:1", "chrome"), " ")
	if !strings.Contains(joined, "--proxy http://p:1") || !strings.Contains(joined, "--cookies-from-browser chrome") {
		t.Errorf("代理+Cookie 参数错误: %s", joined)
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
	if _, err := Resolve(context.Background(), "https://example.com/v", "", ""); err == nil {
		t.Fatal("yt-dlp 缺失时应返回错误")
	}
}

func TestTestProxyValidation(t *testing.T) {
	if err := TestProxy(context.Background(), ""); err == nil {
		t.Fatal("空代理应返回错误")
	}
	if err := TestProxy(context.Background(), "not-a-url"); err == nil {
		t.Fatal("无效代理地址应返回错误")
	}
}
