//go:build linux

package media

import (
	"os"
	"path/filepath"
)

// executableSuffix 在 Linux 上可执行文件无扩展名。
const executableSuffix = ""

// toolSearchDirs 返回 PATH 之外需要额外搜索的工具安装目录。
// 覆盖各发行版与包管理器的常见位置，以及用户级安装目录。
func toolSearchDirs() []string {
	home, _ := os.UserHomeDir()
	dirs := []string{
		"/usr/local/bin",
		"/usr/bin",
		"/snap/bin",                    // Snap
		"/var/lib/flatpak/exports/bin", // Flatpak（系统）
	}
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"), // pipx / pip --user
			filepath.Join(home, "bin"),
			filepath.Join(home, ".local", "share", "flatpak", "exports", "bin"),
		)
	}
	return dirs
}

// installHint 返回在 Linux 上安装该工具的常见方式。
func installHint(name string) string {
	switch name {
	case "yt-dlp":
		return "pipx install yt-dlp，或发行版包管理器（如 apt install yt-dlp）"
	case "ffmpeg", "ffprobe":
		return "发行版包管理器（如 apt install ffmpeg，含 ffprobe）"
	default:
		return ""
	}
}
