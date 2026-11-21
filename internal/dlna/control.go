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
	_, err := soapCall(ctx, r.client, svc.ControlURL, svc.Type, action, all)
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

// SeekUnit REL_TIME 与 ABS_TIME 时间寻址单位。
// REL_TIME（相对时间）是绝大多数 DLNA 电视（三星、索尼、LG、小米、海信等）单视频播放的标准寻址单位；
// ABS_TIME（绝对时间）供部分只支持绝对时间的老旧设备备选。
const (
	SeekUnitRelTime = "REL_TIME"
	SeekUnitABSTime = "ABS_TIME"
)

// Seek 跳转到 target 指定的时间点（格式为 HH:MM:SS）。
// 优先使用给定的 unit（通常为 REL_TIME）；若设备报 710（Seek mode not supported）或 402（Invalid Args），
// 自动在 REL_TIME 与 ABS_TIME 之间切换重试一次，彻底消除不同电视固件寻址单位不一致导致的失败。
func (r *Renderer) Seek(ctx context.Context, unit, target string) error {
	if unit == "" {
		unit = SeekUnitRelTime
	}
	err := r.avTransport(ctx, "Seek", [][2]string{
		{"Unit", unit},
		{"Target", target},
	})
	if err == nil {
		return nil
	}
	// 判断是否由于设备不支持当前单位触发的 710 / 402
	errStr := err.Error()
	if strings.Contains(errStr, "710") || strings.Contains(errStr, "402") {
		fallbackUnit := SeekUnitABSTime
		if unit == SeekUnitABSTime {
			fallbackUnit = SeekUnitRelTime
		}
		if ferr := r.avTransport(ctx, "Seek", [][2]string{
			{"Unit", fallbackUnit},
			{"Target", target},
		}); ferr == nil {
			return nil
		}
	}
	return err
}

// TransportState 的标准取值（UPnP AVTransport），供调用方比较用，
// 避免在各处硬编码字符串。未列出的取值（TRANSITIONING、NO_MEDIA_PRESENT
// 等）按原样透传。
const (
	TransportStateStopped = "STOPPED"
	TransportStatePlaying = "PLAYING"
	TransportStatePaused  = "PAUSED_PLAYBACK"
)

// TransportState 返回播放状态（STOPPED / PLAYING / PAUSED_PLAYBACK 等）。
func (r *Renderer) TransportState(ctx context.Context) (string, error) {
	svc := r.dev.Service(AVTransportServiceType)
	if svc == nil {
		return "", fmt.Errorf("设备 %s 不支持 AVTransport", r.dev.FriendlyName)
	}
	out, err := soapCall(ctx, r.client, svc.ControlURL, svc.Type, "GetTransportInfo", [][2]string{{"InstanceID", "0"}})
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
	out, err := soapCall(ctx, r.client, svc.ControlURL, svc.Type, "GetPositionInfo", [][2]string{{"InstanceID", "0"}})
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
	out, err := soapCall(ctx, r.client, svc.ControlURL, svc.Type, "GetVolume", [][2]string{
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
	_, err := soapCall(ctx, r.client, svc.ControlURL, svc.Type, "SetVolume", [][2]string{
		{"InstanceID", "0"},
		{"Channel", "Master"},
		{"DesiredVolume", strconv.Itoa(volume)},
	})
	return err
}

// GetMute 查询渲染设备是否处于静音状态。
func (r *Renderer) GetMute(ctx context.Context) (bool, error) {
	svc := r.dev.Service(RenderingControlServiceType)
	if svc == nil {
		return false, fmt.Errorf("设备 %s 不支持 RenderingControl", r.dev.FriendlyName)
	}
	out, err := soapCall(ctx, r.client, svc.ControlURL, svc.Type, "GetMute", [][2]string{
		{"InstanceID", "0"},
		{"Channel", "Master"},
	})
	if err != nil {
		return false, err
	}
	val := strings.TrimSpace(out["CurrentMute"])
	return val == "1" || strings.EqualFold(val, "true"), nil
}

// SetMute 设置渲染设备静音状态。
func (r *Renderer) SetMute(ctx context.Context, mute bool) error {
	svc := r.dev.Service(RenderingControlServiceType)
	if svc == nil {
		return fmt.Errorf("设备 %s 不支持 RenderingControl", r.dev.FriendlyName)
	}
	desired := "0"
	if mute {
		desired = "1"
	}
	_, err := soapCall(ctx, r.client, svc.ControlURL, svc.Type, "SetMute", [][2]string{
		{"InstanceID", "0"},
		{"Channel", "Master"},
		{"DesiredMute", desired},
	})
	return err
}

// FormatClock 把秒数格式化为 UPnP 标准的 HH:MM:SS 时间文本。
// 使用两位小时（%02d）确保严格遵循 UPnP AVTransport / DLNA 规范，
// 同时兼容要求两位小时的设备（如部分索尼/松下设备）。
func FormatClock(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	total := int(seconds + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d", total/3600, (total%3600)/60, total%60)
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

// DIDLOptions 包含构造 DIDL-Lite 描述所需的元数据字段。
type DIDLOptions struct {
	Title        string
	URI          string
	MIME         string
	DurationSec  float64
	Direct       bool
	SubtitleURL  string
	SubtitleType string // 如 "srt"
}

// BuildDIDL 构造 SetAVTransportURI 所需的完整 DIDL-Lite 元数据。
// 针对三星、LG、索尼等设备注入标准 protocolInfo 特性参数、总时长与字幕扩展。
func BuildDIDL(opts DIDLOptions) string {
	title := escapeXML(opts.Title)
	uri := escapeXML(opts.URI)
	mime := opts.MIME
	if mime == "" {
		mime = "video/mp4"
	}

	protoInfo := "http-get:*:" + mime + ":*"
	if opts.Direct {
		protoInfo = "http-get:*:" + mime + ":DLNA.ORG_OP=01;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000"
	}

	var durAttr string
	if opts.DurationSec > 0 {
		durAttr = ` duration="` + FormatClock(opts.DurationSec) + `"`
	}

	var sb strings.Builder
	sb.WriteString(`<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/"`)
	sb.WriteString(` xmlns:dc="http://purl.org/dc/elements/1.1/"`)
	sb.WriteString(` xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/"`)
	if opts.SubtitleURL != "" {
		// 三星与 LG 智能电视字幕扩展命名空间
		sb.WriteString(` xmlns:sec="http://www.sec.co.kr/"`)
	}
	sb.WriteString(`><item id="0" restricted="1">`)
	sb.WriteString(`<dc:title>` + title + `</dc:title>`)
	sb.WriteString(`<upnp:class>object.item.videoItem</upnp:class>`)
	sb.WriteString(`<res protocolInfo="` + protoInfo + `"` + durAttr + `>` + uri + `</res>`)

	if opts.SubtitleURL != "" {
		subURI := escapeXML(opts.SubtitleURL)
		subType := opts.SubtitleType
		if subType == "" {
			subType = "srt"
		}
		// 1. 标准 DLNA 字幕资源（text/srt 与通用 application/x-subrip 兼容声明）
		sb.WriteString(`<res protocolInfo="http-get:*:text/` + subType + `:*">` + subURI + `</res>`)
		if subType == "srt" {
			sb.WriteString(`<res protocolInfo="http-get:*:application/x-subrip:*">` + subURI + `</res>`)
		}
		// 2. 三星与 LG 私有扩展标签
		sb.WriteString(`<sec:CaptionInfo sec:type="` + subType + `">` + subURI + `</sec:CaptionInfo>`)
		sb.WriteString(`<sec:CaptionInfoEx sec:type="` + subType + `">` + subURI + `</sec:CaptionInfoEx>`)
	}

	sb.WriteString(`</item></DIDL-Lite>`)
	return sb.String()
}

// BuildDIDLMetadata 构造 SetAVTransportURI 所需的 DIDL-Lite 元数据（兼容基础调用）。
func BuildDIDLMetadata(title, uri, mime string) string {
	return BuildDIDL(DIDLOptions{Title: title, URI: uri, MIME: mime})
}

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}
