package main

import (
	"AnyDLNA/internal/browser"
	"AnyDLNA/internal/media"
	"context"
	"errors"
	"strings"
	"time"
)

// ---------- 设置：代理与 Cookies ----------

// DiagLogPath 返回诊断日志文件路径（出问题把这个文件发来分析）。
func (a *App) DiagLogPath() string {
	path, err := media.DiagLogPath()
	if err != nil {
		return ""
	}
	return path
}

// GetConfig 返回当前设置。
func (a *App) GetConfig() *media.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	cfg := a.cfg
	return &cfg
}

// SetConfig 保存设置并持久化；保存后立即对后续解析/投屏生效。
func (a *App) SetConfig(cfg media.Config) error {
	cfg = cfg.Normalize()
	if err := media.SaveConfig(cfg); err != nil {
		return err
	}
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()
	return nil
}

// SystemProxy 返回当前检测到的系统代理地址，供设置页展示。
func (a *App) SystemProxy() string { return media.DetectSystemProxy() }

// TestProxy 验证给定设置中代理的连通性，返回可直接展示的结论。
func (a *App) TestProxy(cfg media.Config) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	return media.TestProxy(ctx, cfg.Normalize().ResolveOptions())
}

// BrowserInfo 描述本机可用于登录的浏览器。
type BrowserInfo struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// LoginBrowserInfo 描述登录浏览器的当前状态，供设置页展示。
type LoginBrowserInfo struct {
	// Supported 表示本机是否检测到可用浏览器。
	Supported bool `json:"supported"`
	// Running 表示应用启动的登录浏览器是否正在运行。
	Running bool `json:"running"`
	// Executable 是实际使用的浏览器可执行文件。
	Executable string `json:"executable"`
	// Browsers 是本机检测到的候选浏览器列表。
	Browsers []BrowserInfo `json:"browsers"`
}

// LoginBrowserStatus 返回登录浏览器的可用性与运行状态。
func (a *App) LoginBrowserStatus() *LoginBrowserInfo {
	info := &LoginBrowserInfo{}
	for _, b := range browser.Available() {
		info.Browsers = append(info.Browsers, BrowserInfo{Name: b.Name, Path: b.Path})
	}
	info.Supported = len(info.Browsers) > 0
	if a.browserMgr != nil {
		info.Running = a.browserMgr.Running()
		info.Executable = a.browserMgr.Executable()
	}
	return info
}

// OpenLoginBrowser 启动应用专用的浏览器窗口访问指定站点供用户登录。
// browserName 指定用哪台浏览器（LoginBrowserStatus 返回的名称）；空串或
// 未匹配时用检测到的第一个候选。浏览器使用独立 profile，不读写用户日常
// 浏览器的数据；登录状态会被保留，下次无需重复登录。用户登录完成后需
// 调用 SaveBrowserCookies 取回 Cookies。
//
// 该方法只负责启动，不阻塞等待登录。
func (a *App) OpenLoginBrowser(rawURL, browserName string) error {
	if a.browserMgr == nil {
		return errors.New("登录浏览器未初始化")
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		rawURL = "https://www.youtube.com"
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	exe := ""
	if browserName != "" {
		for _, b := range browser.Available() {
			if b.Name == browserName {
				exe = b.Path
				break
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, err := a.browserMgr.Start(ctx, rawURL, exe)
	return err
}

// CloseLoginBrowser 关闭应用启动的登录浏览器。
func (a *App) CloseLoginBrowser() {
	if a.browserMgr != nil {
		a.browserMgr.Close()
	}
}

// SaveBrowserCookies 从登录浏览器读回全部 Cookie（含 HttpOnly）并保存为
// yt-dlp 可读的文件。应在用户于浏览器中完成登录后调用。
func (a *App) SaveBrowserCookies() (*media.CookiesInfo, error) {
	if a.browserMgr == nil {
		return nil, errors.New("登录浏览器未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cookies, err := a.browserMgr.Cookies(ctx)
	if err != nil {
		return nil, err
	}
	n, err := media.SaveCookies(toMediaCookies(cookies))
	if err != nil {
		return nil, err
	}
	a.logf("已从登录浏览器保存 %d 条 Cookies", n)

	status := media.CookiesStatus()
	return &status, nil
}

// toMediaCookies 把浏览器读出的 Cookie 转换为持久化结构。
func toMediaCookies(in []browser.Cookie) []media.Cookie {
	out := make([]media.Cookie, 0, len(in))
	for _, c := range in {
		out = append(out, media.Cookie{
			Domain:    c.Domain,
			Path:      c.Path,
			Name:      c.Name,
			Value:     c.Value,
			Secure:    c.Secure,
			HttpOnly:  c.HttpOnly,
			ExpiresAt: c.ExpiresAt,
		})
	}
	return out
}

// GetCookieStatus 返回登录浏览器导出 Cookies 的保存状态，供设置页展示。
func (a *App) GetCookieStatus() *media.CookiesInfo {
	info := media.CookiesStatus()
	return &info
}

// ClearCookies 清除登录浏览器导出的 Cookies 文件（用于退出登录态）。
func (a *App) ClearCookies() error { return media.DeleteCookies() }

// ResetLoginBrowser 清除登录浏览器的独立 profile（彻底退出所有站点登录）。
func (a *App) ResetLoginBrowser() error {
	if a.browserMgr == nil {
		return errors.New("登录浏览器未初始化")
	}
	return a.browserMgr.Reset()
}
