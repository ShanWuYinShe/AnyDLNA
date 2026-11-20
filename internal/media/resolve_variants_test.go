package media

import "testing"

// TestFormatVariants 候选必须覆盖全部可用档位，并按择优顺序排列。
//
// 背景：B 站把各档位分散在多个 CDN 节点上，实测相当比例节点直接
// Connection refused，且重新解析总是返回同一坏节点。只留「同分辨率同编码」
// 一个候选时，主链一坏就彻底没得选——必须一次解析就留下全部候选。
//
// 排序意图：先同分辨率同编码（画质与转码策略都不变）→ 同分辨率异编码
// （画质不变，可能要转码）→ 降分辨率（画质降但通常免转码）。
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
	if len(vs) != 4 {
		t.Fatalf("应保留 4 个视频候选，实际 %d: %+v", len(vs), vs)
	}
	// 第一条必须是同分辨率同编码（最优）。
	if vs[0].URL != "https://n2/avc1-1080" {
		t.Errorf("首选应为同分辨率同编码的 n2，实际 %s", vs[0].URL)
	}
	// 同分辨率的异编码候选排在降分辨率之前。
	order := make(map[string]int, len(vs))
	for i, v := range vs {
		order[v.URL] = i
	}
	if order["https://n3/hev1-1080"] > order["https://n5/avc1-720"] {
		t.Error("同分辨率异编码候选应排在降分辨率候选之前")
	}
	// 音频不能混进视频候选。
	for _, v := range vs {
		if v.URL == "https://n6/audio" {
			t.Error("音频不能顶上视频轨")
		}
	}
	// 主链自身不出现在候选里。
	for _, v := range vs {
		if v.URL == "https://n1/avc1-1080" {
			t.Error("主链不应重复出现在候选中")
		}
	}

	// JSON 非法或主链未知时返回空。
	if vs := formatVariants([]byte("{"), "https://n1/avc1-1080"); vs != nil {
		t.Error("非法 JSON 应返回 nil")
	}
	if vs := formatVariants(out, "https://unknown/x"); len(vs) != 0 {
		t.Errorf("未知主链应返回空，实际 %+v", vs)
	}
}

// TestCodecFamily 编码归类决定重算 plan 时的能力判断。
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
