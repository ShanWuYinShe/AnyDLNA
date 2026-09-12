// Package media 负责把任意本地视频变成 DLNA 设备可拉取的 HTTP 流：
// 兼容的文件直接供原始字节（支持 Range 拖动），不兼容的用 ffmpeg 实时转码为 MPEG-TS。
package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Info 是 ffprobe 提取的媒体关键信息。
type Info struct {
	// Path 是媒体文件路径（仅本地文件有值）。
	Path        string  `json:"path"`
	Container   string  `json:"container"`
	VideoCodec  string  `json:"video_codec"`
	AudioCodec  string  `json:"audio_codec"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	DurationSec float64 `json:"duration_sec"`
	SizeBytes   int64   `json:"size_bytes"`
	Title       string  `json:"title"` // 不含扩展名的文件名
	// PixFmt 是视频像素格式（如 yuv420p、yuv420p10le）。
	// 10-bit 内容（Hi10P 等）电视普遍无法解码，必须排除在换封装之外。
	PixFmt string `json:"pix_fmt"`
	// FastStart 表示 MP4/MOV 的索引（moov）位于媒体数据（mdat）之前。
	// 只有这样才能边下边播；索引在末尾时播放器需下载完整个文件才能起播，
	// 表现为电视长时间黑屏不播（实测 90 秒内进度始终为 0）。
	// 非 MP4 容器恒为 true（不适用该限制）。
	FastStart bool `json:"fast_start"`
}

// HasFFmpeg 报告 ffmpeg/ffprobe 是否可用。
func HasFFmpeg() bool {
	_, err := exec.LookPath("ffprobe")
	return err == nil
}

// Probe 用 ffprobe 探测媒体文件。
func Probe(ctx context.Context, path string) (*Info, error) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return nil, fmt.Errorf("未找到 ffprobe，请先安装 ffmpeg：brew install ffmpeg")
	}
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_format", "-show_streams",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("探测媒体失败（文件损坏或格式不支持）: %w", err)
	}

	var raw struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			PixFmt    string `json:"pix_fmt"`
		} `json:"streams"`
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
			Size       string `json:"size"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("解析 ffprobe 输出失败: %w", err)
	}

	info := &Info{
		Path:      path,
		Container: raw.Format.FormatName,
		Title:     strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		// 默认视为可流式；仅 MP4/MOV 需要实际检查索引位置。
		FastStart: true,
	}
	fmt.Sscanf(raw.Format.Duration, "%f", &info.DurationSec)
	fmt.Sscanf(raw.Format.Size, "%d", &info.SizeBytes)
	if isMP4Container(info.Container) {
		if fast, err := IsFastStart(path); err == nil {
			info.FastStart = fast
		} else {
			// 无法判断时按不可流式处理，避免投出去后电视长时间黑屏。
			info.FastStart = false
		}
	}
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			if info.VideoCodec == "" {
				info.VideoCodec, info.Width, info.Height = s.CodecName, s.Width, s.Height
				info.PixFmt = s.PixFmt
			}
		case "audio":
			if info.AudioCodec == "" {
				info.AudioCodec = s.CodecName
			}
		}
	}
	return info, nil
}

// OutputMode 描述一路投屏流的输出方式。
type OutputMode string

const (
	// OutputDirect 直接投递原文件字节：设备可原生解码该容器，且支持 HTTP Range 拖动。
	OutputDirect OutputMode = "direct"
	// OutputRemux 仅把已有轨道重新封装进目标容器，不重编码视频。
	// 无损且几乎不占 CPU（实测 18–29 倍实时），是有 H.264 源时的首选。
	OutputRemux OutputMode = "remux"
	// OutputTranscode 完整转码为 H.264/AAC：兼容性最好但开销最高
	// （实测 1080p 约 2 倍、4K 约 1.4 倍实时），仅在前两者不可行时使用。
	OutputTranscode OutputMode = "transcode"
)

// OutputContainer 是封装输出的目标容器。
type OutputContainer string

const (
	// ContainerMPEGTS 是 DLNA 渲染设备的通用基线容器，几乎所有设备都支持。
	ContainerMPEGTS OutputContainer = "mpegts"
	// ContainerFMP4 是碎片化 MP4：可流式写出，且部分设备只声明支持 MP4 而不支持 TS。
	ContainerFMP4 OutputContainer = "mp4"
)

// MIME 返回该容器对应的媒体类型。
func (c OutputContainer) MIME() string {
	if c == ContainerFMP4 {
		return "video/mp4"
	}
	return "video/mp2t"
}

// DeviceCapabilities 是投屏目标通过 ConnectionManager 声明的接收能力。
// 由调用方从 dlna 层转换而来，media 层不直接依赖 dlna 包。
type DeviceCapabilities struct {
	// Queried 表示是否成功查询到设备能力。
	// 设备未提供 ConnectionManager 服务或查询失败时为 false，
	// 此时一律回退到保守策略（MPEG-TS）。
	Queried bool
	// MIMEs 是设备声明支持的媒体类型（已归一化为小写）。
	MIMEs []string
}

// Supports 报告设备是否声明支持给定 MIME 类型。
// 未查询到能力时返回 false，调用方据此回退到保守策略。
//
// c.MIMEs 由调用方（app 层）从 dlna 包的解析结果转换而来，已是归一化写法；
// 这里只做大小写无关的精确比较，别名归一化由 dlna 包统一负责，避免两处规则漂移。
func (c DeviceCapabilities) Supports(mime string) bool {
	if !c.Queried {
		return false
	}
	want := canonicalMIME(mime)
	for _, m := range c.MIMEs {
		if canonicalMIME(m) == want {
			return true
		}
	}
	return false
}

// canonicalMIME 把 MIME 归一化为可比较形式（小写、去参数与空白）。
func canonicalMIME(mime string) string {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	return strings.TrimSpace(mime)
}

// outputContainerFor 选择设备支持的输出容器：优先 MPEG-TS（通用基线），
// 设备明确不支持 TS 但支持 MP4 时改用碎片化 MP4。
func outputContainerFor(caps DeviceCapabilities) OutputContainer {
	if caps.Queried && !caps.Supports(ContainerMPEGTS.MIME()) && caps.Supports(ContainerFMP4.MIME()) {
		return ContainerFMP4
	}
	return ContainerMPEGTS
}

// Plan 描述一路投屏应如何输出。
type Plan struct {
	Mode      OutputMode
	Container OutputContainer // 封装输出的目标容器（直出时无意义）
	CopyVideo bool            // 视频轨道直接复制（不重编码）
	CopyAudio bool            // 音频轨道直接复制（不重编码）
	// DirectMIME 是直出时使用的媒体类型。
	DirectMIME string
}

// IsDirect 报告是否以原文件直出（支持 Range 拖动）。
func (p Plan) IsDirect() bool { return p.Mode == OutputDirect }

// NeedsVideoEncode 报告是否需要重编码视频，即真正的性能瓶颈所在。
func (p Plan) NeedsVideoEncode() bool { return !p.CopyVideo }

// OutputMIME 返回该输出方案下电视端应看到的媒体类型。
func (p Plan) OutputMIME() string {
	if p.Mode == OutputDirect {
		if p.DirectMIME != "" {
			return p.DirectMIME
		}
		return "application/octet-stream"
	}
	return p.Container.MIME()
}

// PlanForLocal 依据探测结果与设备能力决定本地文件的输出方式。
// caps 为零值（未查询到能力）时回退到保守策略。
func PlanForLocal(info *Info, caps DeviceCapabilities) Plan {
	if info == nil {
		return Plan{Mode: OutputTranscode, Container: ContainerMPEGTS}
	}
	return planFor(planInput{
		container:   info.Container,
		videoCodec:  info.VideoCodec,
		audioCodec:  info.AudioCodec,
		pixFmt:      info.PixFmt,
		fastStart:   info.FastStart,
		sourceMIME:  MimeTypeFor(info.Path),
		allowDirect: true,
	}, caps)
}

// PlanForOnline 依据 yt-dlp 选中的编码与设备能力决定在线视频的输出方式。
// 在线源需经管道送入 ffmpeg，无法原文件直出，因此只在换封装与转码之间选择。
func PlanForOnline(videoCodec, audioCodec string, caps DeviceCapabilities) Plan {
	return planFor(planInput{
		videoCodec: videoCodec,
		audioCodec: audioCodec,
		// 在线源的管道输出无法做 faststart 检查，走换封装或转码即可。
		fastStart: true,
	}, caps)
}

// planInput 汇总决策所需的全部输入。
type planInput struct {
	container   string // 源容器（本地文件）
	videoCodec  string
	audioCodec  string
	pixFmt      string
	fastStart   bool
	sourceMIME  string // 源文件的媒体类型（用于向设备协商直出）
	allowDirect bool   // 是否允许原文件直出（仅本地文件）
}

// planFor 是两种来源共用的决策逻辑。
//
// 决策顺序（能不解码就不解码）：
//  1. 源视频不是 H.264 或为高比特深度：必须完整转码。
//  2. 设备明确声明支持源容器，且音频可直通：投原文件（零开销，还能拖进度）。
//     MP4 额外要求 faststart，否则设备要下载完整个文件才能起播。
//  3. 否则换封装：视频无损直通，音频按需转码。
func planFor(in planInput, caps DeviceCapabilities) Plan {
	out := outputContainerFor(caps)

	// 视频不是 H.264（HEVC/AV1/VP9/MPEG-4 等）：设备普遍无法解码，只能完整转码。
	if !isH264(normalizeCodec(in.videoCodec)) {
		return Plan{Mode: OutputTranscode, Container: out}
	}

	// 10-bit H.264（Hi10P 等，动漫常见）设备普遍无法解码，
	// 而重编码会顺带转为 8-bit，因此这类内容必须排除在换封装之外。
	if isHighBitDepth(in.pixFmt) {
		return Plan{Mode: OutputTranscode, Container: out}
	}

	// 能直接放进目标容器的音频只有 AAC，其余（Opus/MP3/AC3 等）需转码。
	copyAudio := isAAC(normalizeCodec(in.audioCodec))

	// 设备声明支持源容器，且音频能直通：直接投原文件，无需任何处理。
	if in.allowDirect && copyAudio && in.sourceMIME != "" && caps.Supports(in.sourceMIME) {
		// MP4/MOV 只有在 faststart 时才能边下边播。
		// 非 faststart 时退回换封装——顺带把索引前置，解决起播等待。
		if !isMP4Container(in.container) || in.fastStart {
			return Plan{
				Mode:       OutputDirect,
				CopyVideo:  true,
				CopyAudio:  true,
				DirectMIME: in.sourceMIME,
			}
		}
	}

	// 换封装：视频无损直通，音频按需转码。
	return Plan{Mode: OutputRemux, Container: out, CopyVideo: true, CopyAudio: copyAudio}
}

// normalizeCodec 归一化 ffprobe / yt-dlp 给出的编码名。
func normalizeCodec(codec string) string {
	return strings.ToLower(strings.TrimSpace(codec))
}

// isH264 报告编码是否为 H.264；ffprobe 用 "h264"，yt-dlp 用 "avc1.640028" 这类写法。
func isH264(codec string) bool {
	return codec == "h264" || strings.HasPrefix(codec, "avc1")
}

// isAAC 报告编码是否为 AAC。
// mp4a.40.* 表示 MPEG-4 音频；其中 mp4a.40.34（以及 mp4a.69/6b）实为 MP3，
// 不能当作 AAC 直接复制进 MPEG-TS。
func isAAC(codec string) bool {
	if codec == "aac" {
		return true
	}
	if !strings.HasPrefix(codec, "mp4a.40.") {
		return false
	}
	objectType := strings.TrimPrefix(codec, "mp4a.40.")
	// 只取对象类型编号，忽略可能存在的后续字段。
	if i := strings.IndexByte(objectType, '.'); i >= 0 {
		objectType = objectType[:i]
	}
	return objectType != "34" // 34 = MP3
}

// isMP4Container 报告容器是否可由电视直接解码原文件。
func isMP4Container(container string) bool {
	container = strings.ToLower(container)
	return strings.Contains(container, "mp4") || strings.Contains(container, "mov")
}

// isHighBitDepth 报告像素格式是否为高比特深度（9/10/12/16-bit）。
// 这类内容（典型为动漫的 Hi10P）绝大多数电视无法解码，必须重编码。
// pixFmt 为空（探测未提供，或在线源无此信息）时按 8-bit 处理，
// 避免把信息缺失误判成不支持。
//
// 注意不能简单匹配 "10"/"12" 等子串：nv12 是 8-bit 格式却含 "12"，
// 因此这里只匹配 ffmpeg 像素格式命名中的规范后缀与已知的 10-bit 格式。
func isHighBitDepth(pixFmt string) bool {
	pixFmt = normalizeCodec(pixFmt)
	if pixFmt == "" {
		return false
	}
	// 高比特深度在 ffmpeg 命名中体现为 le/be 后缀（如 yuv420p10le）。
	if strings.HasSuffix(pixFmt, "le") || strings.HasSuffix(pixFmt, "be") {
		return true
	}
	// 已知的显式高比特深度格式。
	switch pixFmt {
	case "p010", "p012", "p016", "xyz12le", "xyz12be":
		return true
	}
	return false
}
