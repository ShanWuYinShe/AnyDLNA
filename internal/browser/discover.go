package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// EnvBrowserPath 是用户显式指定浏览器可执行文件的环境变量名。
const EnvBrowserPath = "ANYDLNA_BROWSER_PATH"

// envBrowserPath 返回用户通过环境变量指定的浏览器路径。
func envBrowserPath() string { return os.Getenv(EnvBrowserPath) }

// candidate 是一个候选浏览器可执行文件。
type candidate struct {
	Name string
	Path string
}

// candidates 返回本机可用的 Chromium 系浏览器，按偏好排序：
// 环境变量指定 > PATH 中的常见命令 > 各平台已知安装路径。
func candidates() []candidate {
	var out []candidate
	seen := map[string]bool{}
	add := func(name, path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, candidate{Name: name, Path: path})
	}

	// 用户显式指定时优先（便于自定义安装位置与测试）。
	if custom := envBrowserPath(); custom != "" {
		add(filepath.Base(custom), custom)
	}

	// PATH 中的命令名（覆盖包管理器安装的浏览器）。
	for _, name := range commandNames() {
		if p, err := exec.LookPath(name); err == nil {
			add(name, p)
		}
	}

	// 各平台已知安装路径。
	for _, p := range platformBrowserPaths() {
		add(filepath.Base(p), p)
	}
	for _, pattern := range platformBrowserGlobs() {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, m := range matches {
			add(filepath.Base(m), m)
		}
	}
	return out
}

// commandNames 返回 PATH 中的候选命令名。
func commandNames() []string {
	names := []string{
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
		"brave-browser", "microsoft-edge", "vivaldi", "opera",
	}
	if runtime.GOOS == "darwin" {
		names = append(names, "chrome", "brave")
	}
	if runtime.GOOS == "windows" {
		names = append(names, "chrome.exe", "msedge.exe", "brave.exe")
	}
	return names
}
