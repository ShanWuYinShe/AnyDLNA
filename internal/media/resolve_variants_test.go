package media

import "testing"

// TestFormatVariants 从全量格式里挑同分辨率、同类编码的变体。
//
// 两条硬约束：
//  1. 分辨率必须相同——否则会悄悄改变清晰度；
//  2. 编码类别必须相同（都是 H.264 或都不是）——plan 按主链编码一次性
//     决定是否转码，切换变体不重算 plan。跨类别切换会让 ffmpeg 用
//     -c:v copy 把 HEVC/AV1 塞进 MPEG-TS，电视普遍解不了（黑屏/花屏）。
func TestFormatVariants(t *testing.T) {
	out := []byte(`{
		"title":"T","extractor_key":"BiliBili",
		"requested_formats":[{"url":"https://n1/avc1-1080"}],
		"formats":[
			{"url":"https://n1/avc1-1080","vcodec":"avc1.640032","height":1080},
			{"url":"https://n2/avc1-1080","vcodec":"avc1.640028","height":1080},
			{"url":"https://n3/hev1-1080","vcodec":"hev1.1.6.L150","height":1080},
			{"url":"https://n4/av01-1080","vcodec":"av01.0.08M","height":1080},
			{"url":"https://n5/avc1-720","vcodec":"avc1.640028","height":720},
			{"url":"https://n6/audio","vcodec":"none","acodec":"mp4a.40.2"}
		]
	}`)
	vs := formatVariants(out, "https://n1/avc1-1080")
	if len(vs) != 1 {
		t.Fatalf("应只留 1 个同类编码变体，实际 %d: %+v", len(vs), vs)
	}
	if vs[0].URL != "https://n2/avc1-1080" {
		t.Errorf("应保留同为 H.264 的 n2，实际 %s", vs[0].URL)
	}
	for _, v := range vs {
		if v.URL == "https://n3/hev1-1080" || v.URL == "https://n4/av01-1080" {
			t.Error("HEVC/AV1 变体会让 -c:v copy 直通出电视解不了的流")
		}
		if v.URL == "https://n5/avc1-720" {
			t.Error("不同分辨率的格式不能作为变体（会改变清晰度）")
		}
		if v.URL == "https://n6/audio" {
			t.Error("音频不能顶上视频轨")
		}
	}

	// 主链是 HEVC 时，变体也只能是 HEVC（此时 plan 已按转码走，同类才安全）。
	vs = formatVariants(out, "https://n3/hev1-1080")
	if len(vs) != 1 || vs[0].URL != "https://n4/av01-1080" {
		t.Errorf("HEVC 主链应只接受非 H.264 变体，实际 %+v", vs)
	}

	// JSON 非法或主链未知时返回空。
	if vs := formatVariants([]byte("{"), "https://n1/avc1-1080"); vs != nil {
		t.Error("非法 JSON 应返回 nil")
	}
	if vs := formatVariants(out, "https://unknown/x"); len(vs) != 0 {
		t.Errorf("未知主链应返回空，实际 %+v", vs)
	}
}

// TestCodecFamily 编码归类决定变体能否互换。
func TestCodecFamily(t *testing.T) {
	cases := map[string]string{
		"avc1.640032":   "h264",
		"h264":          "h264",
		"hev1.1.6.L150": "other",
		"hvc1.1.6.L120": "other",
		"av01.0.08M":    "other",
		"vp9":           "other",
		"none":          "none",
		"":              "",
	}
	for in, want := range cases {
		if got := codecFamily(in); got != want {
			t.Errorf("codecFamily(%q)=%q，应得 %q", in, got, want)
		}
	}
}
