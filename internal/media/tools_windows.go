//go:build windows

package media

import (
	"os"
	"path/filepath"
)

// executableSuffix 在 Windows 上可执行文件带 .exe 扩展名。
const executableSuffix = ".exe"

// toolSearchDirs 返回 PATH 之外需要额外搜索的工具安装目录。
// Windows 的 PATH 由系统统一设置，通常不会出现 GUI 应用取不到的问题，
// 这里额外覆盖包管理器把可执行文件软链到的目录。
func toolSearchDirs() []string {
	var dirs []string
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		dirs = append(dirs,
			filepath.Join(local, "Microsoft", "WinGet", "Links"), // winget
		)
	}
	if profile := os.Getenv("USERPROFILE"); profile != "" {
		dirs = append(dirs,
			filepath.Join(profile, "scoop", "shims"), // scoop
			filepath.Join(profile, ".local", "bin"),
		)
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		dirs = append(dirs, filepath.Join(appData, "Python", "Scripts")) // pip --user
	}
	return dirs
}

// installHint 返回在 Windows 上安装该工具的常见方式。
func installHint(name string) string {
	switch name {
	case "yt-dlp":
		return "winget install yt-dlp，或 pip install yt-dlp"
	case "ffmpeg", "ffprobe":
		return "winget install ffmpeg（含 ffprobe）"
	default:
		return ""
	}
}
