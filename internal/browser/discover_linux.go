//go:build linux

package browser

// platformBrowserPaths 返回 Linux 上常见浏览器的可执行文件路径。
// 同时依赖 PATH 查找（见 discover.go 的 commandLookup）。
func platformBrowserPaths() []string {
	return []string{
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/usr/bin/microsoft-edge",
		"/usr/bin/brave-browser",
		"/snap/bin/chromium",
		"/var/lib/flatpak/exports/bin/com.google.Chrome",
		"/var/lib/flatpak/exports/bin/org.chromium.Chromium",
	}
}

// platformBrowserGlobs Linux 下无版本化目录需要匹配。
func platformBrowserGlobs() []string { return nil }
