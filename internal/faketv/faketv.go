// Package faketv 提供测试用 DLNA MediaRenderer（假电视）。
//
// 没有实体电视时，用它验证投屏全链路：SSDP 发现、SetAVTransportURI/Play、
// 拉流内容校验（TS 同步与字节计数）、跳转重拉、状态与位置轮询。
// 仅测试使用，不要在正式代码中引用。
package faketv

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// TV 是一台假电视：HTTP 服务承载设备描述与 SOAP 控制，UDP 承载 SSDP 应答，
// Play 后在后台 GET 播放地址并消费流（模拟电视拉流），可附带 ffplay 实时
// 播放窗口与录制文件。
type TV struct {
	mu         sync.Mutex
	listener   net.Listener
	server     *http.Server
	ssdpStop   chan struct{}
	ssdpDone   chan struct{}
	baseURL    string
	usn        string
	uri        string
	state      string // STOPPED / PLAYING / PAUSED_PLAYBACK
	seeks      []string
	bytes      int64
	tsPackets  int64
	tsErrors   int64
	cancelPlay context.CancelFunc
	playStart  time.Time
	duration   float64 // 上报的总时长（秒），GetPositionInfo 用

	player    bool // 是否用 ffplay 实时播放收到的流
	playerCmd *exec.Cmd
	playerIn  io.WriteCloser

	recordPath string
	recordFile *os.File
}

// EnablePlayer 启用实时播放：Play 后把收到的流喂给 ffplay 弹窗播放
// （有画面有声音，延迟数秒）。本机没有 ffplay 时返回错误。
func (t *TV) EnablePlayer() error {
	if _, err := exec.LookPath("ffplay"); err != nil {
		return fmt.Errorf("未找到 ffplay（brew install ffmpeg）：%w", err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.player = true
	return nil
}

// SetRecordPath 设置录制文件：Play 后收到的流同时写入该文件（调试用，
// 可边录边用播放器打开看）。空串表示不录制。
func (t *TV) SetRecordPath(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recordPath = path
}

// New 启动一台假电视，监听 127.0.0.1 随机端口。
func New() (*TV, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	t := &TV{
		listener: ln,
		state:    "STOPPED",
		baseURL:  "http://" + ln.Addr().String(),
		usn:      "uuid:FakeTV-0001",
		ssdpStop: make(chan struct{}),
		ssdpDone: make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/description.xml", t.serveDescription)
	mux.HandleFunc("/avt/control", t.serveControl)
	mux.HandleFunc("/rc/control", t.serveControl)
	t.server = &http.Server{Handler: mux}
	go func() { _ = t.server.Serve(ln) }()
	go t.ssdpResponder()
	// 主动广播上线，应用的被动监听也能发现。
	for i := 0; i < 2; i++ {
		t.sendNotify()
	}
	return t, nil
}

// Close 关闭假电视（含后台拉流与 SSDP 应答）。
func (t *TV) Close() {
	t.stopPlayback()
	close(t.ssdpStop)
	<-t.ssdpDone
	_ = t.server.Close()
}

// DescriptionURL 返回设备描述 XML 地址，可直接交给 dlna.Describe。
func (t *TV) DescriptionURL() string { return t.baseURL + "/description.xml" }

// SetDuration 设置上报的媒体总时长（秒）。
func (t *TV) SetDuration(sec float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.duration = sec
}

// URI 返回最近一次下发的播放地址。
func (t *TV) URI() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.uri
}

// State 返回当前传输状态。
func (t *TV) State() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// Seeks 返回收到的跳转目标（TARGET 值）。
func (t *TV) Seeks() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.seeks...)
}

// Stats 返回消费的字节数、TS 包数与同步错误数。
func (t *TV) Stats() (bytes, packets, errors int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bytes, t.tsPackets, t.tsErrors
}

func (t *TV) serveDescription(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	fmt.Fprintf(w, `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
<device>
<deviceType>urn:schemas-upnp-org:device:MediaRenderer:1</deviceType>
<friendlyName>FakeTV</friendlyName>
<UDN>uuid:FakeTV-0001</UDN>
<serviceList>
<service>
<serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType>
<serviceId>urn:upnp-org:serviceId:AVTransport</serviceId>
<SCPDURL>/avt/scpd.xml</SCPDURL>
<controlURL>/avt/control</controlURL>
<eventURL>/avt/event</eventURL>
</service>
<service>
<serviceType>urn:schemas-upnp-org:service:RenderingControl:1</serviceType>
<serviceId>urn:upnp-org:serviceId:RenderingControl</serviceId>
<SCPDURL>/rc/scpd.xml</SCPDURL>
<controlURL>/rc/control</controlURL>
<eventURL>/rc/event</eventURL>
</service>
</serviceList>
</device>
</root>`)
}

var tagRe = regexp.MustCompile(`<(\w+)>([^<]*)</\w+>`)

// serveControl 处理 AVTransport / RenderingControl 的 SOAP 动作。
// 只实现投屏链路用到的子集，未实现的动作返回 500（模拟老电视的挑食）。
func (t *TV) serveControl(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	action := ""
	if sa := r.Header.Get("SOAPACTION"); sa != "" {
		if i := strings.LastIndex(sa, "#"); i >= 0 {
			action = strings.Trim(sa[i+1:], `"`)
		}
	}
	args := map[string]string{}
	for _, m := range tagRe.FindAllStringSubmatch(string(body), -1) {
		args[m[1]] = m[2]
	}
	var out string
	switch action {
	case "SetAVTransportURI":
		t.mu.Lock()
		t.uri = args["CurrentURI"]
		t.state = "STOPPED"
		t.mu.Unlock()
	case "Play":
		t.startPlayback()
	case "Pause":
		t.mu.Lock()
		if t.state == "PLAYING" {
			t.state = "PAUSED_PLAYBACK"
		}
		t.mu.Unlock()
	case "Stop":
		t.stopPlayback()
	case "Seek":
		t.mu.Lock()
		t.seeks = append(t.seeks, args["Target"])
		t.mu.Unlock()
	case "GetTransportInfo":
		out = "<CurrentTransportState>" + t.State() + "</CurrentTransportState>"
	case "GetPositionInfo":
		out = "<RelTime>" + t.relTime() + "</RelTime><TrackDuration>" + clock(t.duration) + "</TrackDuration>"
	case "GetVolume":
		out = "<CurrentVolume>30</CurrentVolume>"
	case "SetVolume":
		// 记下即可，无需真调音量。
	default:
		http.Error(w, "unsupported action "+action, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:%sResponse xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">%s</u:%sResponse></s:Body></s:Envelope>`, action, out, action)
}

// startPlayback 后台 GET 播放地址并消费流，模拟电视拉流。
// 启用了实时播放时同步喂给 ffplay 弹窗，设置了录制文件时同步写入。
func (t *TV) startPlayback() {
	t.stopPlayback()
	t.mu.Lock()
	uri := t.uri
	t.state = "PLAYING"
	t.playStart = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	t.cancelPlay = cancel
	wantPlayer := t.player
	recordPath := t.recordPath
	// 每次播放起新的录制文件（跳转重播不与上一段混在一起）。
	if recordPath != "" {
		if t.recordFile != nil {
			_ = t.recordFile.Close()
			t.recordFile = nil
		}
		if f, err := os.Create(recordPath); err == nil {
			t.recordFile = f
		}
	}
	t.mu.Unlock()
	if wantPlayer {
		t.launchPlayer()
	}
	if uri == "" {
		return
	}
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
		if err != nil {
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		// 用余量缓冲保证按连续 188 字节审计 TS 同步字节。
		var pending []byte
		tmp := make([]byte, 188*32)
		for {
			c, rerr := resp.Body.Read(tmp)
			if c > 0 {
				chunk := append([]byte(nil), tmp[:c]...)
				pending = append(pending, chunk...)
				var packets, errors int64
				for len(pending) >= 188 {
					if pending[0] == 0x47 {
						packets++
					} else {
						errors++
					}
					pending = pending[188:]
				}
				t.mu.Lock()
				t.bytes += int64(c)
				t.tsPackets += packets
				t.tsErrors += errors
				pw := t.playerIn
				rf := t.recordFile
				t.mu.Unlock()
				// 阻塞写（播放器/磁盘）不能持锁，避免卡住 SOAP 控制。
				if pw != nil {
					if _, werr := pw.Write(chunk); werr != nil {
						t.disablePlayer()
					}
				}
				if rf != nil {
					_, _ = rf.Write(chunk)
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
}

// launchPlayer 启动 ffplay 子进程，把收到的流喂给它的 stdin 实时播放。
// 每次播放新起一个（跳转重播换新流），旧的在 stopPlayback 中已回收。
func (t *TV) launchPlayer() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.killPlayerLocked()
	cmd := exec.Command("ffplay", "-hide_banner", "-loglevel", "error",
		"-window_title", "FakeTV", "-autoexit", "-i", "pipe:0")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	// 播放窗口日志直接透出，方便看到解码报错。
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return
	}
	t.playerCmd = cmd
	t.playerIn = stdin
	go func() { _ = cmd.Wait() }()
}

// disablePlayer 播放器写入失败时（用户关了窗口）停喂但不断流。
func (t *TV) disablePlayer() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.playerIn != nil {
		_ = t.playerIn.Close()
		t.playerIn = nil
	}
}

// killPlayerLocked 回收播放器进程；调用方须持有 t.mu。
func (t *TV) killPlayerLocked() {
	if t.playerIn != nil {
		_ = t.playerIn.Close()
		t.playerIn = nil
	}
	if t.playerCmd != nil && t.playerCmd.Process != nil {
		_ = t.playerCmd.Process.Kill()
		_, _ = t.playerCmd.Process.Wait()
		t.playerCmd = nil
	}
}

// stopPlayback 停止后台拉流、播放器与录制，并置状态为 STOPPED。
func (t *TV) stopPlayback() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancelPlay != nil {
		t.cancelPlay()
		t.cancelPlay = nil
	}
	t.killPlayerLocked()
	if t.recordFile != nil {
		_ = t.recordFile.Close()
		t.recordFile = nil
	}
	t.state = "STOPPED"
}

func (t *TV) relTime() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != "PLAYING" {
		return clock(0)
	}
	el := time.Since(t.playStart).Seconds()
	if t.duration > 0 && el > t.duration {
		el = t.duration
	}
	return clock(el)
}

func clock(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	h := int(sec) / 3600
	m := int(sec) % 3600 / 60
	s := int(sec) % 60
	return fmt.Sprintf("%d:%02d:%02d", h, m, s)
}

// ssdpResponder 应答 M-SEARCH 搜索。
func (t *TV) ssdpResponder() {
	defer close(t.ssdpDone)
	conn, err := net.ListenMulticastUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("239.255.255.250"), Port: 1900})
	if err != nil {
		return
	}
	defer conn.Close()
	go func() {
		<-t.ssdpStop
		_ = conn.Close()
	}()
	buf := make([]byte, 2048)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		msg := string(buf[:n])
		if !strings.Contains(msg, "M-SEARCH") {
			continue
		}
		resp := "HTTP/1.1 200 OK\r\n" +
			"CACHE-CONTROL: max-age=1800\r\n" +
			"LOCATION: " + t.DescriptionURL() + "\r\n" +
			"SERVER: FakeTV/1.0 UPnP/1.0\r\n" +
			"ST: urn:schemas-upnp-org:device:MediaRenderer:1\r\n" +
			"USN: " + t.usn + "\r\n\r\n"
		_, _ = conn.WriteToUDP([]byte(resp), addr)
	}
}

// sendNotify 主动广播上线。
func (t *TV) sendNotify() {
	addr, err := net.ResolveUDPAddr("udp4", "239.255.255.250:1900")
	if err != nil {
		return
	}
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()
	msg := "NOTIFY * HTTP/1.1\r\n" +
		"HOST: 239.255.255.250:1900\r\n" +
		"NT: urn:schemas-upnp-org:device:MediaRenderer:1\r\n" +
		"NTS: ssdp:alive\r\n" +
		"LOCATION: " + t.DescriptionURL() + "\r\n" +
		"USN: " + t.usn + "\r\n\r\n"
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, _ = conn.Write([]byte(msg))
}
