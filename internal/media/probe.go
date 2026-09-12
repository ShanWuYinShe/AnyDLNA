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
		Container: raw.Format.FormatName,
		Title:     strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
	}
	fmt.Sscanf(raw.Format.Duration, "%f", &info.DurationSec)
	fmt.Sscanf(raw.Format.Size, "%d", &info.SizeBytes)
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
	// OutputDirect 直接投递原文件字节：电视可原生解码，且支持 HTTP Range 拖动。
	OutputDirect OutputMode = "direct"
	// OutputRemux 仅把已有轨道重新封装进 MPEG-TS，不重编码视频。
	// 无损且几乎不占 CPU（实测 18–29 倍实时），是有 H.264 源时的首选。
	OutputRemux OutputMode = "remux"
	// OutputTranscode 完整转码为 H.264/AAC：兼容性最好但开销最高
	// （实测 1080p 约 2 倍、4K 约 1.4 倍实时），仅在前两者不可行时使用。
	OutputTranscode OutputMode = "transcode"
)

// Plan 描述一路投屏应如何输出。
type Plan struct {
	Mode      OutputMode
	CopyVideo bool // 视频轨道直接复制（不重编码）
	CopyAudio bool // 音频轨道直接复制（不重编码）
}

// IsDirect 报告是否以原文件直出（支持 Range 拖动）。
func (p Plan) IsDirect() bool { return p.Mode == OutputDirect }

// NeedsVideoEncode 报告是否需要重编码视频，即真正的性能瓶颈所在。
func (p Plan) NeedsVideoEncode() bool { return !p.CopyVideo }

// PlanForLocal 依据探测结果决定本地文件的输出方式。
func PlanForLocal(info *Info) Plan {
	if info == nil {
		return Plan{Mode: OutputTranscode}
	}
	return planFor(info.Container, info.VideoCodec, info.AudioCodec, info.PixFmt, true)
}

// PlanForOnline 依据 yt-dlp 选中的编码决定在线视频的输出方式。
// 在线源必须经管道送入 ffmpeg，无法原文件直出，因此只在换封装与转码之间选择。
func PlanForOnline(videoCodec, audioCodec string) Plan {
	return planFor("", videoCodec, audioCodec, "", false)
}

// planFor 是两种来源共用的决策逻辑。
// allowDirect 仅在本地文件场景为 true：只有它能直接投递原文件字节。
//
// 决策原则是「能不解码就不解码」：视频编码是唯一的高开销环节，
// 只要源视频是电视可解码的 H.264，就一律直通（换封装），
// 音频不兼容时只重编码音频——其开销相对视频可忽略。
func planFor(container, videoCodec, audioCodec, pixFmt string, allowDirect bool) Plan {
	// 视频不是 H.264（HEVC/AV1/VP9/MPEG-4 等）：电视普遍无法解码，只能完整转码。
	if !isH264(normalizeCodec(videoCodec)) {
		return Plan{Mode: OutputTranscode}
	}

	// 10-bit H.264（Hi10P 等，动漫常见）电视普遍无法解码，
	// 而重编码会顺带转为 8-bit，因此这类内容必须排除在换封装之外。
	if isHighBitDepth(pixFmt) {
		return Plan{Mode: OutputTranscode}
	}

	// 能直接放进 MPEG-TS 的音频只有 AAC，其余（Opus/MP3/AC3 等）需转码。
	copyAudio := isAAC(normalizeCodec(audioCodec))

	// 本地文件且容器与音频都合适：投递原文件，电视端还能拖动进度条。
	if allowDirect && copyAudio && isMP4Container(container) {
		return Plan{Mode: OutputDirect, CopyVideo: true, CopyAudio: true}
	}

	// 其余情况换封装：视频无损直通，音频按需转码。
	return Plan{Mode: OutputRemux, CopyVideo: true, CopyAudio: copyAudio}
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
