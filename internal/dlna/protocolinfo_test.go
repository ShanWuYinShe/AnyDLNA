package dlna

import (
	"strings"
	"testing"
)

// 真实设备上报的 Sink 片段（取自一台实际电视的 GetProtocolInfo 响应）。
// 该设备不做 DLNA profile 声明，只给 MIME 通配，是常见的简化实现。
const realSinkSample = "http-get:*:image/jpeg:*,http-get:*:video/mp4:*,http-get:*:video/MP2T:*," +
	"http-get:*:video/x-matroska:*,http-get:*:video/mkv:*,http-get:*:video/webm:*," +
	"http-get:*:audio/mpeg:*"

// 符合 DLNA 规范的设备会带上 profile 与操作参数。
const compliantSinkSample = "http-get:*:video/mp4:DLNA.ORG_PN=AVC_MP4_MP_HD_1080i_AAC;DLNA.ORG_OP=01;DLNA.ORG_CI=0," +
	"http-get:*:video/vnd.dlna.mpeg-tts:DLNA.ORG_PN=MPEG_TS_HD_NA;DLNA.ORG_OP=01"

// TestParseProtocolInfoRealDevice 用真实设备数据验证解析。
func TestParseProtocolInfoRealDevice(t *testing.T) {
	infos := ParseProtocolInfo(realSinkSample)
	if len(infos) != 7 {
		t.Fatalf("应解析出 7 条，实际 %d", len(infos))
	}
	// 每条都应拆出 protocol/network/mime 三段。
	for _, info := range infos {
		if info.Protocol != "http-get" || info.Network != "*" || info.MIME == "" {
			t.Errorf("条目解析异常: %+v", info)
		}
	}
	// 该设备不做 profile 声明，因此 Profile 为空。
	for _, info := range infos {
		if info.Profile != "" {
			t.Errorf("该设备未声明 profile，却解析出 %q", info.Profile)
		}
	}
}

// TestParseProtocolInfoCompliantDevice 验证带 DLNA 参数的规范写法。
func TestParseProtocolInfoCompliantDevice(t *testing.T) {
	infos := ParseProtocolInfo(compliantSinkSample)
	if len(infos) != 2 {
		t.Fatalf("应解析出 2 条，实际 %d", len(infos))
	}
	if infos[0].Profile != "AVC_MP4_MP_HD_1080i_AAC" {
		t.Errorf("profile 解析错误: %q", infos[0].Profile)
	}
	// 未在已核实的规范中确认 DLNA.ORG_OP 的位含义，故只保留参数原文不解释。
	if infos[0].Params == "" {
		t.Error("应保留第四段参数原文")
	}
	if infos[1].MIME != "video/vnd.dlna.mpeg-tts" {
		t.Errorf("MIME 解析错误: %q", infos[1].MIME)
	}
}

// TestParseProtocolInfoEdgeCases 覆盖设备实现的常见瑕疵。
func TestParseProtocolInfoEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"空串", "", 0},
		{"仅空白", "   ", 0},
		{"多余逗号", "http-get:*:video/mp4:*,", 1},
		{"条目间空白", " http-get:*:video/mp4:* , http-get:*:video/mp2t:* ", 2},
		{"段数不足的条目被跳过", "http-get:*", 0},
		{"混合有效与无效", "bad,http-get:*:video/mp4:*", 1},
		{"缺 MIME 的条目被跳过", "http-get:*::*", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseProtocolInfo(c.raw); len(got) != c.want {
				t.Errorf("ParseProtocolInfo(%q) 得到 %d 条，期望 %d", c.raw, len(got), c.want)
			}
		})
	}
}

// TestProtocolCapabilitiesQueriedFlag 确认未查询时一切能力判断都为否，
// 这是「设备不支持协商时回退保守策略」的基础。
func TestProtocolCapabilitiesQueriedFlag(t *testing.T) {
	var unqueried ProtocolCapabilities
	if unqueried.SupportsMIME("video/mp4") {
		t.Error("未查询时不应报告支持任何格式")
	}
	if got := unqueried.VideoMIMEs(); len(got) != 0 {
		t.Errorf("未查询时不应有格式列表: %v", got)
	}

	queried := ProtocolCapabilities{Sink: ParseProtocolInfo(realSinkSample), Queried: true}
	if !queried.SupportsMIME("video/mp4") {
		t.Error("应报告支持 video/mp4")
	}
	if !queried.SupportsMIME("video/mp2t") {
		t.Error("video/MP2T 应大小写无关地匹配 video/mp2t")
	}
}

// TestVideoMIMEs 确认列表去重、排序且只含视频。
func TestVideoMIMEs(t *testing.T) {
	caps := ProtocolCapabilities{
		// 故意混入重复项与音频项。
		Sink:    ParseProtocolInfo("http-get:*:video/mp4:*,http-get:*:video/x-mp4:*,http-get:*:audio/mpeg:*,http-get:*:video/mp2t:*"),
		Queried: true,
	}
	got := caps.VideoMIMEs()
	for _, mime := range got {
		if !strings.HasPrefix(mime, "video/") {
			t.Errorf("不应包含非视频格式: %q", mime)
		}
	}
	// video/mp4 与 video/x-mp4 是同一格式的不同写法，应去重为 video/mp4。
	seen := map[string]bool{}
	for _, mime := range got {
		if seen[mime] {
			t.Errorf("格式重复: %q", mime)
		}
		seen[mime] = true
	}
	if !seen["video/mp4"] || !seen["video/mp2t"] {
		t.Errorf("应包含 video/mp4 与 video/mp2t: %v", got)
	}
	// 排序应稳定。
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("结果未排序: %v", got)
			break
		}
	}
}

// TestNormalizeMIME 覆盖各厂商的 MIME 书写变体归一化。
// 这是跨厂商比较的基础，两处规则必须只在这里维护。
func TestNormalizeMIME(t *testing.T) {
	cases := map[string]string{
		"video/mp4":                 "video/mp4",
		"VIDEO/MP4":                 "video/mp4",
		"  video/mp4  ":             "video/mp4",
		"video/x-mp4":               "video/mp4",
		"video/x-m4v":               "video/mp4",
		"video/mp2t":                "video/mp2t",
		"video/MP2T":                "video/mp2t",
		"video/vnd.dlna.mpeg-tts":   "video/mp2t",
		"video/x-matroska":          "video/x-matroska",
		"video/mkv":                 "video/x-matroska",
		"video/x-mkv":               "video/x-matroska",
		"video/avi":                 "video/x-msvideo",
		"video/divx":                "video/x-msvideo",
		"video/flv":                 "video/x-flv",
		"video/mp4;DLNA.ORG_PN=xyz": "video/mp4",
		"":                          "",
	}
	for in, want := range cases {
		if got := NormalizeMIME(in); got != want {
			t.Errorf("NormalizeMIME(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// TestIsKnownServiceType 确认只保留本应用需要的服务，
// 同时必须包含 ConnectionManager（否则无法协商格式）。
func TestIsKnownServiceType(t *testing.T) {
	for _, svc := range []string{
		AVTransportServiceType,
		RenderingControlServiceType,
		ConnectionManagerServiceType,
	} {
		if !isKnownServiceType(svc) {
			t.Errorf("应保留服务 %q", svc)
		}
	}
	if isKnownServiceType("urn:schemas-upnp-org:service:ContentDirectory:1") {
		t.Error("与本应用无关的服务不应保留")
	}
}
