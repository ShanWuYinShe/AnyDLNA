package media

import (
	"context"
	"fmt"
	"io"
	"os"
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
// directCacheTTL 是直链缓存的有效期。googlevideo 直链带 expire 参数，
// 通常几小时有效；1 小时内复用可避免电视每次重连都重新解析（约 5 秒）。
const directCacheTTL = time.Hour

// pipeExtractors 是必须走管道模式的站点（yt-dlp 下载、ffmpeg 只合并）。
// 这些站点的 CDN 拒绝非浏览器 HTTP 客户端（实测 B 站 mcdn 对 ffmpeg/curl
// 返回 403 或直接拒绝连接，yt-dlp 的 Python 下载栈则正常），ffmpeg 无法
// 直连直链，只能由 yt-dlp 下载后经管道喂给 ffmpeg。其余站点走直链模式。
var pipeExtractors = map[string]bool{
	"bilibili": true,
}

// NeedsPipeMode 报告该站点是否必须走管道模式（见 pipeExtractors）。
func NeedsPipeMode(extractor string) bool {
	return pipeExtractors[extractor]
}

// plan 决定输出方式：可直通的轨道用 -c copy 复制，避免不必要的重编码。
type Transcoder struct {
	mu      sync.Mutex
	path    string  // 本地文件路径；为空表示在线源
	srcURL  string  // 在线视频页面/流地址（直链模式只用于解析，管道模式用于下载）
	isLive  bool    // 直播流（不支持跳转）
	opts    Options // 在线源的代理与 Cookies 配置
	plan    Plan    // 输出方式（换封装 / 转码）
	offset  int64   // 转码起始位置（毫秒），供下一次启动使用
	pipe    bool    // 管道模式：yt-dlp 下载经管道喂 ffmpeg；否则直链模式
	streams map[*stream]struct{}

	// cachedURLs 是已解析的直链（1 条为一体流，2 条为分离音视频），
	// cachedAt 为解析时间，TTL 内复用，过期或跳转失败时重取。仅直链模式用。
	cachedURLs []string
	cachedAt   time.Time

	// lastStderr 仅用于出错诊断，记录最近一次启动的 stderr 缓冲。
	lastStderr *limitBuffer
}

// stream 是一次拉流对应的进程组：直链模式只有 ffmpeg；管道模式是
// ffmpeg + 视频路/音频路两个 yt-dlp 进程（各走一根管道，见 StreamTo）。
type stream struct {
	cmd         *exec.Cmd
	srcCmd      *exec.Cmd
	srcAudioCmd *exec.Cmd
	done        chan struct{}
	stderr      *limitBuffer
}

// NewTranscoder 创建针对本地文件的转码器。
// plan 决定是否复制视频/音频轨道；零值 Plan 视为完整转码。
func NewTranscoder(path string, plan Plan) *Transcoder {
	return &Transcoder{path: path, plan: plan.orTranscode()}
}

// NewURLTranscoder 创建针对在线视频源的转码器。
// plan 决定是否复制视频/音频轨道；零值 Plan 视为完整转码。
// extractor 决定拉流模式：需管道模式的站点（见 NeedsPipeMode）走 yt-dlp
// 下载管道，其余走 ffmpeg 直连直链。
func NewURLTranscoder(url string, isLive bool, opts Options, plan Plan, extractor string) *Transcoder {
	return &Transcoder{srcURL: url, isLive: isLive, opts: opts, plan: plan.orTranscode(), pipe: NeedsPipeMode(extractor)}
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
	// 先杀 ffmpeg。管道模式下再杀两路 yt-dlp：yt-dlp 会拉起自己的 ffmpeg
	// 子进程下载/合并分片，必须整组终止，否则孤儿进程继续占着 CDN 连接，
	// 站点按 IP 限制并发连接，残留连接会让后续投屏被限速。
	killProcGroup(s.cmd)
	killProcGroup(s.srcCmd)
	killProcGroup(s.srcAudioCmd)
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
	if s.srcAudioCmd != nil && s.srcAudioCmd.Process != nil {
		_, _ = s.srcAudioCmd.Process.Wait()
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

	if t.srcURL == "" {
		// 本地文件源：ffmpeg 直接读取，支持输入级快速定位。
		if ssSec > 0 {
			args = append(args, "-ss", strconv.FormatFloat(ssSec, 'f', 2, 64))
		}
		args = append(args, "-i", t.path)
	} else if t.pipe {
		// 管道模式（部分站点 CDN 拒绝 ffmpeg 直连，见 pipeExtractors）：
		// 音视频分开取，各走一根管道，再由 ffmpeg 双输入合并。
		// 不能合用一根管道：native 下载器在多路格式同时输出到同一 stdout
		// 时会跳过合并、把音视频混写在一起，下游解析不出音频轨。
		// seek 由两路各自的 --download-sections 完成（管道不可 seek）。
		videoCmd := toolCmd("yt-dlp", ytDlpSingleStreamArgs(t.srcURL, videoOnlySelector, ssSec, t.isLive, t.opts)...)
		videoCmd.Stderr = stderr
		// 自成进程组：终止时才能连同 yt-dlp 拉起的 ffmpeg 子进程一起回收。
		setProcGroup(videoCmd)
		videoOut, pipeErr := videoCmd.StdoutPipe()
		if pipeErr != nil {
			return func() {}, nil, fmt.Errorf("创建视频管道失败: %w", pipeErr)
		}
		if startErr := videoCmd.Start(); startErr != nil {
			return func() {}, nil, fmt.Errorf("启动视频拉流失败: %w", startErr)
		}
		audioCmd := toolCmd("yt-dlp", ytDlpSingleStreamArgs(t.srcURL, audioOnlySelector, ssSec, t.isLive, t.opts)...)
		audioCmd.Stderr = stderr
		setProcGroup(audioCmd)
		audioOut, pipeErr := audioCmd.StdoutPipe()
		if pipeErr != nil {
			killProcGroup(videoCmd)
			_, _ = videoCmd.Process.Wait()
			return func() {}, nil, fmt.Errorf("创建音频管道失败: %w", pipeErr)
		}
		if startErr := audioCmd.Start(); startErr != nil {
			killProcGroup(videoCmd)
			_, _ = videoCmd.Process.Wait()
			return func() {}, nil, fmt.Errorf("启动音频拉流失败: %w", startErr)
		}
		args = append(args, "-i", "pipe:0", "-i", "pipe:3")
		cmd := toolCmd("ffmpeg", append(args, outputArgs(t.plan, 1)...)...)
		setProcGroup(cmd)
		cmd.Stdin = videoOut
		// 音频管道以 fd 3 传给 ffmpeg（对应 pipe:3）。
		if f, ok := audioOut.(*os.File); ok {
			cmd.ExtraFiles = []*os.File{f}
		} else {
			killProcGroup(videoCmd)
			_, _ = videoCmd.Process.Wait()
			killProcGroup(audioCmd)
			_, _ = audioCmd.Process.Wait()
			return func() {}, nil, fmt.Errorf("音频管道不是文件句柄")
		}
		return t.launchLocked(cmd, videoCmd, audioCmd, w, stderr)
	} else {
		// 直链模式：yt-dlp 只负责解析出直链（-g，不下载），ffmpeg 直接对
		// 直链完成下载、定位（-ss 输入级 Range 定位）、合并全部操作。
		// 跳转不再杀进程重下：同一组直链 + 新的 -ss 即可，电视重拉流就行。
		urls, err := t.directURLsLocked()
		if err != nil {
			return func() {}, nil, err
		}
		// -ss 必须紧贴在每个 -i 之前才是输入级定位（走 HTTP Range，
		// 直链可寻址）；放 -i 之后会变成输出选项，定位失效。
		var ss string
		if ssSec > 0 && !t.isLive {
			ss = strconv.FormatFloat(ssSec, 'f', 2, 64)
		}
		audioIdx := 0
		for _, u := range urls {
			if ss != "" {
				args = append(args, "-ss", ss)
			}
			args = append(args, "-i", u)
		}
		if len(urls) == 2 {
			audioIdx = 1
		}
		cmd := toolCmd("ffmpeg", append(args, outputArgs(t.plan, audioIdx)...)...)
		setProcGroup(cmd)
		// ffmpeg 直连 CDN，必须从设置里的代理走：它只认 http(s) 代理，
		// 从环境变量读取；socks 下无法工作，由 CastURL 提前拦截。
		if proxy := EffectiveProxy(t.opts); IsHTTPProxy(proxy) {
			cmd.Env = EnvWithHTTPProxy(cmd.Env, proxy)
		}
		return t.launchLocked(cmd, nil, nil, w, stderr)
	}

	cmd := toolCmd("ffmpeg", append(args, outputArgs(t.plan, 0)...)...)
	setProcGroup(cmd)
	return t.launchLocked(cmd, nil, nil, w, stderr)
}

// directURLsLocked 返回本次拉流可用的直链（1 条一体流或 2 条分离音视频），
// 优先复用 TTL 内的缓存，否则调用 yt-dlp 重新解析；调用方须持有 t.mu。
// 注意 StreamTo 持锁调用，DirectURLs 内部不再加锁。
func (t *Transcoder) directURLsLocked() ([]string, error) {
	if len(t.cachedURLs) > 0 && time.Since(t.cachedAt) < directCacheTTL {
		return t.cachedURLs, nil
	}
	urls, err := DirectURLs(context.Background(), t.srcURL, t.opts)
	if err != nil {
		// 缓存过期但重取失败时，若有旧链（刚过期）仍可一试；
		// 直链 expire 通常数小时，刚过 TTL 大概率仍有效。
		if len(t.cachedURLs) > 0 {
			return t.cachedURLs, nil
		}
		return nil, err
	}
	t.cachedURLs = urls
	t.cachedAt = time.Now()
	return urls, nil
}

// PrefetchDirect 预解析直链并缓存，让投屏点击时就能发现解析失败，
// 而不是等电视拉流时才转圈。失败时返回错误，由调用方展示给用户。
// 管道模式不需要直链，直接返回 nil。
func (t *Transcoder) PrefetchDirect(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.srcURL == "" || t.pipe {
		return nil
	}
	urls, err := DirectURLs(ctx, t.srcURL, t.opts)
	if err != nil {
		return err
	}
	t.cachedURLs = urls
	t.cachedAt = time.Now()
	return nil
}

// outputArgs 构造输出直播流的 ffmpeg 参数。
//
// 关键性能考量：视频重编码是整条链路唯一的瓶颈（实测 1080p 约 2 倍、
// 4K 约 1.4 倍实时），而视频轨道复制（-c copy）可达 18–29 倍实时且画质无损。
// 因此只要源视频是设备可解码的编码（H.264），就一律复制直通；
// 音频按 plan 决定，不兼容时才重编码为 AAC（开销相对视频可忽略）。
// audioInput 是音频所在输入的序号：单输入（本地文件/旧链路）为 0，
// 在线双管道拉流时视频在 pipe:0、音频在 pipe:3 对应的第 1 个输入。
func outputArgs(plan Plan, audioInput int) []string {
	args := []string{"-map", "0:v:0", "-map", strconv.Itoa(audioInput) + ":a:0?", "-sn", "-dn"}

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

// launchLocked 启动 ffmpeg 并登记进程组；src/srcAudio 为管道模式的上游
// yt-dlp 进程（直链与本地模式为 nil），启动失败时一并回收。
//
// 每次调用产生独立的 stream，并把它记入 t.streams，使 Stop/RestartAt 能覆盖
// 全部并发拉流。返回的 cancel 只作用于本次的进程组。
// 调用方须持有 t.mu（StreamTo 持锁调用，launchLocked 内不得再加锁）。
func (t *Transcoder) launchLocked(cmd, src, srcAudio *exec.Cmd, w io.Writer, stderr *limitBuffer) (func(), <-chan struct{}, error) {
	cmd.Stdout = w
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		for _, c := range []*exec.Cmd{src, srcAudio} {
			if c != nil && c.Process != nil {
				_ = c.Process.Kill()
				_, _ = c.Process.Wait()
			}
		}
		return func() {}, nil, fmt.Errorf("启动 ffmpeg 失败: %w", err)
	}

	s := &stream{cmd: cmd, srcCmd: src, srcAudioCmd: srcAudio, stderr: stderr, done: make(chan struct{})}
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
