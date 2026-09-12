package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"AnyDLNA/internal/dlna"
	"AnyDLNA/internal/media"
	"AnyDLNA/internal/netutil"
)

// DeviceInfo 是展示给前端的设备条目。
type DeviceInfo struct {
	UDN   string `json:"udn"`
	Name  string `json:"name"`
	Model string `json:"model"`
	Host  string `json:"host"`
}

// PickedVideo 是前端选择的视频及其探测结果。
type PickedVideo struct {
	Path        string  `json:"path"`
	Name        string  `json:"name"`
	DurationSec float64 `json:"durationSec"`
	VideoCodec  string  `json:"videoCodec"`
	AudioCodec  string  `json:"audioCodec"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	SizeMB      float64 `json:"sizeMB"`
	DirectPlay  bool    `json:"directPlay"`
}

// CastStatus 描述当前投屏状态。
type CastStatus struct {
	Active bool   `json:"active"`
	Device string `json:"device"`
	File   string `json:"file"`
	Mode   string `json:"mode"` // direct=原文件直出，transcode=本地文件转码，stream=在线视频中转
}

// ResolvedInfo 是在线视频 URL 的解析预览。
type ResolvedInfo struct {
	URL         string  `json:"url"`
	Title       string  `json:"title"`
	DurationSec float64 `json:"durationSec"`
	IsLive      bool    `json:"isLive"`
	Extractor   string  `json:"extractor"`
	Uploader    string  `json:"uploader"`
}

// Position 是电视端播放进度快照。
type Position struct {
	PositionSec float64 `json:"positionSec"`
	DurationSec float64 `json:"durationSec"`
	State       string  `json:"state"`
}

// App 是绑定给前端的应用层。
type App struct {
	ctx           context.Context
	streamSrv     *media.StreamServer
	watcherCancel context.CancelFunc

	mu        sync.Mutex
	devices   []*dlna.Device // 已发现的渲染设备（搜索结果 + 被动监听累积）
	renderer  *dlna.Renderer // 当前投屏目标
	sessionID string         // 当前流会话 ID
	castFile  string
	castMode  string
}

// NewApp 创建应用实例。
func NewApp() *App { return &App{} }

// startup 在应用启动时创建流服务并开启设备广播常驻监听。
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	srv, err := media.NewStreamServer()
	if err != nil {
		runtime.LogErrorf(ctx, "启动流服务失败: %v", err)
		return
	}
	a.streamSrv = srv

	watchCtx, cancel := context.WithCancel(context.Background())
	a.watcherCancel = cancel
	if err := dlna.WatchRenderers(watchCtx, func(dev *dlna.Device) {
		a.mu.Lock()
		isNew := !a.hasDeviceLocked(dev.UDN)
		if isNew {
			a.devices = append(a.devices, dev)
		}
		a.mu.Unlock()
		if isNew {
			runtime.EventsEmit(a.ctx, "device:discovered", deviceInfoOf(dev))
		}
	}); err != nil {
		runtime.LogWarningf(ctx, "设备广播监听未启动（不影响手动搜索）: %v", err)
	}
}

// shutdown 在应用退出时清理流服务、监听与转码进程。
func (a *App) shutdown(ctx context.Context) {
	if a.watcherCancel != nil {
		a.watcherCancel()
	}
	if a.streamSrv != nil {
		a.streamSrv.Close()
	}
}

// deviceInfoOf 转换设备为前端展示结构。
func deviceInfoOf(d *dlna.Device) DeviceInfo {
	name := d.FriendlyName
	if name == "" {
		name = d.UDN
	}
	return DeviceInfo{
		UDN:   d.UDN,
		Name:  name,
		Model: strings.TrimSpace(d.Manufacturer + " " + d.ModelName),
		Host:  hostOf(d.Location),
	}
}

// SearchDevices 主动搜索局域网内的 DLNA 渲染设备，并合并常驻监听已发现的设备。
func (a *App) SearchDevices(timeoutMS int) ([]DeviceInfo, error) {
	if timeoutMS <= 0 || timeoutMS > 15000 {
		timeoutMS = 6000
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	devices, err := dlna.DiscoverRenderers(ctx, time.Duration(timeoutMS)*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("搜索设备失败: %w", err)
	}

	a.mu.Lock()
	merged := make([]*dlna.Device, 0, len(devices)+len(a.devices))
	seen := map[string]bool{}
	for _, d := range devices {
		if !seen[d.UDN] {
			seen[d.UDN] = true
			merged = append(merged, d)
		}
	}
	for _, d := range a.devices {
		if !seen[d.UDN] {
			seen[d.UDN] = true
			merged = append(merged, d)
		}
	}
	a.devices = merged
	a.mu.Unlock()

	out := make([]DeviceInfo, 0, len(merged))
	for _, d := range merged {
		out = append(out, deviceInfoOf(d))
	}
	return out, nil
}

// AddDeviceManually 在 SSDP 发现失效时按 IP:端口 手动添加渲染设备。
// 不带端口时自动尝试常见 UPnP 描述端口。添加后与搜索结果同等可投屏。
func (a *App) AddDeviceManually(host string) (*DeviceInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dev, err := dlna.DescribeByHost(ctx, host)
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	if !a.hasDeviceLocked(dev.UDN) {
		a.devices = append(a.devices, dev)
	}
	a.mu.Unlock()

	info := deviceInfoOf(dev)
	return &info, nil
}

// hasDeviceLocked 判断设备是否已在最近一次结果中；调用方须持有 a.mu。
func (a *App) hasDeviceLocked(udn string) bool {
	for _, d := range a.devices {
		if d.UDN == udn {
			return true
		}
	}
	return false
}

// PickVideo 弹出文件选择框并探测所选视频。
func (a *App) PickVideo() (*PickedVideo, error) {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择要投屏的视频",
		Filters: []runtime.FileFilter{
			{DisplayName: "视频文件", Pattern: "*.mp4;*.m4v;*.mkv;*.mov;*.avi;*.wmv;*.flv;*.webm;*.ts;*.mts;*.m2ts;*.mpg;*.mpeg;*.vob;*.3gp;*.rm;*.rmvb;*.ogv;*.divx"},
			{DisplayName: "所有文件", Pattern: "*.*"},
		},
	})
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil // 用户取消
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	info, err := media.Probe(ctx, path)
	if err != nil {
		return nil, err
	}
	return &PickedVideo{
		Path:        path,
		Name:        info.Title,
		DurationSec: info.DurationSec,
		VideoCodec:  strings.ToUpper(info.VideoCodec),
		AudioCodec:  strings.ToUpper(info.AudioCodec),
		Width:       info.Width,
		Height:      info.Height,
		SizeMB:      float64(info.SizeBytes) / 1024 / 1024,
		DirectPlay:  !info.NeedsTranscode(),
	}, nil
}

// Cast 把视频投到指定设备：注册流会话并通过 AVTransport 下发播放。
func (a *App) Cast(udn, path string) (*CastStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	dev := a.findDeviceLocked(udn)
	if dev == nil {
		return nil, errors.New("设备未找到，请重新搜索")
	}
	if a.streamSrv == nil {
		return nil, errors.New("流服务未启动")
	}
	if !media.HasFFmpeg() {
		// 无 ffmpeg 时只能直出，不兼容的格式无法保证可播。
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		info, probeErr := media.Probe(ctx, path)
		cancel()
		if probeErr != nil || info.NeedsTranscode() {
			return nil, errors.New("未安装 ffmpeg，无法转码该格式；请执行 brew install ffmpeg")
		}
	}

	// 停掉上一次投屏。
	a.stopCastLocked()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	info, err := media.Probe(ctx, path)
	if err != nil {
		return nil, err
	}
	transcode := info.NeedsTranscode()

	var sessionID, mime, mode string
	if transcode {
		sessionID = a.streamSrv.AddTranscode(path, info.Title)
		mime, mode = "video/mp2t", "transcode"
	} else {
		sessionID = a.streamSrv.AddDirect(path, media.MimeTypeFor(path), info.Title)
		mime, mode = media.MimeTypeFor(path), "direct"
	}
	ip, err := netutil.LANIP()
	if err != nil {
		a.streamSrv.Remove(sessionID)
		return nil, fmt.Errorf("获取本机局域网地址失败: %w", err)
	}
	playURL := a.streamSrv.URL(ip, sessionID, !transcode)
	return a.startCastLocked(dev, sessionID, info.Title, playURL, mime, mode, ctx)
}

// ResolveURL 解析在线视频页面地址，返回标题、时长等预览信息。
func (a *App) ResolveURL(rawURL string) (*ResolvedInfo, error) {
	if !media.HasYtDlp() {
		return nil, errors.New("未安装 yt-dlp，无法解析在线视频；请执行 brew install yt-dlp")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r, err := media.Resolve(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	return &ResolvedInfo{
		URL:         rawURL,
		Title:       r.Title,
		DurationSec: r.DurationSec,
		IsLive:      r.IsLive,
		Extractor:   r.Extractor,
		Uploader:    r.Uploader,
	}, nil
}

// CastURL 把在线视频（YouTube、Bilibili 等视频网站页面或流地址）
// 经本机 yt-dlp 拉流 + ffmpeg 转码中转后投到指定设备。
func (a *App) CastURL(udn, rawURL string) (*CastStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	dev := a.findDeviceLocked(udn)
	if dev == nil {
		return nil, errors.New("设备未找到，请重新搜索")
	}
	if a.streamSrv == nil {
		return nil, errors.New("流服务未启动")
	}
	if !media.HasYtDlp() {
		return nil, errors.New("未安装 yt-dlp，无法解析在线视频；请执行 brew install yt-dlp")
	}
	if !media.HasFFmpeg() {
		return nil, errors.New("未安装 ffmpeg，无法转码在线视频；请执行 brew install ffmpeg")
	}

	// 停掉上一次投屏。
	a.stopCastLocked()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resolved, err := media.Resolve(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	sessionID := a.streamSrv.AddTranscodeURL(rawURL, resolved.Title, resolved.IsLive)
	ip, err := netutil.LANIP()
	if err != nil {
		a.streamSrv.Remove(sessionID)
		return nil, fmt.Errorf("获取本机局域网地址失败: %w", err)
	}
	playURL := a.streamSrv.URL(ip, sessionID, false)
	return a.startCastLocked(dev, sessionID, resolved.Title, playURL, "video/mp2t", "stream", ctx)
}

// findDeviceLocked 按 UDN 在最近一次搜索结果中查找设备；调用方须持有 a.mu。
func (a *App) findDeviceLocked(udn string) *dlna.Device {
	for _, d := range a.devices {
		if d.UDN == udn {
			return d
		}
	}
	return nil
}

// startCastLocked 向设备下发播放地址并登记投屏状态；失败时回收会话。调用方须持有 a.mu。
func (a *App) startCastLocked(dev *dlna.Device, sessionID, title, playURL, streamMIME, mode string, ctx context.Context) (*CastStatus, error) {
	renderer := dlna.NewRenderer(dev)
	if err := renderer.SetAVTransportURI(ctx, playURL, dlna.BuildDIDLMetadata(title, playURL, streamMIME)); err != nil {
		a.streamSrv.Remove(sessionID)
		return nil, fmt.Errorf("下发播放地址失败: %w", err)
	}
	if err := renderer.Play(ctx); err != nil {
		a.streamSrv.Remove(sessionID)
		return nil, fmt.Errorf("启动播放失败: %w", err)
	}
	a.renderer = renderer
	a.sessionID = sessionID
	a.castFile = title
	a.castMode = mode
	return &CastStatus{Active: true, Device: dev.FriendlyName, File: title, Mode: mode}, nil
}

// PlayPause 切换播放/暂停。
func (a *App) PlayPause() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.renderer == nil {
		return errors.New("当前没有投屏")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	state, err := a.renderer.TransportState(ctx)
	if err == nil && state == "PLAYING" {
		return a.renderer.Pause(ctx)
	}
	return a.renderer.Play(ctx)
}

// StopCast 停止投屏并清理流会话。
func (a *App) StopCast() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.renderer == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	_ = a.renderer.Stop(ctx)
	cancel()
	a.stopCastLocked()
	return nil
}

// stopCastLocked 清理当前投屏状态；调用方须持有 a.mu。
func (a *App) stopCastLocked() {
	if a.sessionID != "" && a.streamSrv != nil {
		a.streamSrv.Remove(a.sessionID)
	}
	a.renderer = nil
	a.sessionID = ""
	a.castFile = ""
	a.castMode = ""
}

// SeekTo 跳转到指定秒数。
func (a *App) SeekTo(sec float64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.renderer == nil {
		return errors.New("当前没有投屏")
	}
	if sec < 0 {
		sec = 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if a.castMode == "direct" {
		return a.renderer.Seek(ctx, dlna.SeekUnitABSTime, dlna.FormatClock(sec))
	}
	// 转码流（本地转码/在线中转）不支持随机拖动：让转码进程从新位置重启，
	// 并让电视端重新拉流。
	if err := a.streamSrv.SetTranscodeOffset(a.sessionID, sec); err != nil {
		return err
	}
	playURL := a.rebuildURLLocked(sec)
	metadata := dlna.BuildDIDLMetadata(a.castFile, playURL, "video/mp2t")
	if err := a.renderer.SetAVTransportURI(ctx, playURL, metadata); err != nil {
		return fmt.Errorf("重新下发播放地址失败: %w", err)
	}
	return a.renderer.Play(ctx)
}

// rebuildURLLocked 重建当前会话的拉流 URL，附加 t 参数促使电视端视作新资源。
func (a *App) rebuildURLLocked(sec float64) string {
	ip, err := netutil.LANIP()
	if err != nil {
		return ""
	}
	base := a.streamSrv.URL(ip, a.sessionID, false)
	return base + "?t=" + fmt.Sprintf("%d", int(sec))
}

// Poll 拉取电视端当前播放状态与进度，供前端轮询。
func (a *App) Poll() (*Position, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.renderer == nil {
		return &Position{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pos := &Position{State: "STOPPED"}
	state, err := a.renderer.TransportState(ctx)
	if err != nil {
		return pos, nil // 设备暂时无响应不算致命，前端下次再试。
	}
	pos.State = state
	relTime, duration, err := a.renderer.PositionInfo(ctx)
	if err == nil {
		if v, perr := dlna.ParseClock(relTime); perr == nil {
			pos.PositionSec = v
		}
		if v, perr := dlna.ParseClock(duration); perr == nil {
			pos.DurationSec = v
		}
	}
	return pos, nil
}

// GetVolume 获取电视端音量（0-100）。
func (a *App) GetVolume() (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.renderer == nil {
		return 0, errors.New("当前没有投屏")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return a.renderer.GetVolume(ctx)
}

// SetVolume 设置电视端音量（0-100）。
func (a *App) SetVolume(volume int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.renderer == nil {
		return errors.New("当前没有投屏")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return a.renderer.SetVolume(ctx, volume)
}

// CastStatus 返回当前投屏状态快照。
func (a *App) GetCastStatus() *CastStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.renderer == nil {
		return &CastStatus{}
	}
	return &CastStatus{Active: true, Device: a.renderer.Device().FriendlyName, File: a.castFile, Mode: a.castMode}
}

// hostOf 从 URL 提取主机:端口。
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}
