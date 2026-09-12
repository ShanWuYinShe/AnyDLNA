package media

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ResolveOptions 把持久化配置解析为一次在线视频访问所需的生效参数。
// 代理模式 manual 用填写地址，none 显式直连，system 交给 yt-dlp 自行
// 读取环境变量与操作系统代理设置。
func (c Config) ResolveOptions() Options {
	c = c.Normalize()
	opts := Options{ProxyMode: c.ProxyMode, Proxy: c.ProxyURL}

	switch c.CookieMode {
	case CookieModeBrowser:
		opts.CookieBrowser = c.CookieBrowser
	case CookieModeLoginBrowser:
		// 仅在导出文件存在且非空时启用，否则等同不使用 Cookies。
		if path, err := CookieFilePath(); err == nil {
			if st, statErr := os.Stat(path); statErr == nil && st.Size() > 0 {
				opts.CookieFile = path
			}
		}
	}
	return opts
}

// DetectSystemProxy 返回当前实际生效的代理地址（供设置页展示与测试）：
// 优先代理环境变量，其次操作系统网络代理；都没有时返回空串。
func DetectSystemProxy() string {
	if proxy := proxyFromEnv(); proxy != "" {
		return proxy
	}
	return systemProxy()
}

// proxyFromEnv 读取标准代理环境变量（大小写两种形式）。
func proxyFromEnv() string {
	for _, key := range []string{
		"HTTPS_PROXY", "https_proxy",
		"HTTP_PROXY", "http_proxy",
		"ALL_PROXY", "all_proxy",
	} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

// TestProxy 验证代理配置的连通性，返回可直接展示给用户的结论。
// 仅在实际配置了代理时发起请求：直连或系统未配置代理时不做外部探测，
// 因为访问外部检测端点在某些网络下本就可能失败，测了反而误导用户。
func TestProxy(ctx context.Context, opts Options) (string, error) {
	proxy := strings.TrimSpace(opts.Proxy)
	switch opts.ProxyMode {
	case ProxyModeNone:
		return "当前为直连模式，未使用代理", nil
	case ProxyModeSystem:
		proxy = DetectSystemProxy()
		if proxy == "" {
			return "未检测到系统代理或代理环境变量，yt-dlp 将直连访问", nil
		}
	case ProxyModeManual:
		if proxy == "" {
			return "", fmt.Errorf("请先填写代理地址（示例：http://127.0.0.1:10809）")
		}
	default:
		return "", fmt.Errorf("未知的代理模式: %s", opts.ProxyMode)
	}

	pu, err := url.Parse(proxy)
	if err != nil || pu.Scheme == "" || pu.Host == "" {
		return "", fmt.Errorf("代理地址无效（示例：http://127.0.0.1:10809）")
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(pu), // 仅此请求走代理，不读环境变量
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.gstatic.com/generate_204", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("代理不可用: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("代理可用但出口异常: HTTP %d", resp.StatusCode)
	}
	return "代理可用：" + proxy, nil
}

// proxySetting 描述一种代理类型的配置项，用于解析各类系统设置格式。
type proxySetting struct {
	enable bool
	host   string
	port   string
	scheme string
}

// buildProxyURL 根据开关、主机与端口拼出代理 URL；信息不全时返回空串。
func buildProxyURL(enable bool, host, port, scheme string) string {
	if !enable {
		return ""
	}
	host, port = strings.TrimSpace(host), strings.TrimSpace(port)
	if host == "" || port == "" {
		return ""
	}
	if _, err := strconv.Atoi(port); err != nil {
		return ""
	}
	return fmt.Sprintf("%s://%s:%s", scheme, host, port)
}

// parseScutilProxy 解析 macOS `scutil --proxy` 的输出。
// 优先级 HTTPS > HTTP > SOCKS，与系统设置的常用顺序一致。
func parseScutilProxy(output string) string {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		key, val, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		if key = strings.TrimSpace(key); key != "" {
			values[key] = strings.TrimSpace(val)
		}
	}
	for _, candidate := range []struct{ enable, host, port, scheme string }{
		{"HTTPSEnable", "HTTPSProxy", "HTTPSPort", "http"},
		{"HTTPEnable", "HTTPProxy", "HTTPPort", "http"},
		{"SOCKSEnable", "SOCKSProxy", "SOCKSPort", "socks5"},
	} {
		if v := buildProxyURL(values[candidate.enable] == "1",
			values[candidate.host], values[candidate.port], candidate.scheme); v != "" {
			return v
		}
	}
	return ""
}

// scutilProxy 在 macOS 上执行 `scutil --proxy`；其他平台或无该命令时返回空串。
func scutilProxy() string {
	path, err := exec.LookPath("scutil")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--proxy").Output()
	if err != nil {
		return ""
	}
	return parseScutilProxy(string(out))
}

// gnomeProxy 在 Linux（GNOME/多数桌面）上读取 gsettings 中的代理配置。
func gnomeProxy() string {
	path, err := exec.LookPath("gsettings")
	if err != nil {
		return ""
	}
	get := func(schema, key string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, path, "get", schema, key).Output()
		if err != nil {
			return ""
		}
		return strings.Trim(strings.TrimSpace(string(out)), "'")
	}

	mode := get("org.gnome.system.proxy", "mode")
	if mode == "" || mode == "none" {
		return ""
	}
	type entry struct{ schema, key, scheme string }
	for _, e := range []entry{
		{"org.gnome.system.proxy.https", "host", "http"},
		{"org.gnome.system.proxy.http", "host", "http"},
		{"org.gnome.system.proxy.socks", "host", "socks5"},
	} {
		host := get(e.schema, e.key)
		port := get(e.schema, "port")
		if v := buildProxyURL(true, host, port, e.scheme); v != "" {
			return v
		}
	}
	return ""
}

// registryProxy 在 Windows 上读取当前用户的 Internet 代理设置。
// 由各平台的 systemProxy 调用（见 proxy_windows.go）。
func parseWindowsProxy(enabled bool, server, pacURL, bypass string) string {
	if !enabled {
		return ""
	}
	server = strings.TrimSpace(server)
	if server == "" {
		return "" // 仅有 PAC 脚本时不猜测代理地址。
	}
	// ProxyServer 可能是 "host:port" 或 "http=host:port;https=host:port" 形式。
	if !strings.Contains(server, "=") {
		if !strings.Contains(server, "://") {
			server = "http://" + server
		}
		return server
	}

	// 按协议收集 "host:port"，随后按优先级取第一条。
	byScheme := map[string]string{}
	for _, part := range strings.Split(server, ";") {
		scheme, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		scheme = strings.ToLower(strings.TrimSpace(scheme))
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		// 用户在 Windows 设置里可能已写成带协议的形式，先剥掉协议再统一补。
		if _, hostport, ok := strings.Cut(value, "://"); ok {
			value = hostport
		}
		byScheme[scheme] = value
	}
	// https 代理在 URL 中同样使用 http:// 前缀；socks 写作 socks5://。
	for _, candidate := range []struct{ key, prefix string }{
		{"https", "http"},
		{"http", "http"},
		{"socks5", "socks5"},
		{"socks", "socks5"},
	} {
		if v := byScheme[candidate.key]; v != "" {
			return candidate.prefix + "://" + v
		}
	}
	return ""
}
