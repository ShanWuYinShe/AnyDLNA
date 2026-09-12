package media

import (
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// Transcoder 管理一路投屏会话的转码进程，把源（本地文件或在线视频）转为 MPEG-TS。
// 本地源由 ffmpeg 直接读取；在线源先经 yt-dlp 解析合并为流，再管道交给 ffmpeg。
//
// 同一会话可能被并发拉流：电视端常见做法是先发一个探测连接、再发真正的播放连接，
// 播放过程中也可能重连。因此每次 StreamTo 都启动并登记独立的进程组，
// 互不干扰——早期版本把进程句柄放在单份字段里，后一次拉流会覆盖前一次，
// 导致先前的进程既无法取消、也不在 Stop 的管辖范围内，最终泄漏并持续占用带宽。
//
// plan 决定输出方式：可直通的轨道用 -c copy 复制，避免不必要的重编码。
type Transcoder struct {
	mu      sync.Mutex
	path    string  // 本地文件路径；为空表示在线源
	srcURL  string  // 在线视频页面/流地址（走 yt-dlp）
	isLive  bool    // 直播流（不支持 --download-sections 快进）
	opts    Options // 在线源的代理与 Cookies 配置
	plan    Plan    // 输出方式（换封装 / 转码）
	offset  int64   // 转码起始位置（毫秒），供下一次启动使用
	streams map[*stream]struct{}

	// lastStderr 仅用于出错诊断，记录最近一次启动的 stderr 缓冲。
	lastStderr *limitBuffer
}

// stream 是一次拉流对应的进程组（ffmpeg + 可选的 yt-dlp）。
type stream struct {
	cmd    *exec.Cmd
	srcCmd *exec.Cmd
	done   chan struct{}
	stderr *limitBuffer
}

// NewTranscoder 创建针对本地文件的转码器。
// plan 决定是否复制视频/音频轨道；零值 Plan 视为完整转码。
func NewTranscoder(path string, plan Plan) *Transcoder {
	return &Transcoder{path: path, plan: plan.orTranscode()}
}

// NewURLTranscoder 创建针对在线视频源的转码器。
// plan 决定是否复制视频/音频轨道；零值 Plan 视为完整转码。
func NewURLTranscoder(url string, isLive bool, opts Options, plan Plan) *Transcoder {
	return &Transcoder{srcURL: url, isLive: isLive, opts: opts, plan: plan.orTranscode()}
}

// orTranscode 把未设定模式的 Plan 归一化为完整转码，避免误用零值导致参数缺失。
func (p Plan) orTranscode() Plan {
	if p.Mode == "" {
		return Plan{Mode: OutputTranscode}
	}
	return p
}

// RestartAt 终止当前进程并记录新的起始位置，等待下一次拉流时重新启动。
func (t *Transcoder) RestartAt(seconds float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if seconds < 0 {
		seconds = 0
	}
	t.offset = int64(seconds * 1000)
	t.stopAllLocked()
}

// Stop 终止全部正在进行的拉流进程。
func (t *Transcoder) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopAllLocked()
}

// stopAllLocked 终止所有已登记的拉流进程并等待回收；调用方须持有 t.mu。
func (t *Transcoder) stopAllLocked() {
	live := make([]*stream, 0, len(t.streams))
	for s := range t.streams {
		live = append(live, s)
	}
	for _, s := range live {
		t.stopStreamLocked(s)
	}
}

// stopStreamLocked 终止一路拉流进程并等待回收；调用方须持有 t.mu。
func (t *Transcoder) stopStreamLocked(s *stream) {
	// 先杀 ffmpeg：其 stdin（来自 yt-dlp）关闭后，yt-dlp 也会因写入失败退出。
	killProcGroup(s.cmd)
	// yt-dlp 会自己拉起 ffmpeg 子进程下载 / 合并分片，必须整组终止：
	// 只杀 yt-dlp 会让这些子进程变成孤儿（PPID=1）并继续占着 CDN 连接，
	// 站点按 IP 限制并发连接，残留连接会让后续投屏被限速。
	killProcGroup(s.srcCmd)
	// s.done 由启动时的 goroutine 关闭，是唯一调用 cmd.Wait 的地方；
	// srcCmd 没有后台 goroutine，直接 Wait 回收避免僵尸进程。
	if s.done != nil {
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
		}
	}
	if s.srcCmd != nil && s.srcCmd.Process != nil {
		_, _ = s.srcCmd.Process.Wait()
	}
	delete(t.streams, s)
}

// StreamTo 启动一路转码进程，把 MPEG-TS 流写入 w。
//
// 每次调用都会启动独立的进程组并登记；返回的 cancel 只终止本次启动的进程，
// 因此多个连接可以并存，任何一方断开都不会影响其他连接。
// cancel 必须在连接关闭时调用；done 在本次转码进程退出（流结束）时关闭。
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
		srcCmd = toolCmd("yt-dlp", ytDlpStreamArgs(t.srcURL, ssSec, t.isLive, t.opts)...)
		srcCmd.Stderr = stderr
		// 自成进程组：终止时才能连同 yt-dlp 拉起的 ffmpeg 子进程一起回收。
		setProcGroup(srcCmd)
		srcStdout, pipeErr := srcCmd.StdoutPipe()
		if pipeErr != nil {
			return func() {}, nil, fmt.Errorf("创建 yt-dlp 管道失败: %w", pipeErr)
		}
		if startErr := srcCmd.Start(); startErr != nil {
			return func() {}, nil, fmt.Errorf("启动 yt-dlp 失败: %w", startErr)
		}
		args = append(args, "-i", "pipe:0")
		cmd := toolCmd("ffmpeg", append(args, outputArgs(t.plan)...)...)
		setProcGroup(cmd)
		cmd.Stdin = srcStdout
		return t.launchLocked(cmd, srcCmd, w, stderr)
	}

	cmd := toolCmd("ffmpeg", append(args, outputArgs(t.plan)...)...)
	setProcGroup(cmd)
	return t.launchLocked(cmd, nil, w, stderr)
}

// outputArgs 构造输出直播流的 ffmpeg 参数。
//
// 关键性能考量：视频重编码是整条链路唯一的瓶颈（实测 1080p 约 2 倍、
// 4K 约 1.4 倍实时），而视频轨道复制（-c copy）可达 18–29 倍实时且画质无损。
// 因此只要源视频是设备可解码的编码（H.264），就一律复制直通；
// 音频按 plan 决定，不兼容时才重编码为 AAC（开销相对视频可忽略）。
func outputArgs(plan Plan) []string {
	args := []string{"-map", "0:v:0", "-map", "0:a:0?", "-sn", "-dn"}

	if plan.CopyVideo {
		args = append(args, "-c:v", "copy")
	} else {
		args = append(args,
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
			"-maxrate", "4M", "-bufsize", "8M", "-pix_fmt", "yuv420p")
	}

	if plan.CopyAudio {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-c:a", "aac", "-b:a", "192k", "-ac", "2")
	}

	return append(args, containerArgs(plan.Container)...)
}

// containerArgs 返回目标容器的封装参数。
//
// MPEG-TS 是 DLNA 的通用基线，绝大多数设备都能直接播放；
// 碎片化 MP4 用于设备只声明支持 MP4、不支持 TS 的场景：
// frag_keyframe + empty_moov 让索引前置并按关键帧分片，
// 从而可以边生成边推流（普通 MP4 需要回写索引，无法流式输出）。
func containerArgs(container OutputContainer) []string {
	if container == ContainerFMP4 {
		return []string{
			"-movflags", "frag_keyframe+empty_moov+default_base_moof",
			"-f", "mp4", "pipe:1",
		}
	}
	return []string{"-f", "mpegts", "pipe:1"}
}

// launchLocked 启动 ffmpeg 并登记进程组；src 为其上游 yt-dlp 进程（可为 nil）。
//
// 每次调用产生独立的 stream，并把它记入 t.streams，使 Stop/RestartAt 能覆盖
// 全部并发拉流。返回的 cancel 只作用于本次的进程组。
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

	s := &stream{cmd: cmd, srcCmd: src, stderr: stderr, done: make(chan struct{})}
	t.lastStderr = stderr
	if t.streams == nil {
		t.streams = map[*stream]struct{}{}
	}
	t.streams[s] = struct{}{}
	go func() {
		_ = cmd.Wait()
		close(s.done)
	}()

	cancel := func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		// 已经被 Stop/RestartAt 回收过就无需重复处理。
		if _, ok := t.streams[s]; !ok {
			return
		}
		t.stopStreamLocked(s)
	}
	return cancel, s.done, nil
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

// LastStderr 返回最近一次启动的转码进程 stderr 内容（诊断用，可能为空）。
// 并发拉流时以最后一次启动的为准，仅用于出错时打印线索。
func (t *Transcoder) LastStderr() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastStderr == nil {
		return ""
	}
	return t.lastStderr.String()
}
