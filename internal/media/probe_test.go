package media

import (
	"strings"
	"testing"
)

// TestPlanForLocal 覆盖本地文件的输出方式决策。
// 核心期望：只要视频是 H.264 就不要重编码视频——这是性能关键。
func TestPlanForLocal(t *testing.T) {
	cases := []struct {
		name string
		info Info
		want Plan
	}{
		{
			"MP4 H264 AAC：原文件直出（可拖动进度）",
			Info{Container: "mov,mp4,m4a,3gp,3g2,mj2", VideoCodec: "h264", AudioCodec: "aac"},
			Plan{Mode: OutputDirect, CopyVideo: true, CopyAudio: true},
		},
		{
			"MOV H264 AAC：原文件直出",
			Info{Container: "mov", VideoCodec: "h264", AudioCodec: "aac"},
			Plan{Mode: OutputDirect, CopyVideo: true, CopyAudio: true},
		},
		{
			"MKV H264 AAC：换封装（视频免转码，且不再强制转码）",
			Info{Container: "matroska,webm", VideoCodec: "h264", AudioCodec: "aac"},
			Plan{Mode: OutputRemux, CopyVideo: true, CopyAudio: true},
		},
		{
			"MKV H264 AC3：换封装，视频直通、音频转 AAC",
			Info{Container: "matroska,webm", VideoCodec: "h264", AudioCodec: "ac3"},
			Plan{Mode: OutputRemux, CopyVideo: true, CopyAudio: false},
		},
		{
			"HEVC：视频必须转码",
			Info{Container: "mov,mp4,m4a,3gp,3g2,mj2", VideoCodec: "hevc", AudioCodec: "aac"},
			Plan{Mode: OutputTranscode},
		},
		{
			"AV1：视频必须转码",
			Info{Container: "mov,mp4,m4a,3gp,3g2,mj2", VideoCodec: "av1", AudioCodec: "aac"},
			Plan{Mode: OutputTranscode},
		},
		{
			"MPEG-4：视频必须转码",
			Info{Container: "avi", VideoCodec: "mpeg4", AudioCodec: "mp3"},
			Plan{Mode: OutputTranscode},
		},
		{
			"无声 H264：换封装（视频直通，无音频轨道）",
			Info{Container: "matroska,webm", VideoCodec: "h264", AudioCodec: ""},
			Plan{Mode: OutputRemux, CopyVideo: true, CopyAudio: false},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PlanForLocal(&c.info)
			if got != c.want {
				t.Errorf("PlanForLocal() = %+v, 期望 %+v", got, c.want)
			}
			// 视频可复制时绝不能要求重编码，这是本优化的核心不变式。
			if c.info.VideoCodec == "h264" && got.NeedsVideoEncode() {
				t.Errorf("H.264 源不应重编码视频: %+v", got)
			}
		})
	}
}

// TestPlanForOnline 覆盖在线源的决策：yt-dlp 用 avc1/mp4a 这类写法。
func TestPlanForOnline(t *testing.T) {
	cases := []struct {
		name         string
		video, audio string
		want         Plan
	}{
		{
			"B 站 avc1+mp4a：全直通（免转码）",
			"avc1.640033", "mp4a.40.2",
			Plan{Mode: OutputRemux, CopyVideo: true, CopyAudio: true},
		},
		{
			"YouTube avc1+opus：视频直通、音频转 AAC",
			"avc1.640028", "opus",
			Plan{Mode: OutputRemux, CopyVideo: true, CopyAudio: false},
		},
		{
			"AV1：视频必须转码（这正是之前的性能问题）",
			"av01.0.08M.08", "mp4a.40.2",
			Plan{Mode: OutputTranscode},
		},
		{
			"VP9：视频必须转码",
			"vp9", "opus",
			Plan{Mode: OutputTranscode},
		},
		{
			"HEVC：视频必须转码",
			"hvc1.1.6.L150", "mp4a.40.2",
			Plan{Mode: OutputTranscode},
		},
		{
			"mp4a.40.34 实为 MP3：音频需转码",
			"avc1.640033", "mp4a.40.34",
			Plan{Mode: OutputRemux, CopyVideo: true, CopyAudio: false},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PlanForOnline(c.video, c.audio); got != c.want {
				t.Errorf("PlanForOnline(%q, %q) = %+v, 期望 %+v", c.video, c.audio, got, c.want)
			}
		})
	}
}

// TestPlanForLocalNeverDirectForNonMP4 确认非 MP4 容器不会被当作原文件直出，
// 避免电视端因容器不支持而无法播放。
func TestPlanForLocalNeverDirectForNonMP4(t *testing.T) {
	for _, container := range []string{"matroska,webm", "avi", "flv", "mpegts"} {
		info := Info{Container: container, VideoCodec: "h264", AudioCodec: "aac"}
		if got := PlanForLocal(&info); got.IsDirect() {
			t.Errorf("容器 %q 不应直出原文件: %+v", container, got)
		}
	}
}

// TestOutputArgs 校验 ffmpeg 参数确实按 plan 复制或重编码。
func TestOutputArgs(t *testing.T) {
	cases := []struct {
		name      string
		plan      Plan
		wantCopyV bool
		wantCopyA bool
	}{
		{"全复制", Plan{Mode: OutputRemux, CopyVideo: true, CopyAudio: true}, true, true},
		{"复制视频转音频", Plan{Mode: OutputRemux, CopyVideo: true, CopyAudio: false}, true, false},
		{"完整转码", Plan{Mode: OutputTranscode}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := outputArgs(c.plan)
			joined := joinArgs(args)
			hasCopyV := containsSeq(args, "-c:v", "copy")
			hasCopyA := containsSeq(args, "-c:a", "copy")
			if hasCopyV != c.wantCopyV {
				t.Errorf("视频复制 = %v, 期望 %v（参数: %s）", hasCopyV, c.wantCopyV, joined)
			}
			if hasCopyA != c.wantCopyA {
				t.Errorf("音频复制 = %v, 期望 %v（参数: %s）", hasCopyA, c.wantCopyA, joined)
			}
			// 不复制时必须给出对应的编码器。
			if !c.wantCopyV && !containsSeq(args, "-c:v", "libx264") {
				t.Errorf("未复制视频时应使用 libx264: %s", joined)
			}
			if !c.wantCopyA && !containsSeq(args, "-c:a", "aac") {
				t.Errorf("未复制音频时应使用 aac: %s", joined)
			}
			// 输出容器必须是 MPEG-TS，且映射到 stdout。
			if !containsSeq(args, "-f", "mpegts") || args[len(args)-1] != "pipe:1" {
				t.Errorf("输出格式错误: %s", joined)
			}
		})
	}
}

// TestFormatSelectorPrefersH264 确认选择器把 H.264/AAC 排在前面，
// 否则会退回 yt-dlp 默认的 AV1/VP9，导致每次都被迫完整转码。
func TestFormatSelectorPrefersH264(t *testing.T) {
	if !strings.Contains(formatSelector, "[vcodec^=avc1]") {
		t.Errorf("选择器应优先 H.264: %s", formatSelector)
	}
	if !strings.Contains(formatSelector, "[acodec^=mp4a]") {
		t.Errorf("选择器应优先 AAC: %s", formatSelector)
	}
	// 必须保留兜底分支，保证任何站点都能选出格式。
	if !strings.Contains(formatSelector, "bv*+ba/b") {
		t.Errorf("选择器缺少兜底分支: %s", formatSelector)
	}
}

// TestIsAAC 覆盖 mp4a.40.* 中并非 AAC 的特例。
func TestIsAAC(t *testing.T) {
	cases := map[string]bool{
		"aac":        true,
		"mp4a.40.2":  true,
		"mp4a.40.5":  true,
		"mp4a.40.34": false, // 实为 MP3
		"mp4a.69":    false,
		"opus":       false,
		"ac3":        false,
		"mp3":        false,
		"":           false,
	}
	for codec, want := range cases {
		if got := isAAC(normalizeCodec(codec)); got != want {
			t.Errorf("isAAC(%q) = %v, 期望 %v", codec, got, want)
		}
	}
}

// TestOrTranscode 确认零值 Plan 会被当作完整转码，避免参数缺失导致 ffmpeg 失败。
func TestOrTranscode(t *testing.T) {
	if got := (Plan{}).orTranscode(); got.Mode != OutputTranscode {
		t.Errorf("零值 Plan 应归一化为完整转码: %+v", got)
	}
	if got := (Plan{Mode: OutputRemux}).orTranscode(); got.Mode != OutputRemux {
		t.Errorf("已设定的 Plan 不应被改写: %+v", got)
	}
}

// joinArgs 便于在断言失败时输出可读的参数串。
func joinArgs(args []string) string { return strings.Join(args, " ") }

// containsSeq 判断 args 中是否存在相邻的 a、b 两个参数。
func containsSeq(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

// TestHighBitDepthRequiresTranscode 回归测试：10-bit H.264（Hi10P）电视无法解码，
// 必须重编码而不能换封装，否则电视端会黑屏。
func TestHighBitDepthRequiresTranscode(t *testing.T) {
	for _, pixFmt := range []string{"yuv420p10le", "yuv444p10le", "yuv420p12le", "yuv420p16le", "yuv444p9le", "p010le"} {
		info := Info{Container: "matroska,webm", VideoCodec: "h264", AudioCodec: "aac", PixFmt: pixFmt}
		plan := PlanForLocal(&info)
		if plan.Mode != OutputTranscode {
			t.Errorf("像素格式 %s 应重编码，实际 plan=%+v", pixFmt, plan)
		}
		if !plan.NeedsVideoEncode() {
			t.Errorf("像素格式 %s 必须重编码视频", pixFmt)
		}
	}
	// 8-bit 与信息缺失都不应因此被判为需要转码。
	for _, pixFmt := range []string{"yuv420p", "yuvj420p", "nv12", ""} {
		info := Info{Container: "matroska,webm", VideoCodec: "h264", AudioCodec: "aac", PixFmt: pixFmt}
		if got := PlanForLocal(&info); got.NeedsVideoEncode() {
			t.Errorf("像素格式 %q 不应触发视频重编码: %+v", pixFmt, got)
		}
	}
}

// TestIsHighBitDepth 覆盖比特深度判定。
func TestIsHighBitDepth(t *testing.T) {
	cases := map[string]bool{
		"yuv420p":     false,
		"yuvj420p":    false,
		"nv12":        false,
		"":            false,
		"yuv420p10le": true,
		"yuv444p10le": true,
		"yuv420p12le": true,
		"yuv420p16le": true,
		"yuv444p9le":  true,
		"p010le":      true,
	}
	for pixFmt, want := range cases {
		if got := isHighBitDepth(pixFmt); got != want {
			t.Errorf("isHighBitDepth(%q) = %v, 期望 %v", pixFmt, got, want)
		}
	}
}

func TestMimeTypeFor(t *testing.T) {
	cases := map[string]string{
		"/a/movie.mp4": "video/mp4",
		"/a/movie.m4v": "video/mp4",
		"/a/movie.mov": "video/quicktime",
		"/a/movie.mkv": "video/x-matroska",
		"/a/movie.ts":  "video/mp2t",
		"/a/未知.xyz":    "application/octet-stream",
	}
	for path, want := range cases {
		if got := MimeTypeFor(path); got != want {
			t.Errorf("MimeTypeFor(%q) = %q, 期望 %q", path, got, want)
		}
	}
}
