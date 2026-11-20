package media

import (
	"net/http"
	"testing"
)

// TestNormalizeUpstreamHeaders 只回放媒体分片真正必需的头。
//
// 背景：B 站直链缺 Referer 会被 CDN 返回 403；而 yt-dlp 附带的
// Accept: text/html、Sec-Fetch-Mode: navigate 等是「抓页面」语义，
// 拿去请求媒体分片没有依据，也不该发送。
func TestNormalizeUpstreamHeaders(t *testing.T) {
	got := normalizeUpstreamHeaders(map[string]string{
		"Referer":         "https://www.bilibili.com/video/BV1xx/",
		"User-Agent":      "UA/1.0",
		"Origin":          "https://www.bilibili.com",
		"Accept":          "text/html",
		"Accept-Language": "en-us",
		"Sec-Fetch-Mode":  "navigate",
		"Cookie":          "secret=1",
		"Authorization":   "Bearer x",
		"Content-Length":  "100",
		"X-Custom-Header": "y",
	})
	if got["Referer"] == "" {
		t.Error("Referer 必须保留：B 站 CDN 缺它直接 403")
	}
	if got["User-Agent"] != "UA/1.0" {
		t.Errorf("UA 应保留，实际 %q", got["User-Agent"])
	}
	if got["Origin"] == "" {
		t.Error("Origin 应保留")
	}
	for _, k := range []string{"Accept", "Accept-Language", "Sec-Fetch-Mode", "Cookie", "Authorization", "Content-Length", "X-Custom-Header"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s 不应回放", k)
		}
	}
	if normalizeUpstreamHeaders(nil) != nil {
		t.Error("空输入应返回 nil")
	}
	if normalizeUpstreamHeaders(map[string]string{"Accept": "x"}) != nil {
		t.Error("无可用头时应返回 nil（调用方退回默认 UA）")
	}
}

// TestApplyHeadersDefaultsUA 直链没带 UA 时补默认值。
func TestApplyHeadersDefaultsUA(t *testing.T) {
	up := &Upstream{headers: map[string]string{"Referer": "https://ref/"}}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/v", nil)
	up.applyHeaders(req)
	if req.Header.Get("Referer") != "https://ref/" {
		t.Error("Referer 应写入请求")
	}
	if req.Header.Get("User-Agent") != upstreamUA {
		t.Errorf("缺 UA 时应补默认值，实际 %q", req.Header.Get("User-Agent"))
	}
}
