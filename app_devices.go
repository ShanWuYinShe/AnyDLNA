package main

import (
	"AnyDLNA/internal/dlna"
	"AnyDLNA/internal/media"
	"context"
	"fmt"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"net/url"
	"strings"
	"time"
)

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

// findDeviceLocked 按 UDN 在最近一次搜索结果中查找设备；调用方须持有 a.mu。
func (a *App) findDeviceLocked(udn string) *dlna.Device {
	for _, d := range a.devices {
		if d.UDN == udn {
			return d
		}
	}
	return nil
}

// hostOf 从 URL 提取主机:端口。
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}
