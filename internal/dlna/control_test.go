package dlna

import (
	"context"
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
