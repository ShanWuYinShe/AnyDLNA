package dlna

import (
	"context"
	"sort"
	"strings"
)

// ProtocolInfo 描述设备声明支持的一种传输协议与媒体格式组合，
// 对应 ConnectionManager 的 GetProtocolInfo 返回的 Sink 列表中的一条。
type ProtocolInfo struct {
	// Protocol 传输协议，如 http-get。
	Protocol string
	// Network 网络类型，通常为 *。
	Network string
	// MIME 媒体类型，如 video/mp4。
	MIME string
	// Params 是第四段附加参数原文（如 DLNA.ORG_PN=...;DLNA.ORG_OP=01;DLNA.ORG_CI=0）。
	// 保留原文而不解析全部字段：DLNA.ORG_OP 等参数的位含义未在
	// 已核实的规范文档中给出，宁可不解释也不猜测。
	Params string
	// Profile 是从 Params 中提取的 DLNA.ORG_PN 值（如 AVC_MP4_MP_HD_1080i_AAC）。
	// 该字段的格式见 [MS-DSPA] 附录 B；设备不提供时为空。
	Profile string
}

// MatchesMIME 报告该条目是否精确匹配给定 MIME 类型。
// 部分设备会把类型写成 video/x-mp4 等变体，这里统一归一化后比较。
func (p ProtocolInfo) MatchesMIME(mime string) bool {
	return NormalizeMIME(p.MIME) == NormalizeMIME(mime)
}

// mimeAliases 把同一格式的常见写法归一化，便于跨厂商比较。
var mimeAliases = map[string]string{
	"video/mp2t":              "video/mp2t",
	"video/mpeg-ts":           "video/mp2t",
	"video/vnd.dlna.mpeg-tts": "video/mp2t",
	"video/mp4":               "video/mp4",
	"video/x-mp4":             "video/mp4",
	"video/m4v":               "video/mp4",
	"video/x-m4v":             "video/mp4",
	"video/quicktime":         "video/quicktime",
	"video/x-matroska":        "video/x-matroska",
	"video/mkv":               "video/x-matroska",
	"video/x-mkv":             "video/x-matroska",
	"video/webm":              "video/webm",
	"video/mpeg":              "video/mpeg",
	"video/mpg":               "video/mpeg",
	"video/x-msvideo":         "video/x-msvideo",
	"video/avi":               "video/x-msvideo",
	"video/divx":              "video/x-msvideo",
	"video/x-divx":            "video/x-msvideo",
	"video/x-ms-wmv":          "video/x-ms-wmv",
	"video/wmv":               "video/x-ms-wmv",
	"video/x-ms-asf":          "video/x-ms-wmv",
	"video/flv":               "video/x-flv",
	"video/x-flv":             "video/x-flv",
}

// NormalizeMIME 把 MIME 归一化为标准写法（小写、去参数、统一别名）。
// 本包是 MIME 归一化的唯一来源：设备的书写变体（video/x-mp4、video/mkv 等）
// 都在这里统一，其他包只需按归一化结果比较。
func NormalizeMIME(mime string) string {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	mime = strings.TrimSpace(mime)
	if canonical, ok := mimeAliases[mime]; ok {
		return canonical
	}
	return mime
}

// ParseProtocolInfo 解析 GetProtocolInfo 返回的一段协议信息列表。
// 每条格式为 protocol:network:mime:params，条目之间以逗号分隔。
// 设备实现常有缺项或多余空白，这里尽量宽容解析，跳过无法识别的条目。
func ParseProtocolInfo(raw string) []ProtocolInfo {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []ProtocolInfo
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// params 自身不含冒号，因此最多切出 4 段。
		parts := strings.SplitN(entry, ":", 4)
		if len(parts) < 3 {
			continue // 至少要有 protocol:network:mime。
		}
		info := ProtocolInfo{
			Protocol: strings.TrimSpace(parts[0]),
			Network:  strings.TrimSpace(parts[1]),
			MIME:     strings.TrimSpace(parts[2]),
		}
		if len(parts) == 4 {
			info.Params = strings.TrimSpace(parts[3])
			info.Profile = parseProfile(info.Params)
		}
		if info.MIME == "" {
			continue
		}
		out = append(out, info)
	}
	return out
}

// parseProfile 从第四段参数中提取 DLNA.ORG_PN。
// 参数以分号分隔，形如 "DLNA.ORG_PN=AVC_MP4_MP_HD_1080i_AAC;DLNA.ORG_OP=01"。
func parseProfile(params string) string {
	for _, kv := range strings.Split(params, ";") {
		key, value, found := strings.Cut(strings.TrimSpace(kv), "=")
		if !found {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "DLNA.ORG_PN") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// ProtocolCapabilities 是一台设备声明的接收能力。
type ProtocolCapabilities struct {
	// Sink 是设备可接收（即能播放）的格式列表。
	Sink []ProtocolInfo
	// Queried 表示是否成功向设备查询到能力（设备可能不支持该服务或查询失败）。
	Queried bool
}

// SupportsMIME 报告设备是否声明支持给定 MIME 类型。
func (c ProtocolCapabilities) SupportsMIME(mime string) bool {
	if !c.Queried {
		return false
	}
	for _, info := range c.Sink {
		if info.MatchesMIME(mime) {
			return true
		}
	}
	return false
}

// VideoMIMEs 返回设备声明的全部视频类 MIME（去重、排序），用于界面展示。
func (c ProtocolCapabilities) VideoMIMEs() []string {
	seen := map[string]bool{}
	var out []string
	for _, info := range c.Sink {
		mime := NormalizeMIME(info.MIME)
		if !strings.HasPrefix(mime, "video/") || seen[mime] {
			continue
		}
		seen[mime] = true
		out = append(out, mime)
	}
	sort.Strings(out)
	return out
}

// QueryProtocolInfo 向设备的 ConnectionManager 查询其接收能力。
// 设备未提供该服务时返回 Queried=false 的能力描述，调用方应回退到保守策略。
func (r *Renderer) QueryProtocolInfo(ctx context.Context) (ProtocolCapabilities, error) {
	svc := r.dev.Service(ConnectionManagerServiceType)
	if svc == nil {
		return ProtocolCapabilities{}, nil
	}
	out, err := soapCall(ctx, r.client, svc.ControlURL, ConnectionManagerServiceType, "GetProtocolInfo", nil)
	if err != nil {
		return ProtocolCapabilities{}, err
	}
	return ProtocolCapabilities{
		Sink:    ParseProtocolInfo(out["Sink"]),
		Queried: true,
	}, nil
}
