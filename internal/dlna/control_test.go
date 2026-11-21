package dlna

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testCtx() context.Context { return context.Background() }

func TestFormatClock(t *testing.T) {
	cases := []struct {
		sec  float64
		want string
	}{
		{0, "0:00:00"},
		{65, "0:01:05"},
		{3661, "1:01:01"},
		{-5, "0:00:00"},
		{3600 * 100, "100:00:00"},
	}
	for _, c := range cases {
		if got := FormatClock(c.sec); got != c.want {
			t.Errorf("FormatClock(%v) = %q, 期望 %q", c.sec, got, c.want)
		}
	}
}

func TestParseClock(t *testing.T) {
	sec, err := ParseClock("1:01:05")
	if err != nil || sec != 3665 {
		t.Fatalf("ParseClock(\"1:01:05\") = %v, %v", sec, err)
	}
	if _, err := ParseClock("NOT_IMPLEMENTED"); err == nil {
		t.Error("NOT_IMPLEMENTED 应返回错误")
	}
	if _, err := ParseClock(""); err == nil {
		t.Error("空串应返回错误")
	}
}

func TestBuildDIDLMetadata(t *testing.T) {
	meta := BuildDIDLMetadata("复仇者<联盟>&彩蛋", "http://192.168.1.2:5000/f/abc?t=1", "video/mp4")
	for _, want := range []string{
		"复仇者&lt;联盟&gt;&amp;彩蛋",
		`<upnp:class>object.item.videoItem</upnp:class>`,
		`protocolInfo="http-get:*:video/mp4:*"`,
		"http://192.168.1.2:5000/f/abc?t=1",
	} {
		if !strings.Contains(meta, want) {
			t.Errorf("DIDL 缺少 %q: %s", want, meta)
		}
	}
}

func TestBuildDIDLFull(t *testing.T) {
	meta := BuildDIDL(DIDLOptions{
		Title:        "Movie & Trailer",
		URI:          "http://192.168.1.2:5000/f/abc",
		MIME:         "video/mp4",
		DurationSec:  7265, // 2:01:05
		Direct:       true,
		SubtitleURL:  "http://192.168.1.2:5000/s/abc.srt",
		SubtitleType: "srt",
	})
	for _, want := range []string{
		"Movie &amp; Trailer",
		`duration="2:01:05"`,
		`protocolInfo="http-get:*:video/mp4:DLNA.ORG_OP=01;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000"`,
		`xmlns:sec="http://www.sec.co.kr/"`,
		`<res protocolInfo="http-get:*:text/srt:*">http://192.168.1.2:5000/s/abc.srt</res>`,
		`<sec:CaptionInfo sec:type="srt">http://192.168.1.2:5000/s/abc.srt</sec:CaptionInfo>`,
		`<sec:CaptionInfoEx sec:type="srt">http://192.168.1.2:5000/s/abc.srt</sec:CaptionInfoEx>`,
	} {
		if !strings.Contains(meta, want) {
			t.Errorf("DIDL 缺少 %q: %s", want, meta)
		}
	}
}

// TestRendererMute 验证 GetMute 与 SetMute SOAP 调用的编解码行为。
func TestRendererMute(t *testing.T) {
	currentMute := "0"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("SOAPACTION")
		if strings.Contains(action, "GetMute") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:GetMuteResponse xmlns:u="urn:schemas-upnp-org:service:RenderingControl:1">
      <CurrentMute>` + currentMute + `</CurrentMute>
    </u:GetMuteResponse>
  </s:Body>
</s:Envelope>`))
			return
		}
		if strings.Contains(action, "SetMute") {
			currentMute = "1"
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:SetMuteResponse xmlns:u="urn:schemas-upnp-org:service:RenderingControl:1"/>
  </s:Body>
</s:Envelope>`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()

	dev := &Device{
		FriendlyName: "TestTV",
		Services: []Service{
			{Type: RenderingControlServiceType, ControlURL: ts.URL},
		},
	}
	renderer := NewRenderer(dev)
	ctx := context.Background()

	// 1. 初始为非静音
	muted, err := renderer.GetMute(ctx)
	if err != nil {
		t.Fatalf("GetMute 失败: %v", err)
	}
	if muted {
		t.Errorf("初始期望未静音，实际为静音")
	}

	// 2. 设置静音
	if err := renderer.SetMute(ctx, true); err != nil {
		t.Fatalf("SetMute 失败: %v", err)
	}

	// 3. 再次查询应为静音
	muted, err = renderer.GetMute(ctx)
	if err != nil {
		t.Fatalf("GetMute 失败: %v", err)
	}
	if !muted {
		t.Errorf("设置后期望为静音，实际为非静音")
	}
}
