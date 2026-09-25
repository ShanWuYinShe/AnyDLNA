package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"AnyDLNA/internal/browser"
	"AnyDLNA/internal/dlna"
	"AnyDLNA/internal/media"
	"AnyDLNA/internal/netutil"
)

// logf 输出应用诊断日志。
//
// 刻意使用标准库而非 Wails runtime：runtime.Log* 在传入的上下文不是
// Wails 生命周期上下文时会直接 log.Fatalf 终止进程。诊断信息不该有这种
// 后果——例如在测试或任何非 Wails 环境下调用时，应用不应因此退出。
// 生命周期钩子（startup/shutdown）里拿到的 ctx 是真实上下文，
// 那两处仍用 runtime 日志以便进入 Wails 的应用日志。
func (a *App) logf(format string, args ...any) { media.Diagf(format, args...) }

// DeviceInfo 是展示给前端的设备条目。
type DeviceInfo struct {
	UDN   string `json:"udn"`
	Name  string `json:"name"`
	Model string `json:"model"`
	Host  string `json:"host"`
	// Offline 表示设备连续多轮搜索未应答。仅置灰提示、不删除条目：
	// 部分设备平时不应答搜索但仍可投屏，用户手动添加的设备尤其如此。
	Offline bool `json:"offline"`
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

// App 是绑定给前端的应用层。
//
// 锁纪律（避免再次出现「点了没反应」的死锁）：
//   - a.mu 只保护内存状态的读写，临界区内绝不做网络、进程或等待用户的操作；
//   - a.castMu 串行化投屏相关操作（投屏/停止/跳转），耗时 I/O 在持有
//     castMu 但不持有 a.mu 的情况下执行；
//   - a.searchMu 串行化主动搜索，避免定时搜索与手动搜索同时占用网络。
type App struct {
	ctx           context.Context
	streamSrv     *media.StreamServer
	watcherCancel context.CancelFunc
	browserMgr    *browser.Manager

	castMu   sync.Mutex // 串行化投屏操作，长耗时步骤不持有 a.mu
	searchMu sync.Mutex // 串行化主动搜索，长耗时步骤不持有 a.mu

	mu         sync.Mutex
	cfg        media.Config   // 在线视频代理与 Cookies 配置
	devices    []*dlna.Device // 已发现的渲染设备（搜索结果 + 被动监听累积）
	deviceMiss map[string]int // UDN → 连续未应答搜索的轮数（离线判定用）
	renderer   *dlna.Renderer // 当前投屏目标
	sessionID  string         // 当前流会话 ID
	castFile   string
	castMode   string
}

// NewApp 创建应用实例。
func NewApp() *App {
	return &App{cfg: media.DefaultConfig(), browserMgr: browser.NewManager(""), deviceMiss: map[string]int{}}
}

// startup 在应用启动时创建流服务、加载配置并开启设备广播常驻监听。
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.mu.Lock()
	a.cfg = media.LoadConfig()
	a.mu.Unlock()

	// 文件日志先行：GUI 的 stdout 用户看不见，之后所有 logf/标准 log
	// 都落到数据目录的日志文件，出问题直接看文件。
	if path, err := media.InitDiagLog(); err != nil {
		runtime.LogWarningf(ctx, "诊断日志初始化失败（仅影响落盘）: %v", err)
	} else {
		runtime.LogInfof(ctx, "诊断日志: %s", path)
	}

	// 启动自检：记录外部工具的实际解析结果。GUI 应用不继承 shell 的 PATH，
	// 工具「装了却找不到」是最容易误判的一类问题，这里留下可核对的依据。
	a.logf("外部工具: %s", media.ToolStatus())

	// 清理上次异常退出残留的上游缓存临时文件（正常路径在投屏结束时删除）。
	media.CleanUpstreamTemps()

	srv, err := media.NewStreamServer()
	if err != nil {
		runtime.LogErrorf(ctx, "启动流服务失败: %v", err)
		return
	}
	a.streamSrv = srv

	watchCtx, cancel := context.WithCancel(context.Background())
	a.watcherCancel = cancel

	// 被动监听：设备主动广播 SSDP alive 时立刻出现。
	if err := dlna.WatchRenderers(watchCtx, func(dev *dlna.Device) {
		a.mergeDevices([]*dlna.Device{dev}, true)
	}); err != nil {
		runtime.LogWarningf(ctx, "设备广播监听未启动（不影响定时与手动搜索）: %v", err)
	}

	// 定时主动搜索：不少电视（含实测的目标设备）平时不广播、只应答搜索，
	// 仅靠被动监听无法在设备开机后自动发现，因此需要周期性主动搜索兜底。
	go a.watchDevices(watchCtx)

	// 尽力恢复上次投屏的控制（按持久化的设备描述地址重新拉取，网络 I/O 不阻塞启动）。
	go a.restoreCast()
}

// shutdown 在应用退出时清理流服务、监听、浏览器与转码进程。
func (a *App) shutdown(ctx context.Context) {
	if a.watcherCancel != nil {
		a.watcherCancel()
	}
	if a.browserMgr != nil {
		a.browserMgr.Close()
	}
	if a.streamSrv != nil {
		a.streamSrv.Close()
	}
}

// deviceInfoOf 转换新发现（在线）设备为前端展示结构。
func deviceInfoOf(d *dlna.Device) DeviceInfo { return deviceView(d, false) }

// deviceView 转换设备为前端展示结构；offline 标记由调用方按应答情况给出。
func deviceView(d *dlna.Device, offline bool) DeviceInfo {
	name := d.FriendlyName
	if name == "" {
		name = d.UDN
	}
	return DeviceInfo{
		UDN:     d.UDN,
		Name:    name,
		Model:   strings.TrimSpace(d.Manufacturer + " " + d.ModelName),
		Host:    hostOf(d.Location),
		Offline: offline,
	}
}

// offlineAfterMisses 是判定设备离线所需的连续未应答搜索轮数。
// 定时搜索间隔 30 秒，3 轮约 90 秒无应答才置离线，单次丢包不会误判。
// 只置灰不删除：不应答搜索不代表不能投屏（部分电视平时不应答、只接受下发）。
const offlineAfterMisses = 3

// describeBudget 是 SSDP 搜索之后用于抓取并解析设备描述的额外时间预算。
//
// 必须与搜索窗口分开计算：searchDevices 会一直等到超时才返回，
// 若 ctx 的截止时间与搜索窗口相同，随后的描述请求就运行在已过期的
// 上下文上、立即失败，最终表现为「搜索完成但一台设备都没有」。
const describeBudget = 15 * time.Second

// searchBudget 返回主动搜索的总时间预算：SSDP 搜索窗口 + 描述解析预算。
// 单独抽出是为了让「ctx 必须比搜索窗口宽裕」这一约束能被测试固定住。
func searchBudget(timeoutMS int) time.Duration {
	return time.Duration(timeoutMS)*time.Millisecond + describeBudget
}

// deviceSearchInterval 是后台定时主动搜索的间隔。
//
// 被动监听只能等到设备广播 SSDP alive，而不少电视（含本项目实测的目标设备）
// 平时不广播、只应答搜索；没有定时搜索时，设备开机后不会自动出现，
// 用户会以为「搜不到设备」。30 秒是兼顾「开机后能较快出现」与
// 「不频繁占用组播」的取值。
const deviceSearchInterval = 30 * time.Second

// defaultSearchTimeoutMS 是主动搜索的默认窗口，定时与手动搜索共用。
//
// 不为此单独缩短定时搜索的窗口：搜索期间只有最初会发包，其余时间都在等待应答，
// 而 M-SEARCH 声明的 MX 为 3 秒、设备可延迟到此时限才回复，
// 窗口短于 MX 会漏掉守规矩但回复慢的设备。
const defaultSearchTimeoutMS = 6000

// discoverDevices 执行一次主动搜索。
//
// 用 a.searchMu 串行化：定时搜索与手动搜索若并发，会重复发送组播并重复抓取
// 设备描述。这里刻意让后来者等待，从而复用同一次网络动作的结果。
// 注意 searchMu 与 a.mu 不嵌套持有，且整个搜索期间都不持有 a.mu。
func (a *App) discoverDevices(ctx context.Context, window time.Duration) ([]*dlna.Device, error) {
	a.searchMu.Lock()
	defer a.searchMu.Unlock()

	// ctx 的截止时间必须比搜索窗口宽裕，否则随后的描述抓取会立即失败。
	dctx, cancel := context.WithTimeout(ctx, window+describeBudget)
	defer cancel()
	return dlna.DiscoverRenderers(dctx, window)
}

// mergeDevices 把设备并入列表并按 UDN 去重，返回合并后的完整设备视图
// （含离线标记）。已存在的设备刷新描述并清零离线计数——重新应答即恢复在线，
// 状态翻转时推送 device:offline 事件。notify 为真时，对本次新增的设备
// 推送 device:discovered 事件；已存在的设备不会重复推送，
// 因此定时搜索不会反复弹出「发现新设备」。
func (a *App) mergeDevices(devs []*dlna.Device, notify bool) []DeviceInfo {
	var (
		added     []*dlna.Device
		recovered []DeviceInfo
	)

	a.mu.Lock()
	if a.deviceMiss == nil {
		a.deviceMiss = map[string]int{}
	}
	for _, d := range devs {
		if d == nil {
			continue
		}
		if a.hasDeviceLocked(d.UDN) {
			if v := a.refreshDeviceLocked(d); v != nil {
				recovered = append(recovered, *v)
			}
			continue
		}
		a.devices = append(a.devices, d)
		added = append(added, d)
	}
	merged := a.deviceViewsLocked()
	a.mu.Unlock()

	// a.ctx 为 nil 时说明不在 Wails 生命周期内（例如单元测试），此时不推送事件。
	if notify && a.ctx != nil {
		for _, d := range added {
			a.logf("自动发现设备: %s (%s) @ %s", d.FriendlyName, d.UDN, d.Location)
			runtime.EventsEmit(a.ctx, "device:discovered", deviceInfoOf(d))
		}
	}
	for _, v := range recovered {
		a.logf("设备恢复在线: %s (%s)", v.Name, v.UDN)
		a.emitDeviceStatus(v)
	}
	return merged
}

// refreshDeviceLocked 用最新描述覆盖已有设备并清零离线计数；
// 从离线恢复在线时返回其视图供调用方推送状态事件。调用方须持有 a.mu。
func (a *App) refreshDeviceLocked(d *dlna.Device) *DeviceInfo {
	for i, old := range a.devices {
		if old.UDN != d.UDN {
			continue
		}
		wasOffline := a.deviceMiss[d.UDN] >= offlineAfterMisses
		a.devices[i] = d
		delete(a.deviceMiss, d.UDN)
		if wasOffline {
			v := deviceView(d, false)
			return &v
		}
		return nil
	}
	return nil
}

// deviceViewsLocked 构造合并后列表的前端视图；调用方须持有 a.mu。
func (a *App) deviceViewsLocked() []DeviceInfo {
	out := make([]DeviceInfo, 0, len(a.devices))
	for _, d := range a.devices {
		out = append(out, deviceView(d, a.deviceMiss[d.UDN] >= offlineAfterMisses))
	}
	return out
}

// markMissingDevices 对本轮搜索未应答的设备累计离线计数，达到阈值时标记
// 离线并返回状态翻转的设备视图（调用方锁外推送事件）。found 是本轮应答的
// UDN 集合——应答者的计数已由 mergeDevices 清零，这里只处理未应答者。
func (a *App) markMissingDevices(found map[string]bool) []DeviceInfo {
	a.mu.Lock()
	if a.deviceMiss == nil {
		a.deviceMiss = map[string]int{}
	}
	var flipped []DeviceInfo
	for _, d := range a.devices {
		if found[d.UDN] {
			continue
		}
		a.deviceMiss[d.UDN]++
		if a.deviceMiss[d.UDN] == offlineAfterMisses {
			a.logf("设备连续 %d 轮未应答，标记离线: %s (%s)", offlineAfterMisses, d.FriendlyName, d.UDN)
			flipped = append(flipped, deviceView(d, true))
		}
	}
	a.mu.Unlock()
	return flipped
}

// emitDeviceStatus 推送设备在线状态变化；不在 Wails 生命周期内时跳过。
func (a *App) emitDeviceStatus(v DeviceInfo) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "device:offline", v)
}

// watchDevices 周期性主动搜索设备，直到 ctx 结束。
// 与被动监听互补，保证不应答广播、只应答搜索的设备也能自动出现。
func (a *App) watchDevices(ctx context.Context) {
	ticker := time.NewTicker(deviceSearchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			window := time.Duration(defaultSearchTimeoutMS) * time.Millisecond
			devs, err := a.discoverDevices(ctx, window)
			if err != nil {
				a.logf("定时搜索设备失败: %v", err)
				continue
			}
			a.mergeDevices(devs, true)
			// 本轮未应答的设备累计离线计数，状态翻转时通知前端置灰/恢复。
			found := make(map[string]bool, len(devs))
			for _, d := range devs {
				if d != nil {
					found[d.UDN] = true
				}
			}
			for _, v := range a.markMissingDevices(found) {
				a.emitDeviceStatus(v)
			}
		}
	}
}

// SearchDevices 主动搜索局域网内的 DLNA 渲染设备，并合并已发现的设备。
func (a *App) SearchDevices(timeoutMS int) ([]DeviceInfo, error) {
	if timeoutMS <= 0 || timeoutMS > 15000 {
		timeoutMS = defaultSearchTimeoutMS
	}
	devices, err := a.discoverDevices(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("搜索设备失败: %w", err)
	}
	// 记录搜索结果：设备「搜不到」时，可据此区分是网络没响应，
	// 还是响应了但描述解析 / 服务过滤没通过。
	for _, d := range devices {
		a.logf("搜索到设备: %s (%s) @ %s", d.FriendlyName, d.UDN, d.Location)
	}
	a.logf("搜索设备完成：发现 %d 台", len(devices))

	// 手动搜索不推送事件：调用方直接拿到完整列表并自行渲染。
	// （离线状态的翻转由定时搜索路径统一推送，手动搜索不重复计数。）
	return a.mergeDevices(devices, false), nil
}

// DeviceFormats 描述一台设备声明支持的格式，供设置界面展示。
type DeviceFormats struct {
	// Queried 表示是否成功查询到设备能力（设备需提供 ConnectionManager）。
	Queried bool `json:"queried"`
	// SupportsTS / SupportsMP4 / SupportsMKV 是决策时实际关心的三项能力。
	SupportsTS  bool `json:"supportsTs"`
	SupportsMP4 bool `json:"supportsMp4"`
	SupportsMKV bool `json:"supportsMkv"`
	// VideoMIMEs 是设备声明的全部视频格式（去重、排序）。
	VideoMIMEs []string `json:"videoMIMEs"`
}

// DeviceCapabilityInfo 查询并返回指定设备声明的接收能力。
// 用于界面上展示「将按设备实际支持情况决定是否免转码」。
func (a *App) DeviceCapabilityInfo(udn string) *DeviceFormats {
	a.mu.Lock()
	dev := a.findDeviceLocked(udn)
	a.mu.Unlock()

	out := &DeviceFormats{}
	if dev == nil || !dev.HasConnectionManager() {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	caps, err := dlna.NewRenderer(dev).QueryProtocolInfo(ctx)
	if err != nil || !caps.Queried {
		return out
	}
	out.Queried = true
	out.VideoMIMEs = caps.VideoMIMEs()
	out.SupportsTS = caps.SupportsMIME(media.ContainerMPEGTS.MIME())
	out.SupportsMP4 = caps.SupportsMIME(media.ContainerFMP4.MIME())
	out.SupportsMKV = caps.SupportsMIME("video/x-matroska")
	return out
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

// capabilitiesFor 按 UDN 查找设备并查询其声明的接收能力。
// 设备不存在、未提供 ConnectionManager 或查询失败时返回零值（保守回退）。
func (a *App) capabilitiesFor(udn string) media.DeviceCapabilities {
	a.mu.Lock()
	dev := a.findDeviceLocked(udn)
	a.mu.Unlock()
	return a.deviceCapabilities(dev)
}

// deviceCapabilities 查询设备通过 ConnectionManager 声明的接收能力。
// 查询失败不影响投屏，只是回退到通用策略。
func (a *App) deviceCapabilities(dev *dlna.Device) media.DeviceCapabilities {
	if dev == nil || !dev.HasConnectionManager() {
		return media.DeviceCapabilities{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	caps, err := dlna.NewRenderer(dev).QueryProtocolInfo(ctx)
	if err != nil {
		a.logf("查询设备 %s 的格式能力失败，回退到通用策略: %v", dev.FriendlyName, err)
		return media.DeviceCapabilities{}
	}
	if !caps.Queried {
		return media.DeviceCapabilities{}
	}
	mimes := caps.VideoMIMEs()
	a.logf("设备 %s 声明支持 %d 种视频格式", dev.FriendlyName, len(mimes))
	return media.DeviceCapabilities{Queried: true, MIMEs: mimes}
}

// Cast 把本地视频投到指定设备：注册流会话并通过 AVTransport 下发播放。
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

// findDeviceLocked 按 UDN 在最近一次搜索结果中查找设备；调用方须持有 a.mu。
func (a *App) findDeviceLocked(udn string) *dlna.Device {
	for _, d := range a.devices {
		if d.UDN == udn {
			return d
		}
	}
	return nil
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
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	state, err := renderer.TransportState(ctx)
	if err == nil && state == "PLAYING" {
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
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return renderer.GetVolume(ctx)
}

// SetVolume 设置电视端音量（0-100）。
func (a *App) SetVolume(volume int) error {
	renderer, err := a.currentRenderer()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
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

// ---------- 设置：代理与 Cookies ----------

// DiagLogPath 返回诊断日志文件路径（出问题把这个文件发来分析）。
func (a *App) DiagLogPath() string {
	path, err := media.DiagLogPath()
	if err != nil {
		return ""
	}
	return path
}

// GetConfig 返回当前设置。
func (a *App) GetConfig() *media.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	cfg := a.cfg
	return &cfg
}

// SetConfig 保存设置并持久化；保存后立即对后续解析/投屏生效。
func (a *App) SetConfig(cfg media.Config) error {
	cfg = cfg.Normalize()
	if err := media.SaveConfig(cfg); err != nil {
		return err
	}
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()
	return nil
}

// SystemProxy 返回当前检测到的系统代理地址，供设置页展示。
func (a *App) SystemProxy() string { return media.DetectSystemProxy() }

// TestProxy 验证给定设置中代理的连通性，返回可直接展示的结论。
func (a *App) TestProxy(cfg media.Config) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	return media.TestProxy(ctx, cfg.Normalize().ResolveOptions())
}

// BrowserInfo 描述本机可用于登录的浏览器。
type BrowserInfo struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// LoginBrowserInfo 描述登录浏览器的当前状态，供设置页展示。
type LoginBrowserInfo struct {
	// Supported 表示本机是否检测到可用浏览器。
	Supported bool `json:"supported"`
	// Running 表示应用启动的登录浏览器是否正在运行。
	Running bool `json:"running"`
	// Executable 是实际使用的浏览器可执行文件。
	Executable string `json:"executable"`
	// Browsers 是本机检测到的候选浏览器列表。
	Browsers []BrowserInfo `json:"browsers"`
}

// LoginBrowserStatus 返回登录浏览器的可用性与运行状态。
func (a *App) LoginBrowserStatus() *LoginBrowserInfo {
	info := &LoginBrowserInfo{}
	for _, b := range browser.Available() {
		info.Browsers = append(info.Browsers, BrowserInfo{Name: b.Name, Path: b.Path})
	}
	info.Supported = len(info.Browsers) > 0
	if a.browserMgr != nil {
		info.Running = a.browserMgr.Running()
		info.Executable = a.browserMgr.Executable()
	}
	return info
}

// OpenLoginBrowser 启动应用专用的浏览器窗口访问指定站点供用户登录。
// browserName 指定用哪台浏览器（LoginBrowserStatus 返回的名称）；空串或
// 未匹配时用检测到的第一个候选。浏览器使用独立 profile，不读写用户日常
// 浏览器的数据；登录状态会被保留，下次无需重复登录。用户登录完成后需
// 调用 SaveBrowserCookies 取回 Cookies。
//
// 该方法只负责启动，不阻塞等待登录。
func (a *App) OpenLoginBrowser(rawURL, browserName string) error {
	if a.browserMgr == nil {
		return errors.New("登录浏览器未初始化")
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		rawURL = "https://www.youtube.com"
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	exe := ""
	if browserName != "" {
		for _, b := range browser.Available() {
			if b.Name == browserName {
				exe = b.Path
				break
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, err := a.browserMgr.Start(ctx, rawURL, exe)
	return err
}

// CloseLoginBrowser 关闭应用启动的登录浏览器。
func (a *App) CloseLoginBrowser() {
	if a.browserMgr != nil {
		a.browserMgr.Close()
	}
}

// SaveBrowserCookies 从登录浏览器读回全部 Cookie（含 HttpOnly）并保存为
// yt-dlp 可读的文件。应在用户于浏览器中完成登录后调用。
func (a *App) SaveBrowserCookies() (*media.CookiesInfo, error) {
	if a.browserMgr == nil {
		return nil, errors.New("登录浏览器未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cookies, err := a.browserMgr.Cookies(ctx)
	if err != nil {
		return nil, err
	}
	n, err := media.SaveCookies(toMediaCookies(cookies))
	if err != nil {
		return nil, err
	}
	a.logf("已从登录浏览器保存 %d 条 Cookies", n)

	status := media.CookiesStatus()
	return &status, nil
}

// toMediaCookies 把浏览器读出的 Cookie 转换为持久化结构。
func toMediaCookies(in []browser.Cookie) []media.Cookie {
	out := make([]media.Cookie, 0, len(in))
	for _, c := range in {
		out = append(out, media.Cookie{
			Domain:    c.Domain,
			Path:      c.Path,
			Name:      c.Name,
			Value:     c.Value,
			Secure:    c.Secure,
			HttpOnly:  c.HttpOnly,
			ExpiresAt: c.ExpiresAt,
		})
	}
	return out
}

// GetCookieStatus 返回登录浏览器导出 Cookies 的保存状态，供设置页展示。
func (a *App) GetCookieStatus() *media.CookiesInfo {
	info := media.CookiesStatus()
	return &info
}

// ClearCookies 清除登录浏览器导出的 Cookies 文件（用于退出登录态）。
func (a *App) ClearCookies() error { return media.DeleteCookies() }

// ResetLoginBrowser 清除登录浏览器的独立 profile（彻底退出所有站点登录）。
func (a *App) ResetLoginBrowser() error {
	if a.browserMgr == nil {
		return errors.New("登录浏览器未初始化")
	}
	return a.browserMgr.Reset()
}

// hostOf 从 URL 提取主机:端口。
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}
