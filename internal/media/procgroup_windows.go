//go:build windows

package media

import (
	"os/exec"
	"strconv"
	"syscall"
)

// setProcGroup 让命令自成进程组，配合 killProcGroup 终止整棵进程树。
//
// 必要性同非 Windows 平台：yt-dlp 会用 ffmpeg 子进程下载 / 合并分片，
// 只杀 yt-dlp 会漏下这些子进程，残留的 CDN 连接会拖慢后续投屏。
func setProcGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// killProcGroup 用 taskkill 终止进程树。
// Windows 没有进程组信号语义，taskkill /T 是终止子进程树的标准做法。
func killProcGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	if err := exec.Command("taskkill", "/T", "/F", "/PID", pid).Run(); err != nil {
		_ = cmd.Process.Kill()
	}
}
