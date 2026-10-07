package main

import (
	"AnyDLNA/internal/dlna"
	"AnyDLNA/internal/media"
	"AnyDLNA/internal/netutil"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
	// Mode 是预计的输出方式：direct / remux / transcode。
	Mode string `json:"mode"`
	// FastStart 表示 MP4 的索引是否在文件开头；false 时无法边下边播，
	// 会改用换封装以立即起播。
	FastStart bool `json:"fastStart"`
}

// CastStatus 描述当前投屏状态。
type CastStatus struct {
	Active bool   `json:"active"`
	Device string `json:"device"`
	File   string `json:"file"`
	// Mode 取值：direct=原文件直出，remux=换封装（不重编码视频），
	// transcode=完整转码，stream=在线视频中转。
	Mode string `json:"mode"`
}

// ResolvedInfo 是在线视频 URL 的解析预览。
type ResolvedInfo struct {
	URL         string  `json:"url"`
	Title       string  `json:"title"`
	DurationSec float64 `json:"durationSec"`
	IsLive      bool    `json:"isLive"`
	Extractor   string  `json:"extractor"`
	Uploader    string  `json:"uploader"`
	VideoCodec  string  `json:"videoCodec"`
	AudioCodec  string  `json:"audioCodec"`
	// Mode 是预计的输出方式：remux（免转码）或 transcode。
	Mode string `json:"mode"`
}

// Position 是电视端播放进度快照。
type Position struct {
	PositionSec float64 `json:"positionSec"`
	DurationSec float64 `json:"durationSec"`
	State       string  `json:"state"`
}

// PickVideo 弹出文件选择框并探测所选视频。
// udn 为当前选中的设备；用于按其声明的能力给出真实的输出方案（可为空）。
func (a *App) PickVideo(udn string) (*PickedVideo, error) {
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
	info, err := media.CachedProbe(ctx, path)
	if err != nil {
		return nil, err
	}
	plan := media.PlanForLocal(info, a.capabilitiesFor(udn))
	return &PickedVideo{
		Path:        path,
		Name:        info.Title,
		DurationSec: info.DurationSec,
		VideoCodec:  strings.ToUpper(info.VideoCodec),
		AudioCodec:  strings.ToUpper(info.AudioCodec),
		Width:       info.Width,
		Height:      info.Height,
		SizeMB:      float64(info.SizeBytes) / 1024 / 1024,
		Mode:        string(plan.Mode),
		// FastStart 为 false 时说明该 MP4 索引在末尾，无法边下边播。
		FastStart: info.FastStart,
	}, nil
}

// Cast 把本地视频投到指定设备：注册流会话并通过 AVTransport 下发播放。
// titleOverride 为本次投屏的临时显示名称（空串=按全局设置），优先于设置。
func (a *App) Cast(udn, path, titleOverride string) (*CastStatus, error) {
	a.castMu.Lock()
	defer a.castMu.Unlock()

	dev, srv, err := a.castTarget(udn)
	if err != nil {
		return nil, err
	}
	// 先问设备支持什么，再决定怎么投——这一步决定能否免转码。
	caps := a.deviceCapabilities(dev)

	if !media.HasFFmpeg() {
		// 无 ffmpeg 时只能投递原文件字节：换封装与转码都需要 ffmpeg。
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		info, probeErr := media.CachedProbe(ctx, path)
		cancel()
		if probeErr != nil || !media.PlanForLocal(info, caps).IsDirect() {
			return nil, fmt.Errorf("%w；当前只能直接播放设备可解码的原文件", media.MissingToolError("ffmpeg"))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	info, err := media.CachedProbe(ctx, path)
	if err != nil {
		return nil, err
	}
	plan := media.PlanForLocal(info, caps)
	title := a.effectiveCastTitle(info.Title, titleOverride)

	var sessionID, mime, mode string
	switch plan.Mode {
	case media.OutputDirect:
		sessionID = srv.AddDirect(path, plan.OutputMIME(), title)
		mime, mode = plan.OutputMIME(), string(plan.Mode)
	default:
		sessionID = srv.AddTranscode(path, title, plan)
		mime, mode = plan.OutputMIME(), string(plan.Mode)
	}
	ip, err := netutil.LANIP()
	if err != nil {
		srv.Remove(sessionID)
		return nil, fmt.Errorf("获取本机局域网地址失败: %w", err)
	}
	playURL := srv.URL(ip, sessionID, plan.IsDirect())
	return a.startCast(dev, srv, sessionID, title, playURL, mime, mode, ctx)
}

// ResolveURL 解析在线视频页面地址，返回标题、时长等预览信息。
// udn 为当前选中的设备，用于按其声明的能力给出真实方案（可为空）。
func (a *App) ResolveURL(udn, rawURL string) (*ResolvedInfo, error) {
	if !media.HasYtDlp() {
		return nil, fmt.Errorf("%w；在线视频解析需要它", media.MissingToolError("yt-dlp"))
	}
	opts := a.resolveOptions()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	r, err := media.Resolve(ctx, rawURL, opts)
	if err != nil {
		return nil, err
	}
	plan := media.PlanForOnline(r.VideoCodec, r.AudioCodec, a.capabilitiesFor(udn))
	return &ResolvedInfo{
		URL:         rawURL,
		Title:       r.Title,
		DurationSec: r.DurationSec,
		IsLive:      r.IsLive,
		Extractor:   r.Extractor,
		Uploader:    r.Uploader,
		VideoCodec:  r.VideoCodec,
		AudioCodec:  r.AudioCodec,
		Mode:        string(plan.Mode),
	}, nil
}

// resolveOptions 读取当前配置并解析为 yt-dlp 所需参数。
func (a *App) resolveOptions() media.Options {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	return cfg.ResolveOptions()
}

// castDisplayTitle 计算投屏时下发给电视端显示的标题（DIDL 的 dc:title）。
// 配置了投屏显示名称时以其为准：内容中的 {title} 占位符替换为实际标题，
// 不含占位符则整体作为固定名称；未配置时保持原标题。
func (a *App) castDisplayTitle(defaultTitle string) string {
	a.mu.Lock()
	tpl := strings.TrimSpace(a.cfg.CastTitle)
	a.mu.Unlock()
	if tpl == "" {
		return defaultTitle
	}
	return strings.ReplaceAll(tpl, "{title}", defaultTitle)
}

// effectiveCastTitle 结合全局设置与本次投屏的临时覆盖得出最终显示标题：
// 覆盖非空（去首尾空格）时整体生效，优先于全局设置；否则按 castDisplayTitle。
func (a *App) effectiveCastTitle(defaultTitle, override string) string {
	if t := strings.TrimSpace(override); t != "" {
		return t
	}
	return a.castDisplayTitle(defaultTitle)
}

// castTarget 取出投屏目标设备与流服务，校验可用性。
func (a *App) castTarget(udn string) (*dlna.Device, *media.StreamServer, error) {
	a.mu.Lock()
	dev := a.findDeviceLocked(udn)
	srv := a.streamSrv
	a.mu.Unlock()

	if dev == nil {
		return nil, nil, errors.New("设备未找到，请重新搜索")
	}
	if srv == nil {
		return nil, nil, errors.New("流服务未启动")
	}
	return dev, srv, nil
}

// CastURL 把在线视频（YouTube、Bilibili 等视频网站页面或流地址）
// 经本机 yt-dlp 拉流 + ffmpeg 转码中转后投到指定设备。
// titleOverride 为本次投屏的临时显示名称（空串=按全局设置），优先于设置。
//
// 注意：yt-dlp 解析可能耗时数十秒，期间绝不能持有 a.mu，
// 否则所有前端 IPC（轮询、状态读取）都会被阻塞。
func (a *App) CastURL(udn, rawURL, titleOverride string) (*CastStatus, error) {
	a.castMu.Lock()
	defer a.castMu.Unlock()

	dev, srv, err := a.castTarget(udn)
	if err != nil {
		return nil, err
	}
	if !media.HasYtDlp() {
		return nil, fmt.Errorf("%w；在线视频解析需要它", media.MissingToolError("yt-dlp"))
	}
	if !media.HasFFmpeg() {
		return nil, fmt.Errorf("%w；在线视频无法免转码时需要用它在本地处理", media.MissingToolError("ffmpeg"))
	}

	caps := a.deviceCapabilities(dev)
	opts := a.resolveOptions()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// 一次 -J 同时拿元数据与直链（见 ResolveDirect），不再单独调 -g：
	// 慢代理下每次 yt-dlp 调用都可能是几十秒，两次串行就是失败翻倍。
	// socks 代理不再拦截：上游拉取已是 Go 原生 http.Client，
	// http/https 与 socks5 都支持（见 newProxyHTTPClient）。
	resolved, urls, err := media.ResolveDirect(ctx, rawURL, opts)
	if err != nil {
		media.Diagf("投屏失败 解析 url=%.80s err=%v", rawURL, err)
		return nil, err
	}
	media.Diagf("投屏解析 url=%.80s 标题=%.40s 时长=%.0fs 直播=%v 站点=%s 直链=%d条",
		rawURL, resolved.Title, resolved.DurationSec, resolved.IsLive, resolved.Extractor, len(urls))
	plan := media.PlanForOnline(resolved.VideoCodec, resolved.AudioCodec, caps)
	title := a.effectiveCastTitle(resolved.Title, titleOverride)
	sessionID, err := srv.AddTranscodeURL(ctx, rawURL, title, resolved.IsLive, opts, plan, resolved.Extractor, urls)
	if err != nil {
		media.Diagf("投屏失败 预热 会话=%s err=%v", sessionID, err)
		return nil, err
	}
	ip, err := netutil.LANIP()
	if err != nil {
		srv.Remove(sessionID)
		return nil, fmt.Errorf("获取本机局域网地址失败: %w", err)
	}
	playURL := srv.URL(ip, sessionID, false)
	status, err := a.startCast(dev, srv, sessionID, title, playURL, plan.OutputMIME(), string(plan.Mode), ctx)
	if err != nil {
		media.Diagf("投屏失败 下发 设备=%s err=%v", dev.FriendlyName, err)
		return nil, err
	}
	media.Diagf("投屏成功 设备=%s 标题=%.40s 模式=%s", dev.FriendlyName, resolved.Title, string(plan.Mode))
	return status, nil
}

// startCast 向设备下发播放地址并登记投屏状态；失败时回收会话。
// 调用方须持有 a.castMu，且不得持有 a.mu。
func (a *App) startCast(dev *dlna.Device, srv *media.StreamServer, sessionID, title, playURL, streamMIME, mode string, ctx context.Context) (*CastStatus, error) {
	// 先停掉上一次投屏（回收在 a.mu 之外完成）。
	a.stopCast()

	renderer := dlna.NewRenderer(dev)
	if err := renderer.SetAVTransportURI(ctx, playURL, dlna.BuildDIDLMetadata(title, playURL, streamMIME)); err != nil {
		srv.Remove(sessionID)
		return nil, fmt.Errorf("下发播放地址失败: %w", err)
	}
	if err := renderer.Play(ctx); err != nil {
		srv.Remove(sessionID)
		return nil, fmt.Errorf("启动播放失败: %w", err)
	}

	a.mu.Lock()
	a.renderer = renderer
	a.sessionID = sessionID
	a.castFile = title
	a.castMode = mode
	a.mu.Unlock()

	a.saveCastState(dev.UDN, dev.Location, title, mode)

	return &CastStatus{Active: true, Device: dev.FriendlyName, File: title, Mode: mode}, nil
}

// stopCast 清理当前投屏状态并回收流会话。
// 会话回收会等待转码进程退出，因此必须在释放 a.mu 之后进行。
func (a *App) stopCast() {
	a.mu.Lock()
	sessionID := a.sessionID
	srv := a.streamSrv
	a.renderer = nil
	a.sessionID = ""
	a.castFile = ""
	a.castMode = ""
	a.mu.Unlock()

	if sessionID != "" && srv != nil {
		srv.Remove(sessionID)
	}
	a.clearCastState()
}

// ---------- 投屏会话快照：重启后恢复控制 ----------

// castSessionState 是投屏会话的持久化快照，供应用重启后尽力恢复控制。
type castSessionState struct {
	UDN      string `json:"udn"`
	Location string `json:"location"` // 设备描述地址，重启后据此重新拉取
	Title    string `json:"title"`
	Mode     string `json:"mode"`
}

// castStatePath 返回投屏会话快照文件路径。
func castStatePath() (string, error) {
	dir, err := media.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cast_session.json"), nil
}

// saveCastState 持久化当前投屏会话。写失败只记日志：恢复是尽力而为的
// 增强能力，不应反过来影响投屏本身。
func (a *App) saveCastState(udn, location, title, mode string) {
	path, err := castStatePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(castSessionState{UDN: udn, Location: location, Title: title, Mode: mode})
	if err != nil {
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		a.logf("投屏状态落盘失败: %v", err)
	}
}

// clearCastState 删除投屏会话快照（投屏结束/清理时）。
func (a *App) clearCastState() {
	if path, err := castStatePath(); err == nil {
		_ = os.Remove(path)
	}
}

// restoreCast 尝试恢复上次投屏的控制能力。重启后本机流服务端口已变，
// 电视端继续拉流会失败，但 AVTransport 控制（暂停/停止/音量）独立于流地址：
// 恢复后至少能看到投屏内容并暂停/停止，而不是留下一个无法控制的「僵尸投屏」。
// 设备描述拉取失败（已关机/网络不可达）时清除快照，静默结束。
func (a *App) restoreCast() {
	path, err := castStatePath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var st castSessionState
	if err := json.Unmarshal(data, &st); err != nil || st.UDN == "" || st.Location == "" {
		_ = os.Remove(path)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dev, err := dlna.Describe(ctx, &http.Client{Timeout: 15 * time.Second}, st.Location)
	if err != nil || !dev.HasAVTransport() {
		_ = os.Remove(path)
		return
	}

	a.mu.Lock()
	a.renderer = dlna.NewRenderer(dev)
	a.sessionID = "" // 本机流会话已随重启失效，仅恢复控制面
	a.castFile = st.Title
	a.castMode = st.Mode
	a.mu.Unlock()

	a.logf("恢复上次投屏控制: %s · %s（流已随重启失效，可暂停/停止/调音量）", dev.FriendlyName, st.Title)
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "cast:restored", a.GetCastStatus())
	}
}

// currentRenderer 返回当前渲染器；没有投屏时返回错误。
func (a *App) currentRenderer() (*dlna.Renderer, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.renderer == nil {
		return nil, errors.New("当前没有投屏")
	}
	return a.renderer, nil
}

// PlayPause 切换播放/暂停。
func (a *App) PlayPause() error {
	renderer, err := a.currentRenderer()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	state, err := renderer.TransportState(ctx)
	if err == nil && state == dlna.TransportStatePlaying {
		return renderer.Pause(ctx)
	}
	return renderer.Play(ctx)
}

// StopCast 停止投屏并清理流会话。
func (a *App) StopCast() error {
	a.castMu.Lock()
	defer a.castMu.Unlock()

	renderer, _ := a.currentRenderer()
	if renderer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
		_ = renderer.Stop(ctx)
		cancel()
	}
	a.stopCast()
	return nil
}

// SeekTo 跳转到指定秒数。
func (a *App) SeekTo(sec float64) error {
	a.castMu.Lock()
	defer a.castMu.Unlock()

	renderer, err := a.currentRenderer()
	if err != nil {
		return err
	}
	if sec < 0 {
		sec = 0
	}

	a.mu.Lock()
	srv := a.streamSrv
	sessionID, castFile, castMode := a.sessionID, a.castFile, a.castMode
	a.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if castMode == "direct" {
		return renderer.Seek(ctx, dlna.SeekUnitABSTime, dlna.FormatClock(sec))
	}
	// 转码流（本地转码/在线中转）不支持随机拖动：让转码进程从新位置重启，
	// 并让电视端重新拉流。
	if srv == nil {
		return errors.New("流服务未启动")
	}
	if err := srv.SetTranscodeOffset(sessionID, sec); err != nil {
		return err
	}
	playURL := rebuildURL(srv, sessionID, sec)
	if playURL == "" {
		return errors.New("获取本机局域网地址失败")
	}
	metadata := dlna.BuildDIDLMetadata(castFile, playURL, "video/mp2t")
	if err := renderer.SetAVTransportURI(ctx, playURL, metadata); err != nil {
		media.Diagf("跳转失败 下发 %.1fs err=%v", sec, err)
		return fmt.Errorf("重新下发播放地址失败: %w", err)
	}
	if err := renderer.Play(ctx); err != nil {
		media.Diagf("跳转失败 播放 %.1fs err=%v", sec, err)
		return err
	}
	media.Diagf("跳转成功 %.1fs 文件=%.40s", sec, castFile)
	return nil
}

// rebuildURL 重建当前会话的拉流 URL，附加 t 参数促使电视端视作新资源。
func rebuildURL(srv *media.StreamServer, sessionID string, sec float64) string {
	ip, err := netutil.LANIP()
	if err != nil {
		return ""
	}
	base := srv.URL(ip, sessionID, false)
	return base + "?t=" + fmt.Sprintf("%d", int(sec))
}

// Poll 拉取电视端当前播放状态与进度，供前端轮询。
func (a *App) Poll() (*Position, error) {
	renderer, err := a.currentRenderer()
	if err != nil {
		return &Position{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	pos := &Position{State: "STOPPED"}
	state, err := renderer.TransportState(ctx)
	if err != nil {
		// 设备暂时无响应不算致命，但要与「设备明确停止」区分：
		// 前端只对明确的 STOPPED 连续计数判停并回收会话，
		// 网络抖动若被当作 STOPPED 会误清正在进行的投屏。
		pos.State = "UNKNOWN"
		return pos, nil
	}
	pos.State = state
	relTime, duration, err := renderer.PositionInfo(ctx)
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
	renderer, err := a.currentRenderer()
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	return renderer.GetVolume(ctx)
}

// SetVolume 设置电视端音量（0-100）。
func (a *App) SetVolume(volume int) error {
	renderer, err := a.currentRenderer()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	return renderer.SetVolume(ctx, volume)
}

// GetCastStatus 返回当前投屏状态快照。
func (a *App) GetCastStatus() *CastStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.renderer == nil {
		return &CastStatus{}
	}
	return &CastStatus{Active: true, Device: a.renderer.Device().FriendlyName, File: a.castFile, Mode: a.castMode}
}
