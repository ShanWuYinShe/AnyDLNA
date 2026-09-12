//go:build darwin

package browser

import "os"

// platformBrowserPaths 返回 macOS 上常见浏览器的可执行文件路径。
func platformBrowserPaths() []string {
	home, _ := os.UserHomeDir()
	return []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Vivaldi.app/Contents/MacOS/Vivaldi",
		"/Applications/Opera.app/Contents/MacOS/Opera",
		// 用户目录下的安装位置。
		home + "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		home + "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		home + "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
	}
}

// platformBrowserGlobs 返回需要通配符匹配的路径。
func platformBrowserGlobs() []string {
	home, _ := os.UserHomeDir()
	return []string{
		home + "/.agent-browser/browsers/*/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
	}
}
