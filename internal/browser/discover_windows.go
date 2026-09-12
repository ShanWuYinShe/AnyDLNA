//go:build windows

package browser

import "os"

// platformBrowserPaths 返回 Windows 上常见浏览器的可执行文件路径。
func platformBrowserPaths() []string {
	var out []string
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
		base := os.Getenv(env)
		if base == "" {
			continue
		}
		out = append(out,
			base+`\Google\Chrome\Application\chrome.exe`,
			base+`\Microsoft\Edge\Application\msedge.exe`,
			base+`\BraveSoftware\Brave-Browser\Application\brave.exe`,
			base+`\Chromium\Application\chrome.exe`,
		)
	}
	return out
}

// platformBrowserGlobs Windows 下无版本化目录需要匹配。
func platformBrowserGlobs() []string { return nil }
