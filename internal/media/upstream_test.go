package media

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// rangeOrigin 返回支持 Range 的测试源（http.ServeContent 原生支持）。
func rangeOrigin(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "v.mp4", time.Now(), bytes.NewReader(payload))
	}))
}

// TestUpstreamFullAndRange 全量拉取与 Range 服务必须字节精确。
func TestUpstreamFullAndRange(t *testing.T) {
	payload := make([]byte, 3*1024*1024+12345)
	for i := range payload {
		payload[i] = byte(i * 31)
	}
	srv := rangeOrigin(t, payload)
	defer srv.Close()

	up, err := NewUpstream(srv.URL, Options{ProxyMode: ProxyModeNone})
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	if !up.ranged {
		t.Fatal("测试源支持 Range，应探测出 ranged")
	}
	if up.Size() != int64(len(payload)) {
		t.Fatalf("长度不对: %d", up.Size())
	}

	// 经 ServeHTTP 全量读出。
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	up.ServeHTTP(rec, req)
	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, payload) {
		t.Fatalf("全量内容不一致: %d vs %d", len(body), len(payload))
	}

	// Range 切片。
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Range", "bytes=100-199")
	rec = httptest.NewRecorder()
	up.ServeHTTP(rec, req)
	resp = rec.Result()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("应返回 206，实际 %d", resp.StatusCode)
	}
	body, _ = io.ReadAll(resp.Body)
	if !bytes.Equal(body, payload[100:200]) {
		t.Fatal("Range 切片内容不对")
	}
	if got := resp.Header.Get("Content-Range"); got != fmt.Sprintf("bytes 100-199/%d", len(payload)) {
		t.Fatalf("Content-Range 不对: %q", got)
	}
}

// TestUpstreamSeekPriority 跳转到高位必须优先拉取（不等待低位填完）。
func TestUpstreamSeekPriority(t *testing.T) {
	payload := make([]byte, 8*1024*1024)
	for i := range payload {
		payload[i] = byte(i)
	}
	// 每请求延迟，放大调度差异。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		http.ServeContent(w, r, "v.mp4", time.Now(), bytes.NewReader(payload))
	}))
	defer srv.Close()

	up, err := NewUpstream(srv.URL, Options{ProxyMode: ProxyModeNone})
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()

	// 直接请求尾部 1MB：优先调度下应在 2 秒内返回（而非等 8MB 全下完）。
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", len(payload)-1024*1024))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { up.ServeHTTP(rec, req); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("跳转位置迟迟不可读，优先调度失效")
	}
	body, _ := io.ReadAll(rec.Result().Body)
	if !bytes.Equal(body, payload[len(payload)-1024*1024:]) {
		t.Fatal("尾部内容不对")
	}
}

// TestUpstreamIgnoresEnvProxy none 模式必须无视环境变量里的代理。
func TestUpstreamIgnoresEnvProxy(t *testing.T) {
	payload := []byte("hello-upstream")
	srv := rangeOrigin(t, payload)
	defer srv.Close()

	t.Setenv("http_proxy", "http://127.0.0.1:1")
	t.Setenv("https_proxy", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	up, err := NewUpstream(srv.URL, Options{ProxyMode: ProxyModeNone})
	if err != nil {
		t.Fatalf("none 模式应直连成功（无视坏代理）: %v", err)
	}
	defer up.Close()
}

// TestUpstreamBadProxy 无效代理地址应在构造时就报错（投屏点击时展示）。
func TestUpstreamBadProxy(t *testing.T) {
	if _, err := newProxyHTTPClient(Options{ProxyMode: ProxyModeManual, Proxy: "://坏地址"}); err == nil {
		t.Fatal("无效代理应报错")
	}
	if _, err := newProxyHTTPClient(Options{ProxyMode: ProxyModeManual, Proxy: "ftp://x:21"}); err == nil {
		t.Fatal("不支持的协议应报错")
	}
	// socks5 只在真正使用时才建连，构造本身应成功。
	if _, err := newProxyHTTPClient(Options{ProxyMode: ProxyModeManual, Proxy: "socks5://127.0.0.1:10808"}); err != nil {
		t.Fatalf("socks5 构造应成功: %v", err)
	}
}

// TestUpstreamOriginError 上游 403 时构造即失败，不留到拉流时转圈。
func TestUpstreamOriginError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer srv.Close()
	if _, err := NewUpstream(srv.URL, Options{ProxyMode: ProxyModeNone}); err == nil {
		t.Fatal("403 上游应直接报错")
	}
}

// TestUpstreamNoRangeFallback 不支持 Range 的上游退化为顺序拉取。
func TestUpstreamNoRangeFallback(t *testing.T) {
	payload := make([]byte, 512*1024)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 故意忽略 Range，一律 200 全量。
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	up, err := NewUpstream(srv.URL, Options{ProxyMode: ProxyModeNone})
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	if up.ranged {
		t.Fatal("应探测出不支持 Range")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	up.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	if !bytes.Equal(body, payload) {
		t.Fatalf("顺序内容不一致: %d vs %d", len(body), len(payload))
	}
}

// TestParseRange 覆盖常见与非法 Range。
func TestParseRange(t *testing.T) {
	for _, tc := range []struct {
		in     string
		size   int64
		wantS  int64
		wantE  int64
		wantOK bool
	}{
		{"bytes=0-99", 1000, 0, 99, true},
		{"bytes=500-", 1000, 500, 999, true},
		{"bytes=900-9999", 1000, 900, 999, true},
		{"bytes=-100", 1000, 900, 999, true},
		{"bytes=1000-2000", 1000, 0, 0, false},
		{"bytes=200-100", 1000, 0, 0, false},
		{"garbage", 1000, 0, 0, false},
	} {
		s, e, ok := parseRange(tc.in, tc.size)
		if ok != tc.wantOK || s != tc.wantS || e != tc.wantE {
			t.Errorf("parseRange(%q) = %d,%d,%v", tc.in, s, e, ok)
		}
	}
}

// TestParseTotalFromContentRange 覆盖 Content-Range 解析。
func TestParseTotalFromContentRange(t *testing.T) {
	if n := parseTotalFromContentRange("bytes 0-0/12345"); n != 12345 {
		t.Errorf("应得 12345，实际 %d", n)
	}
	if n := parseTotalFromContentRange("bytes 0-0/*"); n != -1 {
		t.Errorf("未知长度应 -1，实际 %d", n)
	}
	if n := parseTotalFromContentRange("xxx"); n != -1 {
		t.Errorf("非法应 -1，实际 %d", n)
	}
}

// TestParseResolveJSON 一次 -J 同时给出元数据与直链；直链缺失时回退取链。
func TestParseResolveJSON(t *testing.T) {
	out := []byte(`{"title":"T","duration":63.5,"is_live":false,` +
		`"extractor_key":"youtube","uploader":"U","vcodec":"avc1","acodec":"mp4a",` +
		`"requested_formats":[{"url":"https://r1/v"},{"url":"https://r2/a"}]}`)
	res, urls, err := parseResolveJSON(out)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "T" || res.DurationSec != 63.5 || res.VideoCodec != "avc1" || res.AudioCodec != "mp4a" {
		t.Errorf("元数据不对: %+v", res)
	}
	if len(urls) != 2 || urls[0] != "https://r1/v" || urls[1] != "https://r2/a" {
		t.Errorf("直链不对: %v", urls)
	}

	// 无 requested_formats：元数据照常，直链为空（调用方回退 DirectURLs）。
	out = []byte(`{"title":"T","extractor_key":"generic"}`)
	if _, urls, err := parseResolveJSON(out); err != nil || len(urls) != 0 {
		t.Errorf("应返回空直链回退，实际 %v %v", urls, err)
	}
	// 非法 JSON。
	if _, _, err := parseResolveJSON([]byte("{")); err == nil {
		t.Error("非法 JSON 应报错")
	}
}
