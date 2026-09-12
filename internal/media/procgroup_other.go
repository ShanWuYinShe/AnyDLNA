//go:build !windows

package media

import (
	"os/exec"
	"syscall"
)

// setProcGroup 让命令在独立进程组中运行，从而可以连同其子进程一起终止。
//
// 必要性：yt-dlp 自己会用 ffmpeg 子进程下载 / 合并分片。若只杀 yt-dlp，
// 这些子进程会被 reparent 到 launchd（PPID=1）继续存活，残留的 CDN 连接
// 会一直占着不放——站点通常按 IP 限制并发连接数，于是后续投屏被限速。
// 实测：电视端一次投屏会开多个连接，每次断开都会漏下一个下载用 ffmpeg。
func setProcGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcGroup 终止命令所在的整个进程组。
// 进程（组）已经不存在时不报错，交由调用方继续回收。
func killProcGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// 负号表示「进程组」；setProcGroup 已保证子进程自成组长，pgid == pid。
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
