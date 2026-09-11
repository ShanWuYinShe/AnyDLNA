package dlna

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Renderer 封装对单台渲染设备的 AVTransport / RenderingControl 操作。
type Renderer struct {
	dev    *Device
	client *http.Client
}

// NewRenderer 基于已描述的设备构造渲染器。
func NewRenderer(dev *Device) *Renderer {
	return &Renderer{dev: dev, client: defaultHTTPClient()}
}

// Device 返回底层设备信息。
func (r *Renderer) Device() *Device { return r.dev }

// avTransport 调用 AVTransport 服务动作。
func (r *Renderer) avTransport(ctx context.Context, action string, args [][2]string) error {
	svc := r.dev.Service(AVTransportServiceType)
	if svc == nil {
		return fmt.Errorf("设备 %s 不支持 AVTransport", r.dev.FriendlyName)
	}
	all := append([][2]string{{"InstanceID", "0"}}, args...)
	_, err := soapCall(ctx, r.client, svc.ControlURL, AVTransportServiceType, action, all)
	return err
}

// SetAVTransportURI 设置待播放的媒体 URI 及其元数据。
func (r *Renderer) SetAVTransportURI(ctx context.Context, uri, metadata string) error {
	return r.avTransport(ctx, "SetAVTransportURI", [][2]string{
		{"CurrentURI", uri},
		{"CurrentURIMetaData", metadata},
	})
}

// Play 开始播放。
func (r *Renderer) Play(ctx context.Context) error {
	return r.avTransport(ctx, "Play", [][2]string{{"Speed", "1"}})
}

// Pause 暂停播放。
func (r *Renderer) Pause(ctx context.Context) error {
	return r.avTransport(ctx, "Pause", nil)
}

// Stop 停止播放。
func (r *Renderer) Stop(ctx context.Context) error {
	return r.avTransport(ctx, "Stop", nil)
}

// SeekUnit ABS_TIME 表示跳转到媒体内的绝对时间点。
const SeekUnitABSTime = "ABS_TIME"

// Seek 跳转到 target 指定的时间点，格式为 H:MM:SS。
func (r *Renderer) Seek(ctx context.Context, unit, target string) error {
	return r.avTransport(ctx, "Seek", [][2]string{
		{"Unit", unit},
		{"Target", target},
	})
}

// TransportState 返回播放状态（STOPPED / PLAYING / PAUSED_PLAYBACK 等）。
func (r *Renderer) TransportState(ctx context.Context) (string, error) {
	svc := r.dev.Service(AVTransportServiceType)
	if svc == nil {
		return "", fmt.Errorf("设备 %s 不支持 AVTransport", r.dev.FriendlyName)
	}
	out, err := soapCall(ctx, r.client, svc.ControlURL, AVTransportServiceType, "GetTransportInfo", [][2]string{{"InstanceID", "0"}})
	if err != nil {
		return "", err
	}
	return out["CurrentTransportState"], nil
}

// PositionInfo 返回当前播放位置与媒体总时长（均为 H:MM:SS 文本）。
func (r *Renderer) PositionInfo(ctx context.Context) (relTime, duration string, err error) {
	svc := r.dev.Service(AVTransportServiceType)
	if svc == nil {
		return "", "", fmt.Errorf("设备 %s 不支持 AVTransport", r.dev.FriendlyName)
	}
	out, err := soapCall(ctx, r.client, svc.ControlURL, AVTransportServiceType, "GetPositionInfo", [][2]string{{"InstanceID", "0"}})
	if err != nil {
		return "", "", err
	}
	return out["RelTime"], out["TrackDuration"], nil
}

// GetVolume 返回渲染设备主声道音量（0-100）。
func (r *Renderer) GetVolume(ctx context.Context) (int, error) {
	svc := r.dev.Service(RenderingControlServiceType)
	if svc == nil {
		return 0, fmt.Errorf("设备 %s 不支持 RenderingControl", r.dev.FriendlyName)
	}
	out, err := soapCall(ctx, r.client, svc.ControlURL, RenderingControlServiceType, "GetVolume", [][2]string{
		{"InstanceID", "0"},
		{"Channel", "Master"},
	})
	if err != nil {
		return 0, err
	}
	vol, err := strconv.Atoi(strings.TrimSpace(out["CurrentVolume"]))
	if err != nil {
		return 0, fmt.Errorf("解析音量失败: %w", err)
	}
	return vol, nil
}

// SetVolume 设置渲染设备主声道音量（0-100）。
func (r *Renderer) SetVolume(ctx context.Context, volume int) error {
	svc := r.dev.Service(RenderingControlServiceType)
	if svc == nil {
		return fmt.Errorf("设备 %s 不支持 RenderingControl", r.dev.FriendlyName)
	}
	if volume < 0 {
		volume = 0
	}
	if volume > 100 {
		volume = 100
	}
	_, err := soapCall(ctx, r.client, svc.ControlURL, RenderingControlServiceType, "SetVolume", [][2]string{
		{"InstanceID", "0"},
		{"Channel", "Master"},
		{"DesiredVolume", strconv.Itoa(volume)},
	})
	return err
}

// FormatClock 把秒数格式化为 UPnP 的 H:MM:SS 时间文本。
func FormatClock(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	total := int(seconds + 0.5)
	return fmt.Sprintf("%d:%02d:%02d", total/3600, (total%3600)/60, total%60)
}

// ParseClock 解析 UPnP 的 H:MM:SS（或 H:MM:SS.fraction）时间文本为秒数。
// 无法解析时返回 err 非 nil。
func ParseClock(text string) (float64, error) {
	text = strings.TrimSpace(text)
	if text == "" || text == "NOT_IMPLEMENTED" {
		return 0, fmt.Errorf("设备未提供时间信息")
	}
	parts := strings.Split(text, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("无法解析时间 %q", text)
	}
	h, err1 := strconv.ParseFloat(parts[0], 64)
	m, err2 := strconv.ParseFloat(parts[1], 64)
	s, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, fmt.Errorf("无法解析时间 %q", text)
	}
	return h*3600 + m*60 + s, nil
}

// BuildDIDLMetadata 构造 SetAVTransportURI 所需的 DIDL-Lite 元数据。
// 某些电视不校验元数据，但携带标题与 upnp:class 可以让电视端显示片名并正确归类。
func BuildDIDLMetadata(title, uri, mime string) string {
	title = strings.ReplaceAll(title, "&", "&amp;")
	title = strings.ReplaceAll(title, "<", "&lt;")
	title = strings.ReplaceAll(title, ">", "&gt;")
	uri = strings.ReplaceAll(uri, "&", "&amp;")
	return `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/"` +
		` xmlns:dc="http://purl.org/dc/elements/1.1/"` +
		` xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">` +
		`<item id="0" restricted="1">` +
		`<dc:title>` + title + `</dc:title>` +
		`<upnp:class>object.item.videoItem</upnp:class>` +
		`<res protocolInfo="http-get:*:` + mime + `:*">` + uri + `</res>` +
		`</item></DIDL-Lite>`
}
