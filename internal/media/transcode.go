package media

import (
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
)

// Transcoder 管理一路实时转码的 ffmpeg 进程。
// 同一时间至多一个进程；更换起始位置时旧进程被终止，新进程在下一次拉流时启动。
type Transcoder struct {
	mu     sync.Mutex
	path   string // 源文件路径
	offset int64  // 转码起始位置（毫秒），供下一次启动使用
	cmd    *exec.Cmd
}

// NewTranscoder 创建针对源文件的转码器。
func NewTranscoder(path string) *Transcoder {
	return &Transcoder{path: path}
}

// RestartAt 终止当前进程并记录新的起始位置，等待下一次拉流时重新启动。
func (t *Transcoder) RestartAt(seconds float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if seconds < 0 {
		seconds = 0
	}
	t.offset = int64(seconds * 1000)
	t.stopLocked()
}

// Stop 终止转码进程（若在运行）。
func (t *Transcoder) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopLocked()
}

func (t *Transcoder) stopLocked() {
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
		_, _ = t.cmd.Process.Wait()
		t.cmd = nil
	}
}

// StreamTo 启动（或复用）ffmpeg 进程，把 MPEG-TS 流写入 w。
// 返回的 cancel 必须在连接关闭时调用，用于终止转码进程。
func (t *Transcoder) StreamTo(w io.Writer) (cancel func(), err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	ssSec := float64(t.offset) / 1000
	t.offset = 0

	args := []string{"-hide_banner", "-loglevel", "error"}
	if ssSec > 0 {
		args = append(args, "-ss", strconv.FormatFloat(ssSec, 'f', 2, 64))
	}
	args = append(args,
		"-i", t.path,
		"-map", "0:v:0", "-map", "0:a:0?",
		"-sn", "-dn",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
		"-maxrate", "4M", "-bufsize", "8M", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "192k", "-ac", "2",
		"-f", "mpegts", "pipe:1",
	)
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = w
	stderr := new(limitBuffer)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return func() {}, fmt.Errorf("启动 ffmpeg 失败: %w", err)
	}
	t.cmd = cmd

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	cancel = func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.cmd == cmd && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		if t.cmd == cmd {
			t.cmd = nil
		}
	}
	return cancel, nil
}

// limitBuffer 截断保存 ffmpeg stderr，仅用于出错诊断。
type limitBuffer struct {
	buf []byte
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	const max = 8 * 1024
	if room := max - len(b.buf); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf = append(b.buf, p...)
	}
	return len(p), nil
}

func (b *limitBuffer) String() string { return string(b.buf) }
