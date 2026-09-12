package media

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// 代理模式：只作用于在线视频的解析与拉流，不影响设备发现与投屏流。
const (
	// ProxyModeSystem 跟随操作系统代理设置。
	ProxyModeSystem = "system"
	// ProxyModeManual 使用用户填写的代理地址。
	ProxyModeManual = "manual"
	// ProxyModeNone 直连，不使用代理。
	ProxyModeNone = "none"
)

// Cookie 模式：决定 yt-dlp 如何获得站点登录态。
const (
	// CookieModeNone 不使用 Cookies。
	CookieModeNone = "none"
	// CookieModeLoginBrowser 通过应用启动的独立浏览器登录后导出 Cookies。
	CookieModeLoginBrowser = "loginbrowser"
	// CookieModeBrowser 从本机已安装浏览器的登录态读取 Cookies。
	CookieModeBrowser = "browser"
)

// Options 是一次在线视频访问所需的生效参数（由 Config 解析而来）。
type Options struct {
	ProxyMode     string // system / manual / none
	Proxy         string // manual 模式下的代理地址
	CookieFile    string // 登录浏览器导出的 Netscape 格式 Cookies 文件；空为不使用
	CookieBrowser string // 读取登录态的本机浏览器名；空为不使用
}

// Config 是应用的可持久化配置。
// JSON 字段采用 camelCase，与前端及本项目其他 DTO 的约定一致。
type Config struct {
	ProxyMode     string `json:"proxyMode"`     // system / manual / none
	ProxyURL      string `json:"proxyUrl"`      // manual 模式下的代理地址
	CookieMode    string `json:"cookieMode"`    // none / loginbrowser / browser
	CookieBrowser string `json:"cookieBrowser"` // browser 模式下的浏览器名
}

// ConfigPath 返回配置文件路径（随系统用户配置目录）。
func ConfigPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// DataDir 返回应用数据目录，用于存放配置与登录浏览器导出的 Cookies。
func DataDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "AnyDLNA"), nil
}

// CookieFilePath 返回登录浏览器导出的 Netscape 格式 Cookies 文件路径。
func CookieFilePath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cookies.txt"), nil
}

// DefaultConfig 返回首次运行时的默认配置：跟随系统代理，不使用 Cookies。
func DefaultConfig() Config {
	return Config{ProxyMode: ProxyModeSystem, CookieMode: CookieModeNone}
}

// LoadConfig 读取配置；文件不存在或损坏时返回默认配置，不阻塞启动。
// 兼容早期仅有 proxy / cookie_browser 字段的配置格式。
func LoadConfig() Config {
	path, err := ConfigPath()
	if err != nil {
		return DefaultConfig()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return DefaultConfig()
	}

	// raw 同时接受新格式（camelCase）与早期格式（snake_case / 仅有
	// proxy 与 cookie_browser），确保老用户升级后配置不丢失。
	var raw struct {
		ProxyMode string `json:"proxyMode"`
		ProxyURL  string `json:"proxyUrl"`
		// 下列为早期 snake_case 字段。
		LegacyProxyMode  string `json:"proxy_mode"`
		LegacyProxyURL   string `json:"proxy_url"`
		CookieMode       string `json:"cookieMode"`
		CookieBrowser    string `json:"cookieBrowser"`
		LegacyCookieMode string `json:"cookie_mode"`
		// 最早期的字段：只有代理地址与浏览器名。
		LegacyProxy         string `json:"proxy"`
		LegacyCookieBrowser string `json:"cookie_browser"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return DefaultConfig()
	}

	// 同名字段优先取新格式，缺失时回退到旧格式。
	firstNonEmpty := func(values ...string) string {
		for _, v := range values {
			if strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
	c := Config{
		ProxyMode:     firstNonEmpty(raw.ProxyMode, raw.LegacyProxyMode),
		ProxyURL:      firstNonEmpty(raw.ProxyURL, raw.LegacyProxyURL),
		CookieMode:    firstNonEmpty(raw.CookieMode, raw.LegacyCookieMode),
		CookieBrowser: firstNonEmpty(raw.CookieBrowser, raw.LegacyCookieBrowser),
	}
	// 旧格式迁移：原 proxy 字段非空视为手动代理，空则按新默认跟随系统。
	if c.ProxyMode == "" {
		if strings.TrimSpace(raw.LegacyProxy) != "" {
			c.ProxyMode, c.ProxyURL = ProxyModeManual, raw.LegacyProxy
		} else {
			c.ProxyMode = ProxyModeSystem
		}
	}
	// 旧格式迁移：原 cookie_browser 字段对应 browser 模式。
	if c.CookieMode == "" {
		if strings.TrimSpace(c.CookieBrowser) != "" {
			c.CookieMode = CookieModeBrowser
		} else {
			c.CookieMode = CookieModeNone
		}
	}
	return c.Normalize()
}

// Normalize 返回校验并补齐默认值后的配置副本。
func (c Config) Normalize() Config {
	if !ValidProxyMode(c.ProxyMode) {
		c.ProxyMode = DefaultConfig().ProxyMode
	}
	c.ProxyURL = strings.TrimSpace(c.ProxyURL)
	if c.ProxyMode != ProxyModeManual {
		c.ProxyURL = "" // 非手动模式不保留地址，避免切换模式后残留旧值。
	}
	if !ValidCookieMode(c.CookieMode) {
		c.CookieMode = CookieModeNone
	}
	c.CookieBrowser = strings.ToLower(strings.TrimSpace(c.CookieBrowser))
	if c.CookieMode != CookieModeBrowser {
		c.CookieBrowser = ""
	}
	return c
}

// ValidProxyMode 报告代理模式是否为已知取值。
func ValidProxyMode(mode string) bool {
	switch mode {
	case ProxyModeSystem, ProxyModeManual, ProxyModeNone:
		return true
	default:
		return false
	}
}

// ValidCookieMode 报告 Cookies 模式是否为已知取值。
func ValidCookieMode(mode string) bool {
	switch mode {
	case CookieModeNone, CookieModeLoginBrowser, CookieModeBrowser:
		return true
	default:
		return false
	}
}

// SaveConfig 写入配置文件。
func SaveConfig(c Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c.Normalize(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
