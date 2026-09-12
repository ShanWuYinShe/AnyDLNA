//go:build darwin

package media

import (
	"os"
	"path/filepath"
)

// executableSuffix 在 macOS 上可执行文件无扩展名。
const executableSuffix = ""

// toolSearchDirs 返回 PATH 之外需要额外搜索的工具安装目录。
//
// 从 Finder / Dock 启动的 GUI 应用不继承 shell 的 PATH，
// 这里覆盖 macOS 上常见的包管理器安装位置：
// Homebrew（Apple Silicon 在 /opt/homebrew，Intel 在 /usr/local）、MacPorts，
// 以及用户级安装目录。
func toolSearchDirs() []string {
	home, _ := os.UserHomeDir()
	dirs := []string{
		"/opt/homebrew/bin", // Homebrew (Apple Silicon)
		"/usr/local/bin",    // Homebrew (Intel) / 手动安装
		"/opt/local/bin",    // MacPorts
		"/usr/local/sbin",
	}
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "bin"),
			// pipx / Python 用户级安装（yt-dlp 的另一种常见安装方式）
			filepath.Join(home, "Library", "Python", "bin"),
		)
	}
	return dirs
}

// installHint 返回在 macOS 上安装该工具的命令。
func installHint(name string) string {
	switch name {
	case "yt-dlp":
		return "brew install yt-dlp"
	case "ffmpeg", "ffprobe":
		return "brew install ffmpeg（含 ffprobe）"
	default:
		return ""
	}
}
