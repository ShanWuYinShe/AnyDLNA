package media

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestBilibiliChain 用真实 B 站视频验证完整拉流链路：
// 解析（含 Referer）→ 上游并发 Range → 本机 Range 服务。
//
// 覆盖的真实缺陷：
//   - 直链缺 Referer 时 CDN 返回 403（必须回放 http_headers）；
//   - 所选格式的 CDN 节点不可达时，换同分辨率其它编码变体。
//
// 设 ANYDLNA_BILI_PAGE 才跑（需外网与代理）。
func TestBilibiliChain(t *testing.T) {
	page := os.Getenv("ANYDLNA_BILI_PAGE")
	if page == "" {
		t.Skip("未设置 ANYDLNA_BILI_PAGE，跳过真实站点验证")
	}
	opts := Options{ProxyMode: ProxyModeSystem}
	urls, headers, variants, err := resolveURLsWithVariants(context.Background(), page, opts)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(urls) == 0 || len(headers) == 0 {
		t.Fatalf("直链或请求头缺失: urls=%d headers=%d", len(urls), len(headers))
	}
	t.Logf("直链 %d 条，第 1 条有 %d 个编码变体", len(urls), len(variants[0]))
	if ref := headers[0]["Referer"]; ref == "" {
		t.Error("请求头缺少 Referer：B 站 CDN 会返回 403")
	}

	up, err := newUpstreamWithVariants(urls[0], headers[0], variants[0], opts)
	if err != nil {
		t.Fatalf("上游建立失败（主链与所有变体均不可达）: %v", err)
	}
	defer up.Close()
	t.Logf("上游就绪 长度=%d Range=%v", up.Size(), up.ranged)

	srv := httptest.NewServer(up)
	defer srv.Close()

	const want = 8 << 20
	t0 := time.Now()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	req.Header.Set("Range", "bytes=0-"+itoa(want-1))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("状态应为 206，实际 %d", resp.StatusCode)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	d := time.Since(t0)
	mbps := float64(n) / d.Seconds() / 1048576
	t.Logf("读取 %d 字节 / %s = %.2f MB/s", n, d.Round(time.Millisecond), mbps)
	if mbps < 0.5 {
		t.Errorf("吞吐 %.2f MB/s 过低，无法支撑 1080P 播放", mbps)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
