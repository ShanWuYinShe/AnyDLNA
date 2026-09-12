package media

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capsWith 构造「已查询到能力」的设备能力描述。
func capsWith(mimes ...string) DeviceCapabilities {
	return DeviceCapabilities{Queried: true, MIMEs: mimes}
}

// TestPlanForLocalNegotiation 覆盖本地文件在设备能力已知时的决策。
// 核心期望：设备声明支持该容器时直接投原文件（零开销）；
// 只要视频是 H.264 就绝不重编码视频。
func TestPlanForLocalNegotiation(t *testing.T) {
	caps := capsWith("video/mp4", "video/x-matroska", "video/mp2t")

	cases := []struct {
		name string
		info Info
		want Plan
	}{
		{
			"MP4 H264 AAC + 设备支持 MP4 且 faststart：原文件直出",
			Info{Path: "/a/m.mp4", Container: "mov,mp4,m4a,3gp,3g2,mj2",
				VideoCodec: "h264", AudioCodec: "aac", FastStart: true},
			Plan{Mode: OutputDirect, CopyVideo: true, CopyAudio: true, DirectMIME: "video/mp4"},
		},
		{
			"MKV H264 AAC + 设备支持 MKV：原文件直出（连封装都省了）",
			Info{Path: "/a/m.mkv", Container: "matroska,webm",
				VideoCodec: "h264", AudioCodec: "aac", FastStart: true},
			Plan{Mode: OutputDirect, CopyVideo: true, CopyAudio: true, DirectMIME: "video/x-matroska"},
		},
		{
			"MKV H264 AC3：音频不兼容，换封装（视频仍免转码）",
			Info{Path: "/a/m.mkv", Container: "matroska,webm",
				VideoCodec: "h264", AudioCodec: "ac3", FastStart: true},
			Plan{Mode: OutputRemux, Container: ContainerMPEGTS, CopyVideo: true, CopyAudio: false},
		},
		{
			"MP4 非 faststart：不能直出（需下载完整个文件才能起播），改换封装",
			Info{Path: "/a/m.mp4", Container: "mov,mp4,m4a,3gp,3g2,mj2",
				VideoCodec: "h264", AudioCodec: "aac", FastStart: false},
			Plan{Mode: OutputRemux, Container: ContainerMPEGTS, CopyVideo: true, CopyAudio: true},
		},
		{
			"HEVC：视频必须转码",
			Info{Path: "/a/m.mp4", Container: "mov,mp4,m4a,3gp,3g2,mj2",
				VideoCodec: "hevc", AudioCodec: "aac", FastStart: true},
			Plan{Mode: OutputTranscode, Container: ContainerMPEGTS},
		},
		{
			"AV1：视频必须转码",
			Info{Path: "/a/m.mp4", Container: "mov,mp4,m4a,3gp,3g2,mj2",
				VideoCodec: "av1", AudioCodec: "aac", FastStart: true},
			Plan{Mode: OutputTranscode, Container: ContainerMPEGTS},
		},
		{
			"MPEG-4：视频必须转码",
			Info{Path: "/a/m.avi", Container: "avi",
				VideoCodec: "mpeg4", AudioCodec: "mp3", FastStart: true},
			Plan{Mode: OutputTranscode, Container: ContainerMPEGTS},
		},
		{
			"无声 H264：换封装（视频直通，无音频轨道）",
			Info{Path: "/a/m.mkv", Container: "matroska,webm",
				VideoCodec: "h264", AudioCodec: "", FastStart: true},
			Plan{Mode: OutputRemux, Container: ContainerMPEGTS, CopyVideo: true, CopyAudio: false},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PlanForLocal(&c.info, caps)
			if got != c.want {
				t.Errorf("PlanForLocal() = %+v, 期望 %+v", got, c.want)
			}
			// 核心不变式：H.264 源绝不重编码视频。
			if c.info.VideoCodec == "h264" && got.NeedsVideoEncode() {
				t.Errorf("H.264 源不应重编码视频: %+v", got)
			}
		})
	}
}

// TestPlanForLocalWithoutCapabilities 覆盖设备能力未知（未提供 ConnectionManager）
// 时的保守回退：不直出，但 H.264 仍走换封装而非转码。
func TestPlanForLocalWithoutCapabilities(t *testing.T) {
	unknown := DeviceCapabilities{}
	info := Info{Path: "/a/m.mp4", Container: "mov,mp4,m4a,3gp,3g2,mj2",
		VideoCodec: "h264", AudioCodec: "aac", FastStart: true}

	got := PlanForLocal(&info, unknown)
	if got.IsDirect() {
		t.Errorf("能力未知时不应直出原文件: %+v", got)
	}
	if got.NeedsVideoEncode() {
		t.Errorf("能力未知时 H.264 仍应免转码: %+v", got)
	}
	if got.Container != ContainerMPEGTS {
		t.Errorf("能力未知时应回退到 MPEG-TS: %+v", got)
	}
}

// TestOutputContainerNegotiation 覆盖容器协商：设备不支持 TS 但支持 MP4 时改用碎片化 MP4。
func TestOutputContainerNegotiation(t *testing.T) {
	onlyMP4 := capsWith("video/mp4")
	info := Info{Path: "/a/m.mkv", Container: "matroska,webm",
		VideoCodec: "h264", AudioCodec: "aac", FastStart: true}

	got := PlanForLocal(&info, onlyMP4)
	if got.Container != ContainerFMP4 {
		t.Errorf("设备只支持 MP4 时应输出碎片化 MP4: %+v", got)
	}
	if got.OutputMIME() != "video/mp4" {
		t.Errorf("输出 MIME 应为 video/mp4: %q", got.OutputMIME())
	}

	// 设备同时支持时优先 TS（通用基线）。
	both := capsWith("video/mp4", "video/mp2t")
	if got := PlanForLocal(&info, both); got.Container != ContainerMPEGTS {
		t.Errorf("同时支持时应优先 MPEG-TS: %+v", got)
	}
}

// TestPlanForOnline 覆盖在线源的决策：yt-dlp 用 avc1/mp4a 这类写法。
func TestPlanForOnline(t *testing.T) {
	caps := capsWith("video/mp4", "video/mp2t")
	cases := []struct {
		name         string
		video, audio string
		caps         DeviceCapabilities
		want         Plan
	}{
		{
			"B 站 avc1+mp4a：全直通（免转码）",
			"avc1.640033", "mp4a.40.2", caps,
			Plan{Mode: OutputRemux, Container: ContainerMPEGTS, CopyVideo: true, CopyAudio: true},
		},
		{
			"YouTube avc1+opus：视频直通、音频转 AAC",
			"avc1.640028", "opus", caps,
			Plan{Mode: OutputRemux, Container: ContainerMPEGTS, CopyVideo: true, CopyAudio: false},
		},
		{
			"AV1：视频必须转码（这正是之前的性能问题）",
			"av01.0.08M.08", "mp4a.40.2", caps,
			Plan{Mode: OutputTranscode, Container: ContainerMPEGTS},
		},
		{
			"VP9：视频必须转码",
			"vp9", "opus", caps,
			Plan{Mode: OutputTranscode, Container: ContainerMPEGTS},
		},
		{
			"HEVC：视频必须转码",
			"hvc1.1.6.L150", "mp4a.40.2", caps,
			Plan{Mode: OutputTranscode, Container: ContainerMPEGTS},
		},
		{
			"mp4a.40.34 实为 MP3：音频需转码",
			"avc1.640033", "mp4a.40.34", caps,
			Plan{Mode: OutputRemux, Container: ContainerMPEGTS, CopyVideo: true, CopyAudio: false},
		},
		{
			"设备不支持 TS 只支持 MP4：换封装为碎片化 MP4",
			"avc1.640033", "mp4a.40.2", capsWith("video/mp4"),
			Plan{Mode: OutputRemux, Container: ContainerFMP4, CopyVideo: true, CopyAudio: true},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PlanForOnline(c.video, c.audio, c.caps); got != c.want {
				t.Errorf("PlanForOnline(%q, %q) = %+v, 期望 %+v", c.video, c.audio, got, c.want)
			}
		})
	}
}

// TestDeviceCapabilitiesSupports 覆盖能力匹配。
//
// 契约：c.MIMEs 由 app 层从 dlna 包转换而来，其中的书写变体
// （video/x-mp4、video/mkv 等）已由 dlna.NormalizeMIME 归一化；
// media 层只做大小写与参数无关的比较（见 probe.go 的说明）。
func TestDeviceCapabilitiesSupports(t *testing.T) {
	caps := capsWith("video/mp2t", "video/x-matroska", "video/mp4")
	for _, mime := range []string{
		"video/mp2t", "VIDEO/MP2T", "Video/Mp2t",
		"video/x-matroska", "video/mp4", "video/mp4; charset=x",
	} {
		if !caps.Supports(mime) {
			t.Errorf("应报告支持 %q", mime)
		}
	}
	if caps.Supports("video/avi") {
		t.Error("未声明的格式不应报告支持")
	}
	if caps.Supports("video/x-mp4") {
		t.Error("书写变体应由 dlna 层归一化后再传入，media 层不应匹配")
	}
	// 未查询到能力时一律返回 false，促使调用方回退保守策略。
	if (DeviceCapabilities{}).Supports("video/mp4") {
		t.Error("能力未查询时不应报告支持")
	}
}

// TestCanonicalMIME 覆盖 media 层的 MIME 比较归一化（只做大小写与参数处理，
// 别名映射由 dlna 层统一负责，避免两处规则漂移）。
func TestCanonicalMIME(t *testing.T) {
	cases := map[string]string{
		"video/mp4":            "video/mp4",
		"  VIDEO/MP4  ":        "video/mp4",
		"video/mp4; charset=x": "video/mp4",
		"video/x-mp4":          "video/x-mp4",
		"":                     "",
	}
	for in, want := range cases {
		if got := canonicalMIME(in); got != want {
			t.Errorf("canonicalMIME(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// TestPlanForLocalNeverDirectForUndeclaredContainer 确认设备未声明支持的容器
// 不会被直接投递，避免设备端无法播放。
func TestPlanForLocalNeverDirectForUndeclaredContainer(t *testing.T) {
	// 设备只声明支持 MP4。
	caps := capsWith("video/mp4")
	for _, tc := range []struct{ path, container string }{
		{"/a/m.mkv", "matroska,webm"},
		{"/a/m.avi", "avi"},
		{"/a/m.flv", "flv"},
		{"/a/m.ts", "mpegts"},
	} {
		info := Info{Path: tc.path, Container: tc.container,
			VideoCodec: "h264", AudioCodec: "aac", FastStart: true}
		if got := PlanForLocal(&info, caps); got.IsDirect() {
			t.Errorf("设备未声明支持 %q，不应直出: %+v", tc.container, got)
		}
	}

	// 设备声明支持 MKV 时，MKV 才允许直出。
	mkvCaps := capsWith("video/x-matroska")
	info := Info{Path: "/a/m.mkv", Container: "matroska,webm",
		VideoCodec: "h264", AudioCodec: "aac", FastStart: true}
	if got := PlanForLocal(&info, mkvCaps); !got.IsDirect() {
		t.Errorf("设备声明支持 MKV 时应直出: %+v", got)
	}
}

// TestOutputArgs 校验 ffmpeg 参数确实按 plan 复制或重编码，并按容器封装。
func TestOutputArgs(t *testing.T) {
	cases := []struct {
		name      string
		plan      Plan
		wantCopyV bool
		wantCopyA bool
		wantFmt   string
	}{
		{"全复制（TS）", Plan{Mode: OutputRemux, Container: ContainerMPEGTS, CopyVideo: true, CopyAudio: true}, true, true, "mpegts"},
		{"复制视频转音频（TS）", Plan{Mode: OutputRemux, Container: ContainerMPEGTS, CopyVideo: true, CopyAudio: false}, true, false, "mpegts"},
		{"完整转码（TS）", Plan{Mode: OutputTranscode, Container: ContainerMPEGTS}, false, false, "mpegts"},
		{"换封装为碎片化 MP4", Plan{Mode: OutputRemux, Container: ContainerFMP4, CopyVideo: true, CopyAudio: true}, true, true, "mp4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := outputArgs(c.plan, 0)
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
			// 输出容器必须符合计划，且映射到 stdout。
			if !containsSeq(args, "-f", c.wantFmt) || args[len(args)-1] != "pipe:1" {
				t.Errorf("输出格式应为 %s: %s", c.wantFmt, joined)
			}
			// 碎片化 MP4 必须带 frag_keyframe+empty_moov，否则无法流式输出。
			if c.wantFmt == "mp4" && !containsSeq(args, "-movflags", "frag_keyframe+empty_moov+default_base_moof") {
				t.Errorf("碎片化 MP4 缺少 movflags: %s", joined)
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

// TestHighBitDepthRequiresTranscode 回归测试：10-bit H.264（Hi10P）设备无法解码，
// 必须重编码而不能直通，否则设备端会黑屏。
func TestHighBitDepthRequiresTranscode(t *testing.T) {
	// 设备声明支持 MKV，仍不能直通 10-bit 内容。
	caps := capsWith("video/x-matroska", "video/mp2t", "video/mp4")
	for _, pixFmt := range []string{"yuv420p10le", "yuv444p10le", "yuv420p12le", "yuv420p16le", "yuv444p9le", "p010le"} {
		info := Info{Path: "/a/m.mkv", Container: "matroska,webm",
			VideoCodec: "h264", AudioCodec: "aac", PixFmt: pixFmt, FastStart: true}
		plan := PlanForLocal(&info, caps)
		if plan.Mode != OutputTranscode {
			t.Errorf("像素格式 %s 应重编码，实际 plan=%+v", pixFmt, plan)
		}
		if !plan.NeedsVideoEncode() {
			t.Errorf("像素格式 %s 必须重编码视频", pixFmt)
		}
	}
	// 8-bit 与信息缺失都不应因此被判为需要转码。
	for _, pixFmt := range []string{"yuv420p", "yuvj420p", "nv12", ""} {
		info := Info{Path: "/a/m.mkv", Container: "matroska,webm",
			VideoCodec: "h264", AudioCodec: "aac", PixFmt: pixFmt, FastStart: true}
		if got := PlanForLocal(&info, caps); got.NeedsVideoEncode() {
			t.Errorf("像素格式 %q 不应触发视频重编码: %+v", pixFmt, got)
		}
	}
}

// TestIsFastStartRealFiles 用真实构造的文件验证 moov 位置检测——
// 这直接决定 MP4 能否边下边播（索引在末尾时设备会长时间黑屏）。
func TestIsFastStartRealFiles(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
		desc string
	}{
		// 由 ffmpeg 以 -movflags +faststart 生成的样本（若不存在则跳过）。
		{"/tmp/dltest/sample_fs.mp4", true, "faststart（moov 前置）"},
		{"/tmp/dltest/sample.mp4", false, "非 faststart（moov 在末尾）"},
	} {
		if _, err := os.Stat(tc.path); err != nil {
			continue // 样本不存在时跳过，避免依赖外部文件。
		}
		got, err := IsFastStart(tc.path)
		if err != nil {
			t.Fatalf("%s 检测失败: %v", tc.desc, err)
		}
		if got != tc.want {
			t.Errorf("%s: IsFastStart=%v, 期望 %v", tc.desc, got, tc.want)
		}
	}
}

// TestIsFastStartRejectsGarbage 确认非 MP4 内容不会误判为可流式。
func TestIsFastStartRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.mp4")
	if err := os.WriteFile(path, []byte("this is definitely not an mp4 file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fast, err := IsFastStart(path); err == nil && fast {
		t.Error("非法内容不应被判为 faststart")
	}
}

// TestProbeSetsFastStart 确认 Probe 会为 MP4 填充 FastStart 字段。
func TestProbeSetsFastStart(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/tmp/dltest/sample_fs.mp4", true},
		{"/tmp/dltest/sample.mp4", false},
	} {
		if _, err := os.Stat(tc.path); err != nil {
			continue
		}
		info, err := Probe(context.Background(), tc.path)
		if err != nil {
			t.Fatalf("Probe(%s) 失败: %v", tc.path, err)
		}
		if info.FastStart != tc.want {
			t.Errorf("%s: FastStart=%v, 期望 %v", tc.path, info.FastStart, tc.want)
		}
		if info.Path != tc.path {
			t.Errorf("Probe 应保留文件路径，得到 %q", info.Path)
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
