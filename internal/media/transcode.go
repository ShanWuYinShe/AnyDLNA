package media

import (
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// Transcoder 管理一路实时转码进程，把源（本地文件或在线视频）转为 MPEG-TS。
// 本地源由 ffmpeg 直接读取；在线源先经 yt-dlp 解析合并为流，再管道交给 ffmpeg。
// 同一时间至多一路进程；更换起始位置时旧进程被终止，新进程在下一次拉流时启动。
type Transcoder struct {
	mu     sync.Mutex
	path   string    // 本地文件路径；为空表示在线源
	srcURL string    // 在线视频页面/流地址（走 yt-dlp）
	isLive bool      // 直播流（不支持 --download-sections 快进）
	opts   Options   // 在线源的代理与 Cookies 配置
	offset int64     // 转码起始位置（毫秒），供下一次启动使用
	cmd    *exec.Cmd // ffmpeg 进程
	srcCmd *exec.Cmd // yt-dlp 进程（仅在线源）
	done   chan struct{}
	stderr *limitBuffer // 最近一次进程 stderr（诊断用）
}

// NewTranscoder 创建针对本地文件的转码器。
func NewTranscoder(path string) *Transcoder {
	return &Transcoder{path: path}
}

// NewURLTranscoder 创建针对在线视频源的转码器。
func NewURLTranscoder(url string, isLive bool, opts Options) *Transcoder {
	return &Transcoder{srcURL: url, isLive: isLive, opts: opts}
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

// stopLocked 终止 ffmpeg 及其上游 yt-dlp 并等待回收；调用方须持有 t.mu。
func (t *Transcoder) stopLocked() {
	if t.cmd != nil && t.cmd.Process != nil {
		// 先杀 ffmpeg：其 stdin（来自 yt-dlp）关闭后，yt-dlp 也会因写入失败退出。
		_ = t.cmd.Process.Kill()
	}
	if t.srcCmd != nil && t.srcCmd.Process != nil {
		_ = t.srcCmd.Process.Kill()
	}
	// t.done 由 launchLocked 的 goroutine 关闭，是唯一调用 cmd.Wait 的地方；
	// srcCmd 没有后台 goroutine，直接 Wait 回收避免僵尸进程。
	if t.done != nil {
		select {
		case <-t.done:
		case <-time.After(5 * time.Second):
		}
	}
	if t.srcCmd != nil {
		_, _ = t.srcCmd.Process.Wait()
	}
	t.cmd, t.srcCmd, t.done = nil, nil, nil
}

// StreamTo 启动转码进程，把 MPEG-TS 流写入 w。
// 返回的 cancel 必须在连接关闭时调用以终止转码进程；
// done 在转码进程退出（流结束）时关闭。
func (t *Transcoder) StreamTo(w io.Writer) (cancel func(), done <-chan struct{}, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	ssSec := float64(t.offset) / 1000
	t.offset = 0

	stderr := new(limitBuffer)
	args := []string{"-hide_banner", "-loglevel", "error"}

	var srcCmd *exec.Cmd
	if t.srcURL == "" {
		// 本地文件源：ffmpeg 直接读取，支持输入级快速定位。
		if ssSec > 0 {
			args = append(args, "-ss", strconv.FormatFloat(ssSec, 'f', 2, 64))
		}
		args = append(args, "-i", t.path)
	} else {
		// 在线源：seek 由 yt-dlp --download-sections 完成（管道不可 seek），
		// ffmpeg 从 stdin 读取 yt-dlp 已合并的音视频流。
		srcCmd = exec.Command("yt-dlp", ytDlpStreamArgs(t.srcURL, ssSec, t.isLive, t.opts)...)
		srcCmd.Stderr = stderr
		srcStdout, pipeErr := srcCmd.StdoutPipe()
		if pipeErr != nil {
			return func() {}, nil, fmt.Errorf("创建 yt-dlp 管道失败: %w", pipeErr)
		}
		if startErr := srcCmd.Start(); startErr != nil {
			return func() {}, nil, fmt.Errorf("启动 yt-dlp 失败: %w", startErr)
		}
		args = append(args, "-i", "pipe:0")
		cmd := exec.Command("ffmpeg", append(args, outputArgs()...)...)
		cmd.Stdin = srcStdout
		return t.launchLocked(cmd, srcCmd, w, stderr)
	}

	cmd := exec.Command("ffmpeg", append(args, outputArgs()...)...)
	return t.launchLocked(cmd, nil, w, stderr)
}

// outputArgs 是输出 MPEG-TS 直播流的公共转码参数（电视端兼容优先）。
func outputArgs() []string {
	return []string{
		"-map", "0:v:0", "-map", "0:a:0?",
		"-sn", "-dn",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
		"-maxrate", "4M", "-bufsize", "8M", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "192k", "-ac", "2",
		"-f", "mpegts", "pipe:1",
	}
}

// launchLocked 启动 ffmpeg 并建立取消逻辑；src 为其上游 yt-dlp 进程（可为 nil）。
// 调用方须持有 t.mu（StreamTo 持锁调用，launchLocked 内不得再加锁）。
func (t *Transcoder) launchLocked(cmd, src *exec.Cmd, w io.Writer, stderr *limitBuffer) (func(), <-chan struct{}, error) {
	cmd.Stdout = w
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		if src != nil && src.Process != nil {
			_ = src.Process.Kill()
			_, _ = src.Process.Wait()
		}
		return func() {}, nil, fmt.Errorf("启动 ffmpeg 失败: %w", err)
	}
	t.cmd = cmd
	t.srcCmd = src
	t.stderr = stderr
	t.done = make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(t.done)
	}()

	cancel := func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		// 会话已被新一代进程替换时无需处理。
		if t.cmd != cmd {
			return
		}
		t.stopLocked()
	}
	return cancel, t.done, nil
}

// limitBuffer 截断保存进程 stderr，仅用于出错诊断。
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

func (b *limitBuffer) Len() int { return len(b.buf) }
