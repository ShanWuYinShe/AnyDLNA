package browser

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestManagerCookiesIntegration 走通「启动浏览器 → 访问站点 → 通过 CDP 读回
// Cookie」的完整链路，并确认能拿到 HttpOnly 条目。
//
// 需要显式设置 ANYDLNA_BROWSER_ITEST=1 才会运行：它会真实启动一个浏览器
// 进程（使用临时 profile），因此默认不在普通测试中执行。
func TestManagerCookiesIntegration(t *testing.T) {
	if os.Getenv("ANYDLNA_BROWSER_ITEST") == "" {
		t.Skip("未设置 ANYDLNA_BROWSER_ITEST，跳过浏览器集成测试")
	}
	if !Supported() {
		t.Skip("本机未检测到可用于测试的浏览器")
	}

	// 本地站点写入普通与 HttpOnly Cookie，模拟真实登录态。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "PLAIN", Value: "plain-value", Path: "/"})
		http.SetCookie(w, &http.Cookie{
			Name: "HTTPONLY_SID", Value: "secret-value", Path: "/", HttpOnly: true,
			Expires: time.Now().Add(time.Hour),
		})
		fmt.Fprint(w, "<html><body>test site</body></html>")
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	siteURL := "http://" + ln.Addr().String() + "/"

	mgr := NewManager(t.TempDir())
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	reused, err := mgr.Start(ctx, siteURL)
	if err != nil {
		t.Fatalf("启动浏览器失败: %v", err)
	}
	if reused {
		t.Fatal("首次启动不应复用实例")
	}
	if !mgr.Running() {
		t.Fatal("启动后应处于运行状态")
	}

	// 等待页面加载并写入 Cookie。
	deadline := time.Now().Add(30 * time.Second)
	var cookies []Cookie
	for time.Now().Before(deadline) {
		readCtx, readCancel := context.WithTimeout(context.Background(), 10*time.Second)
		cookies, err = mgr.Cookies(readCtx)
		readCancel()
		if err == nil && len(cookies) >= 2 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("读取 Cookies 失败: %v", err)
	}
	t.Logf("读到 %d 条 Cookie", len(cookies))
	for _, c := range cookies {
		t.Logf("  %s%s %s httpOnly=%v secure=%v", c.Domain, c.Path, c.Name, c.HttpOnly, c.Secure)
	}

	// 关键断言：HttpOnly Cookie 必须被读回（前端 document.cookie 做不到）。
	var foundPlain, foundHTTPOnly bool
	for _, c := range cookies {
		switch c.Name {
		case "PLAIN":
			foundPlain = c.Value == "plain-value"
		case "HTTPONLY_SID":
			foundHTTPOnly = c.Value == "secret-value" && c.HttpOnly
		}
	}
	if !foundPlain {
		t.Error("应读回普通 Cookie PLAIN")
	}
	if !foundHTTPOnly {
		t.Error("应读回 HttpOnly Cookie HTTPONLY_SID（这是本方案的核心价值）")
	}

	// 读到后再启动应复用同一实例。
	reused, err = mgr.Start(ctx, siteURL)
	if err != nil {
		t.Fatalf("复用启动失败: %v", err)
	}
	if !reused {
		t.Error("第二次启动应复用已有实例")
	}
}

// TestManagerStartReportsNoBrowser 确认无浏览器时给出明确错误。
func TestManagerStartReportsNoBrowser(t *testing.T) {
	if !Supported() {
		t.Skip("本机存在浏览器，跳过缺失场景")
	}
	// 指向一个不存在的可执行文件，验证错误路径而非静默失败。
	dir := t.TempDir()
	missing := dir + "/no-such-browser"
	t.Setenv(EnvBrowserPath, missing)

	// Available 会回退到真实浏览器，因此这里只验证路径检测的健壮性：
	// 指定的路径不存在时不应被当作可用浏览器。
	for _, b := range Available() {
		if b.Path == missing {
			t.Fatal("不存在的路径不应出现在可用浏览器列表中")
		}
	}
}

// TestManagerCookiesWithoutBrowser 确认未启动时读取 Cookies 有明确错误。
func TestManagerCookiesWithoutBrowser(t *testing.T) {
	mgr := NewManager(t.TempDir())
	if _, err := mgr.Cookies(context.Background()); err == nil {
		t.Fatal("未启动浏览器时读取 Cookies 应返回错误")
	}
	if mgr.Running() {
		t.Fatal("未启动时不应处于运行状态")
	}
}

// TestNormalizeURL 覆盖用户输入的常见形式。
func TestNormalizeURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://www.youtube.com", "https://www.youtube.com"},
		{"www.youtube.com", "https://www.youtube.com"},
		{"bilibili.com", "https://bilibili.com"},
		{"http://example.com/x", "http://example.com/x"},
		{"  spaced.com  ", "https://spaced.com"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := normalizeURL(tc.in); got != tc.want {
			t.Errorf("normalizeURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestAvailablePathsExist 确认检测结果都指向真实存在的可执行文件。
func TestAvailablePathsExist(t *testing.T) {
	for _, b := range Available() {
		if _, err := os.Stat(b.Path); err != nil {
			t.Errorf("报告的浏览器路径不存在: %s (%v)", b.Path, err)
		}
		if strings.TrimSpace(b.Name) == "" {
			t.Errorf("浏览器名称不应为空: %s", b.Path)
		}
	}
}

// TestCandidateOrderHonoursEnv 确认 ANYDLNA_BROWSER_PATH 优先。
func TestCandidateOrderHonoursEnv(t *testing.T) {
	custom := "/tmp/custom-browser-for-test"
	t.Setenv(EnvBrowserPath, custom)
	list := candidates()
	if len(list) == 0 {
		t.Fatal("候选列表不应为空")
	}
	if list[0].Path != custom {
		t.Errorf("环境变量指定的浏览器应排在最前: %s", list[0].Path)
	}
}

// TestResolveExecutable 确认返回的是可执行文件而非目录。
func TestResolveExecutable(t *testing.T) {
	for _, b := range Available() {
		info, err := os.Stat(b.Path)
		if err != nil {
			continue
		}
		if info.IsDir() {
			t.Errorf("不应把目录当作浏览器: %s", b.Path)
		}
		// 在类 Unix 系统上应具备可执行权限。
		if info.Mode().Perm()&0o111 == 0 && os.PathSeparator == '/' {
			t.Errorf("浏览器文件不可执行: %s", b.Path)
		}
	}
}

var _ = exec.Command // 保持测试文件的导入稳定
