package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   Config
		want Config
	}{
		{
			name: "非法代理模式回退到默认",
			in:   Config{ProxyMode: "bogus", CookieMode: CookieModeNone},
			want: Config{ProxyMode: ProxyModeSystem, CookieMode: CookieModeNone},
		},
		{
			name: "非手动模式清空代理地址",
			in:   Config{ProxyMode: ProxyModeSystem, ProxyURL: "http://127.0.0.1:10809", CookieMode: CookieModeNone},
			want: Config{ProxyMode: ProxyModeSystem, CookieMode: CookieModeNone},
		},
		{
			name: "手动模式保留代理地址并去空格",
			in:   Config{ProxyMode: ProxyModeManual, ProxyURL: "  http://127.0.0.1:10809  ", CookieMode: CookieModeNone},
			want: Config{ProxyMode: ProxyModeManual, ProxyURL: "http://127.0.0.1:10809", CookieMode: CookieModeNone},
		},
		{
			name: "非浏览器模式清空浏览器名",
			in:   Config{ProxyMode: ProxyModeNone, CookieMode: CookieModeLoginBrowser, CookieBrowser: "chrome"},
			want: Config{ProxyMode: ProxyModeNone, CookieMode: CookieModeLoginBrowser},
		},
		{
			name: "浏览器模式归一化浏览器名",
			in:   Config{ProxyMode: ProxyModeNone, CookieMode: CookieModeBrowser, CookieBrowser: "  Chrome "},
			want: Config{ProxyMode: ProxyModeNone, CookieMode: CookieModeBrowser, CookieBrowser: "chrome"},
		},
		{
			name: "非法 Cookie 模式回退为不使用",
			in:   Config{ProxyMode: ProxyModeNone, CookieMode: "bogus", CookieBrowser: "chrome"},
			want: Config{ProxyMode: ProxyModeNone, CookieMode: CookieModeNone},
		},
		{
			name: "投屏显示名称去首尾空格",
			in:   Config{ProxyMode: ProxyModeSystem, CookieMode: CookieModeNone, CastTitle: "  xxx 投屏  "},
			want: Config{ProxyMode: ProxyModeSystem, CookieMode: CookieModeNone, CastTitle: "xxx 投屏"},
		},
		{
			name: "投屏显示名称保留 {title} 占位符",
			in:   Config{ProxyMode: ProxyModeSystem, CookieMode: CookieModeNone, CastTitle: "{title} · 来自 AnyDLNA"},
			want: Config{ProxyMode: ProxyModeSystem, CookieMode: CookieModeNone, CastTitle: "{title} · 来自 AnyDLNA"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Normalize(); got != tc.want {
				t.Errorf("Normalize() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestLoadConfigMigratesLegacyFormat 确保早期仅含 proxy / cookie_browser 的
// 配置文件能正确迁移，避免老用户升级后配置丢失。
func TestLoadConfigMigratesLegacyFormat(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir) // os.UserConfigDir 在 macOS 上读 HOME

	// 旧格式：有代理、有浏览器名。
	path, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"proxy":"http://127.0.0.1:10809","cookie_browser":"chrome"}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	got := LoadConfig()
	if got.ProxyMode != ProxyModeManual || got.ProxyURL != "http://127.0.0.1:10809" {
		t.Errorf("旧 proxy 字段应迁移为 manual 模式: %+v", got)
	}
	if got.CookieMode != CookieModeBrowser || got.CookieBrowser != "chrome" {
		t.Errorf("旧 cookie_browser 字段应迁移为 browser 模式: %+v", got)
	}

	// 旧格式：直连（proxy 为空）应按新默认跟随系统代理。
	if err := os.WriteFile(path, []byte(`{"proxy":"","cookie_browser":""}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got = LoadConfig()
	if got.ProxyMode != ProxyModeSystem {
		t.Errorf("旧格式空 proxy 应迁移为 system 模式: %+v", got)
	}
	if got.CookieMode != CookieModeNone {
		t.Errorf("旧格式空 cookie_browser 应迁移为不使用: %+v", got)
	}
}

// TestLoadConfigMissingOrBroken 确认配置缺失或损坏时回退到默认值而不报错。
func TestLoadConfigMissingOrBroken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	if got := LoadConfig(); got.ProxyMode != ProxyModeSystem || got.CookieMode != CookieModeNone {
		t.Errorf("配置文件不存在时应返回默认配置: %+v", got)
	}

	path, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{ 这不是合法 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadConfig(); got.ProxyMode != ProxyModeSystem {
		t.Errorf("配置损坏时应回退默认配置: %+v", got)
	}
}

// TestResolveOptionsCookieFile 确认 loginbrowser 模式只在文件存在且非空时启用。
func TestResolveOptionsCookieFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	cfg := Config{ProxyMode: ProxyModeNone, CookieMode: CookieModeLoginBrowser}
	if opts := cfg.ResolveOptions(); opts.CookieFile != "" {
		t.Errorf("Cookies 文件尚不存在时不应启用: %+v", opts)
	}

	path, err := CookieFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if opts := cfg.ResolveOptions(); opts.CookieFile != "" {
		t.Errorf("Cookies 文件为空时不应启用: %+v", opts)
	}

	if err := os.WriteFile(path, []byte(NetscapeHeader+"a\tb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if opts := cfg.ResolveOptions(); opts.CookieFile != path {
		t.Errorf("Cookies 文件存在且非空时应启用: %+v", opts)
	}
}

// TestSaveConfigRoundTrip 确认保存后能原样读回。
func TestSaveConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	want := Config{ProxyMode: ProxyModeManual, ProxyURL: "http://127.0.0.1:7890",
		CookieMode: CookieModeBrowser, CookieBrowser: "firefox",
		CastTitle: "{title} · 来自 AnyDLNA"}
	if err := SaveConfig(want); err != nil {
		t.Fatal(err)
	}
	if got := LoadConfig(); got != want {
		t.Errorf("保存后读回不一致: got %+v, want %+v", got, want)
	}
	// 权限 0600：手动代理地址可能内嵌认证信息，不应让同机其他用户可读。
	path, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config.json 权限应为 0600，实际 %v", perm)
	}
}

// TestParseScutilProxy 覆盖 macOS 系统代理输出的解析优先级。
func TestParseScutilProxy(t *testing.T) {
	// HTTPS 优先于 HTTP。
	out := `<dictionary> {
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : 10.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 10809
  HTTPSProxy : 127.0.0.1
}`
	if got := parseScutilProxy(out); got != "http://127.0.0.1:10809" {
		t.Errorf("应优先取 HTTPS 代理: %q", got)
	}

	// 仅 SOCKS 时取 socks5。
	out = `<dictionary> {
  HTTPEnable : 0
  HTTPSEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`
	if got := parseScutilProxy(out); got != "socks5://127.0.0.1:1080" {
		t.Errorf("应取 SOCKS 代理: %q", got)
	}

	// 全部关闭：无代理。
	out = `<dictionary> {
  HTTPEnable : 0
  HTTPSEnable : 0
  SOCKSEnable : 0
}`
	if got := parseScutilProxy(out); got != "" {
		t.Errorf("代理均未启用时应返回空: %q", got)
	}

	// 端口非法：忽略该条。
	out = `<dictionary> {
  HTTPSEnable : 1
  HTTPSPort : not-a-port
  HTTPSProxy : 127.0.0.1
}`
	if got := parseScutilProxy(out); got != "" {
		t.Errorf("端口非法时应返回空: %q", got)
	}
}

// TestParseWindowsProxy 覆盖 Windows 代理设置的各种书写形式。
func TestParseWindowsProxy(t *testing.T) {
	// 未启用。
	if got := parseWindowsProxy(false, "127.0.0.1:10809", "", ""); got != "" {
		t.Errorf("未启用时应返回空: %q", got)
	}
	// 仅 PAC，不猜地址。
	if got := parseWindowsProxy(true, "", "http://pac/proxy.pac", ""); got != "" {
		t.Errorf("仅有 PAC 时不应猜代理地址: %q", got)
	}
	// 单一地址。
	if got := parseWindowsProxy(true, "127.0.0.1:10809", "", ""); got != "http://127.0.0.1:10809" {
		t.Errorf("单一地址解析错误: %q", got)
	}
	// 分协议形式：https 优先。
	got := parseWindowsProxy(true, "http=10.0.0.1:8080;https=10.0.0.2:8443", "", "")
	if got != "http://10.0.0.2:8443" {
		t.Errorf("分协议形式应优先 https: %q", got)
	}
	// socks 归一化为 socks5。
	got = parseWindowsProxy(true, "socks=127.0.0.1:1080", "", "")
	if got != "socks5://127.0.0.1:1080" {
		t.Errorf("socks 应归一化为 socks5: %q", got)
	}
}

// TestWriteNetscapeCookieFile 校验导出格式符合 yt-dlp 的解析要求：
// 7 个制表符分隔字段、HttpOnly 前缀、expires 为十进制整数。
func TestWriteNetscapeCookieFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.txt")
	cookies := []Cookie{
		{Domain: ".youtube.com", Path: "/", Name: "SID", Value: "secret",
			Secure: true, HttpOnly: true, ExpiresAt: time.Unix(1789265837, 0)},
		{Domain: ".youtube.com", Path: "/", Name: "PREF", Value: "hl=en", Secure: true},
		// 值中含制表符与换行，必须被替换以免破坏列结构。
		{Domain: "example.com", Path: "", Name: "WEIRD", Value: "a\tb\nc"},
		// 缺域名或名称的条目应被跳过。
		{Domain: "", Path: "/", Name: "NODOMAIN", Value: "x"},
	}
	if err := WriteNetscapeCookieFile(path, cookies); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "# Netscape HTTP Cookie File") {
		t.Errorf("缺少 Netscape 文件头:\n%s", text)
	}

	lines := []string{}
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		// 空行与被注释的文件头不算 Cookie 行（yt-dlp 会忽略它们）。
		if strings.TrimSpace(l) == "" {
			continue
		}
		if strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "#HttpOnly_") {
			continue
		}
		lines = append(lines, l)
	}
	if len(lines) != 3 {
		t.Fatalf("应写出 3 条 Cookie（跳过缺域名的），实际 %d:\n%s", len(lines), text)
	}

	// HttpOnly 条目必须带前缀，否则会被 yt-dlp 当注释丢弃。
	if !strings.HasPrefix(lines[0], "#HttpOnly_.youtube.com\t") {
		t.Errorf("HttpOnly Cookie 缺少 #HttpOnly_ 前缀: %q", lines[0])
	}
	for _, l := range lines {
		fields := strings.Split(l, "\t")
		if len(fields) != 7 {
			t.Errorf("每行必须恰好 7 个制表符分隔字段，实际 %d: %q", len(fields), l)
		}
	}
	// 清理了值的换行与制表符。
	if strings.Contains(text, "a\tb") {
		t.Errorf("值中的制表符应被替换，避免破坏列结构:\n%s", text)
	}
	if strings.Contains(text, "\nc") {
		t.Errorf("值中的换行应被替换:\n%s", text)
	}
}

// TestSaveCookies 覆盖保存与状态统计。
func TestSaveCookies(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	if _, err := SaveCookies(nil); err == nil {
		t.Fatal("空 Cookie 列表应返回错误")
	}

	n, err := SaveCookies([]Cookie{
		{Domain: ".youtube.com", Path: "/", Name: "SID", Value: "v", HttpOnly: true},
		{Domain: ".bilibili.com", Path: "/", Name: "SESSDATA", Value: "v"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("应保存 2 条，实际 %d", n)
	}

	info := CookiesStatus()
	if !info.Exists || info.Count != 2 {
		t.Errorf("状态统计错误: %+v", info)
	}
	if info.SavedAt == "" {
		t.Error("应记录保存时间")
	}

	if err := DeleteCookies(); err != nil {
		t.Fatal(err)
	}
	if info := CookiesStatus(); info.Exists {
		t.Errorf("删除后不应存在: %+v", info)
	}
	// 重复删除不应报错。
	if err := DeleteCookies(); err != nil {
		t.Errorf("重复删除不应报错: %v", err)
	}
}
