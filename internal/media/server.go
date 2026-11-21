package media

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MIME MPEG-TS 流的 Content-Type。
const mpegtsMIME = "video/mp2t"

// session 是一次投屏对应的流会话。
type session struct {
	id           string
	path         string // 源文件路径
	direct       bool   // true=原文件直出；false=ffmpeg 实时转码
	mime         string
	title        string
	subtitlePath string      // 外挂字幕路径（可选）
	tc           *Transcoder // 仅转码模式非空
}

// StreamServer 在局域网侧提供媒体流 HTTP 服务。
// 直出会话通过 /f/{id} 提供带 Range 的原文件；转码会话通过 /t/{id} 提供 MPEG-TS。
type StreamServer struct {
	listener net.Listener
	server   *http.Server

	mu       sync.Mutex
	sessions map[string]*session
}

// NewStreamServer 启动监听所有网卡的随机端口。
func NewStreamServer() (*StreamServer, error) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, fmt.Errorf("流服务监听失败: %w", err)
	}
	s := &StreamServer{
		listener: ln,
		sessions: map[string]*session{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/f/", s.serveDirect)
	mux.HandleFunc("/t/", s.serveTranscode)
	mux.HandleFunc("/s/", s.serveSubtitle)
	s.server = &http.Server{Handler: mux}
	go func() {
		if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("流服务退出: %v", err)
		}
	}()
	return s, nil
}

// Port 返回流服务监听端口。
func (s *StreamServer) Port() int { return s.listener.Addr().(*net.TCPAddr).Port }

// Close 关闭流服务并终止全部转码进程。
func (s *StreamServer) Close() {
	_ = s.server.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.tc != nil {
			sess.tc.Stop()
		}
	}
	s.sessions = map[string]*session{}
}

// AddDirectWithSubtitle 注册带外挂字幕的直出会话，返回会话 ID。
func (s *StreamServer) AddDirectWithSubtitle(path, mime, title, subtitlePath string) string {
	return s.add(&session{id: newSessionID(), path: path, direct: true, mime: mime, title: title, subtitlePath: subtitlePath})
}

// AddDirect 注册直出会话，返回会话 ID。
func (s *StreamServer) AddDirect(path, mime, title string) string {
	return s.AddDirectWithSubtitle(path, mime, title, "")
}

// AddTranscodeWithSubtitle 注册带外挂字幕的本地文件流会话，返回会话 ID。
func (s *StreamServer) AddTranscodeWithSubtitle(path, title string, plan Plan, subtitlePath string) string {
	return s.add(&session{
		id: newSessionID(), path: path, direct: false,
		mime: plan.OutputMIME(), title: title, tc: NewTranscoder(path, plan),
		subtitlePath: subtitlePath,
	})
}

// AddTranscode 注册本地文件流会话（按 plan 换封装或转码），返回会话 ID。
func (s *StreamServer) AddTranscode(path, title string, plan Plan) string {
	return s.AddTranscodeWithSubtitle(path, title, plan, "")
}

// SetSubtitle 为指定会话补充或更新外挂字幕路径。
func (s *StreamServer) SetSubtitle(id, subtitlePath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess := s.sessions[id]; sess != nil {
		sess.subtitlePath = subtitlePath
	}
}

// SubtitleURL 构造外挂字幕的 HTTP 地址。
func (s *StreamServer) SubtitleURL(hostIP, id string) string {
	s.mu.Lock()
	sess := s.sessions[id]
	s.mu.Unlock()
	if sess == nil || sess.subtitlePath == "" {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(sess.subtitlePath))
	if ext == "" {
		ext = ".srt"
	}
	return fmt.Sprintf("http://%s:%d/s/%s%s", hostIP, s.Port(), id, ext)
}

// AddCamera 注册摄像头实时采集会话（macOS avfoundation），返回会话 ID。
// videoDevice / audioDevice 为设备序号；audioDevice 为空表示不采集声音。
func (s *StreamServer) AddCamera(videoDevice, audioDevice string, screen bool, title string, plan Plan) string {
	return s.add(&session{
		id: newSessionID(), direct: false,
		mime: plan.OutputMIME(), title: title,
		tc: NewCameraTranscoder(&CameraSource{VideoDevice: videoDevice, AudioDevice: audioDevice, Screen: screen}, plan),
	})
}

// AddTranscodeURL 注册在线视频流会话。Go 传输模式（见 NeedsPipeMode 之外的站点）：
// yt-dlp 只解析，直链由 Go 原生拉取并在本机服务，ffmpeg 以 HTTP 输入读取；
// 注册时同步预热传输，失败直接返回错误，让投屏点击时就能发现问题，
// 而不是等电视拉流时才转圈。管道模式跳过预热（Resolve 阶段已验证过可达）。
// urls 是 ResolveDirect 附带返回的直链，为空则预热时回退到 DirectURLs 再取。
// opts 决定代理与 Cookies 行为，语义见 ytDlpCommonArgs。
func (s *StreamServer) AddTranscodeURL(ctx context.Context, url, title string, isLive bool, opts Options, plan Plan, extractor string, urls []string) (string, error) {
	return s.AddTranscodeURLWithHeaders(ctx, url, title, isLive, opts, plan, extractor, urls, nil)
}

// AddTranscodeURLWithHeaders 同 AddTranscodeURL，额外接收每条直链必需的
// HTTP 请求头（与 urls 一一对应）。
func (s *StreamServer) AddTranscodeURLWithHeaders(ctx context.Context, url, title string, isLive bool, opts Options, plan Plan, extractor string, urls []string, headers []map[string]string) (string, error) {
	tc := NewURLTranscoderWithHeaders(url, isLive, opts, plan, extractor, urls, headers)
	if err := tc.PrefetchDirect(ctx); err != nil {
		return "", err
	}
	return s.add(&session{
		id: newSessionID(), direct: false,
		mime: plan.OutputMIME(), title: title, tc: tc,
	}), nil
}

func (s *StreamServer) add(sess *session) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.id] = sess
	return sess.id
}

// Remove 注销会话并终止其转码进程。
func (s *StreamServer) Remove(id string) {
	s.mu.Lock()
	sess := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if sess != nil && sess.tc != nil {
		sess.tc.Stop()
	}
}

// SetTranscodeOffset 让转码会话从指定秒数重新开始：终止当前进程，
// 下一次电视端拉流时从该位置转码输出。
func (s *StreamServer) SetTranscodeOffset(id string, seconds float64) error {
	s.mu.Lock()
	sess := s.sessions[id]
	s.mu.Unlock()
	if sess == nil || sess.tc == nil {
		return fmt.Errorf("转码会话 %s 不存在", id)
	}
	sess.tc.RestartAt(seconds)
	return nil
}

// URL 构造电视端可访问的会话 URL。hostIP 为本机局域网地址。
func (s *StreamServer) URL(hostIP, id string, direct bool) string {
	if direct {
		return fmt.Sprintf("http://%s:%d/f/%s", hostIP, s.Port(), id)
	}
	return fmt.Sprintf("http://%s:%d/t/%s", hostIP, s.Port(), id)
}

func (s *StreamServer) serveDirect(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByPath(r.URL.Path, "/f/")
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(sess.path)
	if err != nil {
		http.Error(w, "源文件不可读", http.StatusGone)
		return
	}
	defer f.Close()

	// DLNA 规范头：通知设备当前为流式传输，且原文件支持 HTTP 范围请求与拖动（DLNA.ORG_OP=01）
	w.Header().Set("transferMode.dlna.org", "Streaming")
	w.Header().Set("contentFeatures.dlna.org", "DLNA.ORG_OP=01;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000")
	w.Header().Set("RealTimeInfo.dlna.org", "DLNA.ORG_TLR=-1")

	// ServeContent 自带 Range/If-Range 处理，电视端拖动进度条即可按需取段。
	http.ServeContent(w, r, filepath.Base(sess.path), time.Now(), f)
}

func (s *StreamServer) serveTranscode(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByPath(r.URL.Path, "/t/")
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	// 连接生命周期诊断：电视端周期性断开重连（DLNA 直播流常态）时，
	// 这两条日志能直接呈现连接模式——多久断一次、每次收了多少字节。
	// 平均速率与首字节耗时是判断「电视端卡顿」的第一手依据：
	// 平均速率应始终高于源码率，否则电视必然反复缓冲。
	connStart := time.Now()
	var connBytes int64
	log.Printf("拉流开始 %s（会话 %s）", r.URL.Path, sess.id)

	// 使用会话注册时的容器类型：可能是 MPEG-TS，也可能是碎片化 MP4。
	w.Header().Set("Content-Type", sess.mime)
	w.Header().Set("Connection", "close")
	// DLNA 规范头：实时转码流不支持 Range 定位（DLNA.ORG_OP=00）
	w.Header().Set("transferMode.dlna.org", "Streaming")
	w.Header().Set("contentFeatures.dlna.org", "DLNA.ORG_OP=00;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000")
	w.Header().Set("RealTimeInfo.dlna.org", "DLNA.ORG_TLR=-1")

	w.WriteHeader(http.StatusOK)
	// 主动 flush，让电视端尽快收到数据开始起播。
	counting := &countingFlushWriter{w: &flushWriter{w: w, flushedAt: time.Now()}}
	counting.flushHeader()

	cancel, done, err := sess.tc.StreamTo(counting)
	if err != nil {
		// 响应头已发出，只能中断连接；电视端表现为无法播放。
		log.Printf("转码启动失败: %v", err)
		return
	}
	// 阻塞保持响应打开：客户端断开（停止/Seek）或转码进程退出（播放完毕）时结束。
	// 断开原因必须区分：电视端主动断开（缓冲满/超时后的重连前兆）与服务端
	// 流结束（采集异常）指向完全不同的排查方向。
	select {
	case <-r.Context().Done():
		connBytes = counting.bytes()
		log.Printf("拉流结束 %s（会话 %s）：电视端断开，时长 %s，送达 %d 字节%s",
			r.URL.Path, sess.id, time.Since(connStart).Round(time.Millisecond), connBytes, rateSummary(connBytes, connStart))
	case <-done:
		connBytes = counting.bytes()
		log.Printf("拉流结束 %s（会话 %s）：服务端流结束，时长 %s，送达 %d 字节%s",
			r.URL.Path, sess.id, time.Since(connStart).Round(time.Millisecond), connBytes, rateSummary(connBytes, connStart))
	}
	cancel()
	if msg := sess.tc.LastStderr(); msg != "" {
		log.Printf("转码进程输出: %s", msg)
	}
}

// serveSubtitle 提供外挂字幕文件下载（/s/{id} 或 /s/{id}.ext）。
func (s *StreamServer) serveSubtitle(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/s/")
	id := path
	if idx := strings.IndexByte(path, '.'); idx >= 0 {
		id = path[:idx]
	}
	s.mu.Lock()
	sess := s.sessions[id]
	s.mu.Unlock()
	if sess == nil || sess.subtitlePath == "" {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(sess.subtitlePath)
	if err != nil {
		http.Error(w, "字幕文件不可读", http.StatusGone)
		return
	}
	defer f.Close()

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	ext := strings.ToLower(filepath.Ext(sess.subtitlePath))
	switch ext {
	case ".vtt":
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	case ".srt":
		w.Header().Set("Content-Type", "text/srt; charset=utf-8")
	case ".ass", ".ssa":
		w.Header().Set("Content-Type", "text/x-ssa; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	http.ServeContent(w, r, filepath.Base(sess.subtitlePath), time.Now(), f)
}

// rateSummary 返回「平均速率 + 首字节耗时」的诊断后缀，用于投屏日志。
// 送达 0 字节时明确标注，这是「电视端等不到数据就断开」的典型形态。
func rateSummary(n int64, start time.Time) string {
	d := time.Since(start)
	if n == 0 {
		return fmt.Sprintf("，平均 0 B/s（%s 内未送达任何数据）", d.Round(time.Millisecond))
	}
	sec := d.Seconds()
	if sec <= 0 {
		return ""
	}
	return fmt.Sprintf("，平均 %.2f MB/s", float64(n)/sec/1048576)
}

// countingFlushWriter 在 flushWriter 基础上统计送达字节数（连接诊断用）。
type countingFlushWriter struct {
	w  *flushWriter
	n  int64
	mu sync.Mutex
}

func (c *countingFlushWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.n += int64(len(p))
	c.mu.Unlock()
	return c.w.Write(p)
}

func (c *countingFlushWriter) flushHeader() { c.w.flushHeader() }

func (c *countingFlushWriter) bytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// flushWriter 在适当时机 Flush，兼顾首包起播速度与 TCP 吞吐。
type flushWriter struct {
	w         http.ResponseWriter
	flushedAt time.Time
	pending   int
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err != nil {
		return n, err
	}
	f.pending += n
	now := time.Now()
	// 累积满 16KB 或超过 100ms 刷新一次，避免频繁系统调用与小包碎片化
	if f.pending >= 16*1024 || now.Sub(f.flushedAt) >= 100*time.Millisecond {
		if flusher, ok := f.w.(http.Flusher); ok {
			flusher.Flush()
			f.flushedAt = now
			f.pending = 0
		}
	}
	return n, nil
}

func (f *flushWriter) flushHeader() {
	if flusher, ok := f.w.(http.Flusher); ok {
		flusher.Flush()
		f.flushedAt = time.Now()
		f.pending = 0
	}
}

// sessionByPath 按 /f/{id} 或 /t/{id} 路径取出会话。
func (s *StreamServer) sessionByPath(path, prefix string) *session {
	id := path[len(prefix):]
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	if sess == nil {
		return nil
	}
	// 直出/转码路由不能互换。
	if sess.direct != (prefix == "/f/") {
		return nil
	}
	return sess
}

func newSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// MimeTypeFor 按文件扩展名返回直出应使用的 Content-Type。
func MimeTypeFor(path string) string {
	switch filepath.Ext(path) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		return "video/x-matroska"
	case ".webm":
		return "video/webm"
	case ".ts", ".mts", ".m2ts":
		return mpegtsMIME
	case ".avi":
		return "video/x-msvideo"
	default:
		return "application/octet-stream"
	}
}
