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
			}
		case "audio":
			if info.AudioCodec == "" {
				info.AudioCodec = s.CodecName
			}
		}
	}
	return info, nil
}

// NeedsTranscode 判定电视端大概率无法直接解码、需要转码的情况。
// 采用保守策略：仅「MP4/MOV 容器 + H.264 视频 + AAC 音频」直接投递原文件，
// 其余（MKV、HEVC、AVI、FLV、多音轨等）一律实时转码，以最大概率保证可播。
func (i *Info) NeedsTranscode() bool {
	container := strings.ToLower(i.Container)
	directContainer := strings.Contains(container, "mp4") || strings.Contains(container, "mov")
	return !(directContainer &&
		strings.EqualFold(i.VideoCodec, "h264") &&
		strings.EqualFold(i.AudioCodec, "aac"))
}
