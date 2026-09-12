package media

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MIME MPEG-TS 流的 Content-Type。
const mpegtsMIME = "video/mp2t"

// session 是一次投屏对应的流会话。
type session struct {
	id     string
	path   string // 源文件路径
	direct bool   // true=原文件直出；false=ffmpeg 实时转码
	mime   string
	title  string
	tc     *Transcoder // 仅转码模式非空
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

// AddDirect 注册直出会话，返回会话 ID。
func (s *StreamServer) AddDirect(path, mime, title string) string {
	return s.add(&session{id: newSessionID(), path: path, direct: true, mime: mime, title: title})
}

// AddTranscode 注册本地文件流会话（按 plan 换封装或转码），返回会话 ID。
func (s *StreamServer) AddTranscode(path, title string, plan Plan) string {
	return s.add(&session{
		id: newSessionID(), path: path, direct: false,
		mime: plan.OutputMIME(), title: title, tc: NewTranscoder(path, plan),
	})
}

// AddTranscodeURL 注册在线视频流会话：yt-dlp 解析拉流，ffmpeg 按 plan 换封装或转码。
// opts 决定 yt-dlp 的代理与 Cookies 行为，语义见 ytDlpCommonArgs。
func (s *StreamServer) AddTranscodeURL(url, title string, isLive bool, opts Options, plan Plan) string {
	return s.add(&session{
		id: newSessionID(), direct: false,
		mime: plan.OutputMIME(), title: title, tc: NewURLTranscoder(url, isLive, opts, plan),
	})
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
	// ServeContent 自带 Range/If-Range 处理，电视端拖动进度条即可按需取段。
	http.ServeContent(w, r, filepath.Base(sess.path), time.Now(), f)
}

func (s *StreamServer) serveTranscode(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByPath(r.URL.Path, "/t/")
	if sess == nil {
		http.NotFound(w, r)
		return
	}
	// 使用会话注册时的容器类型：可能是 MPEG-TS，也可能是碎片化 MP4。
	w.Header().Set("Content-Type", sess.mime)
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)
	// 主动 flush，让电视端尽快收到数据开始起播。
	flushWriter{w}.flushHeader()

	cancel, done, err := sess.tc.StreamTo(flushWriter{w})
	if err != nil {
		// 响应头已发出，只能中断连接；电视端表现为无法播放。
		log.Printf("转码启动失败: %v", err)
		return
	}
	// 阻塞保持响应打开：客户端断开（停止/Seek）或转码进程退出（播放完毕）时结束。
	select {
	case <-r.Context().Done():
	case <-done:
	}
	cancel()
	if sess.tc.stderr != nil && sess.tc.stderr.Len() > 0 {
		log.Printf("转码进程输出: %s", sess.tc.stderr.String())
	}
}

// flushWriter 在每次写入后主动 Flush，把转码输出尽快推给电视端。
type flushWriter struct {
	w http.ResponseWriter
}

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if flusher, ok := f.w.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, err
}

func (f flushWriter) flushHeader() {
	if flusher, ok := f.w.(http.Flusher); ok {
		flusher.Flush()
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
