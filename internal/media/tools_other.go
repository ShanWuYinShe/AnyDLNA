//go:build !darwin && !linux && !windows

package media

import (
	"os"
	"path/filepath"
)

// executableSuffix 其他平台按无扩展名处理。
const executableSuffix = ""

// toolSearchDirs 返回 PATH 之外需要额外搜索的工具安装目录。
// 其他平台仅补充用户级安装目录，主要仍依赖 PATH。
func toolSearchDirs() []string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "bin"),
	}
}

// installHint 其他平台不给出具体安装命令。
func installHint(name string) string { return "" }
