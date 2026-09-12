package media

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// 外部工具的环境变量覆盖名。用户自定义安装位置时可显式指定，
// 优先级高于自动搜索。
const (
	envYtDlpPath   = "ANYDLNA_YTDLP_PATH"
	envFFmpegPath  = "ANYDLNA_FFMPEG_PATH"
	envFFprobePath = "ANYDLNA_FFPROBE_PATH"
)

// toolOverrideEnv 返回工具对应的路径覆盖环境变量名。
func toolOverrideEnv(name string) string {
	switch name {
	case "yt-dlp":
		return envYtDlpPath
	case "ffmpeg":
		return envFFmpegPath
	case "ffprobe":
		return envFFprobePath
	default:
		return ""
	}
}

// ResolveTool 解析外部工具的可执行文件路径。
//
// 不能只依赖 exec.LookPath：macOS 上从 Finder / Dock 启动的 GUI 应用
// 不继承 shell 的 PATH（launchctl 默认也未设置），此时 PATH 只有
// /usr/bin:/bin:/usr/sbin:/sbin，通过 Homebrew 安装的 yt-dlp、ffmpeg
// 一律找不到——尽管它们在终端里完全可用。
// 因此这里在 PATH 之外再搜索各平台工具的常见安装目录。
//
// 查找顺序：环境变量覆盖 > PATH > 常见安装目录。
// 返回 ok=false 时，path 为空。
func ResolveTool(name string) (path string, ok bool) {
	if env := toolOverrideEnv(name); env != "" {
		if custom := strings.TrimSpace(os.Getenv(env)); custom != "" {
			if isExecutableFile(custom) {
				return custom, true
			}
			// 显式指定但不可用时不静默回退，交由调用方报错提示。
			return "", false
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, true
	}
	for _, dir := range toolSearchDirs() {
		candidate := filepath.Join(dir, name)
		if isExecutableFile(candidate) {
			return candidate, true
		}
		if executableSuffix != "" {
			if candidate = filepath.Join(dir, name+executableSuffix); isExecutableFile(candidate) {
				return candidate, true
			}
		}
	}
	return "", false
}

// isExecutableFile 报告路径是否存在且为可执行文件（而非目录）。
func isExecutableFile(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	// Windows 不依赖权限位判断可执行性。
	if executableSuffix != "" {
		return true
	}
	return st.Mode().Perm()&0o111 != 0
}

// toolLookupPath 返回可执行文件路径；未找到时返回原名，
// 让 exec 自行给出「找不到文件」的错误。
func toolLookupPath(name string) string {
	if p, ok := ResolveTool(name); ok {
		return p
	}
	return name
}

// toolCmd 构造调用外部工具的命令。
//
// 关键是给子进程补上 PATH：yt-dlp 合并分离的音视频流时会自行调用 ffmpeg，
// 若沿用 GUI 应用的精简 PATH，即使我们自己找到了 yt-dlp，它也会因为找不到
// ffmpeg 而失败。
func toolCmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(toolLookupPath(name), args...)
	cmd.Env = ToolEnv()
	return cmd
}

// toolCmdContext 与 toolCmd 相同，但绑定上下文。
func toolCmdContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, toolLookupPath(name), args...)
	cmd.Env = ToolEnv()
	return cmd
}

// ToolEnv 返回供外部工具使用的环境变量：在继承当前环境的基础上，
// 把工具的常见安装目录追加进 PATH（已存在的目录才会加入）。
func ToolEnv() []string {
	env := os.Environ()
	extra := existingToolDirs()
	if len(extra) == 0 {
		return env
	}
	sep := string(os.PathListSeparator)
	suffix := strings.Join(extra, sep)

	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			current := strings.TrimPrefix(kv, "PATH=")
			if current == "" {
				env[i] = "PATH=" + suffix
			} else {
				env[i] = "PATH=" + current + sep + suffix
			}
			return env
		}
	}
	return append(env, "PATH="+suffix)
}

// existingToolDirs 返回真实存在的工具安装目录（已去重）。
func existingToolDirs() []string {
	var out []string
	seen := map[string]bool{}
	for _, dir := range toolSearchDirs() {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			out = append(out, dir)
		}
	}
	return out
}

// MissingToolError 返回外部工具缺失时的可操作错误。
// name 取 "yt-dlp" / "ffmpeg" / "ffprobe"。
//
// 提示中会说明已搜索的位置——GUI 应用找不到工具通常不是「没装」，
// 而是没有继承 shell 的 PATH，明确列出搜索范围能避免用户误判。
func MissingToolError(name string) error {
	msg := "未找到 " + name
	if dirs := existingToolDirs(); len(dirs) > 0 {
		msg += "（已搜索系统 PATH 与 " + strings.Join(dirs, "、") + "）"
	} else {
		msg += "（已搜索系统 PATH）"
	}
	if hint := installHint(name); hint != "" {
		msg += "。安装：" + hint
	}
	if override := toolOverrideEnv(name); override != "" {
		msg += "；若已安装在其他位置，可用环境变量 " + override + " 指定完整路径"
	}
	return errors.New(msg)
}
