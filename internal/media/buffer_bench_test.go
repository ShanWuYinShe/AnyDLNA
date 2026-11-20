package media

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestBufferDepthBenefit 量化「预缓冲深度」对播放稳定性的影响。
//
// 现状：ensureLocked 只保证「读者当前要的 256KB」就绪，读完再拉下一段。
// 上游一旦抖动（CDN 限流、换节点、Go 的 4 并发抢同一节点），读者立刻
// 饿死——这就是「同一网络下有的视频正常、有的只有 20 KB/s」的机制。
//
// 这里用真实 B 站直链，模拟 ffmpeg 的读取节奏（256KB 一次、消费速率
// 固定），比较「无预缓冲」与「预先填 N 秒缓冲」下的卡顿次数。
// 设 ANYDLNA_BILI_PAGE 才跑。
func TestBufferDepthBenefit(t *testing.T) {
	page := os.Getenv("ANYDLNA_BILI_PAGE")
	if page == "" {
		t.Skip("未设置 ANYDLNA_BILI_PAGE")
	}
	opts := Options{ProxyMode: ProxyModeSystem}
	urls, headers, variants, _, err := resolveURLsWithVariants(context.Background(), page, opts)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	up, err := newUpstreamWithVariants(urls[0], headers[0], variants[0], opts)
	if err != nil {
		t.Fatalf("上游失败: %v", err)
	}
	defer up.Close()
	size := up.Size()
	t.Logf("上游长度 %.0f MB", float64(size)/1048576)

	srv := httptest.NewServer(up)
	defer srv.Close()

	// 模拟播放：以 1080P 码率（0.9 MB/s）消费，统计每次读取的等待时间。
	const chunk = 256 * 1024
	const bitrate = 0.9 * 1024 * 1024 // B/s
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", 60<<20))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	buf := make([]byte, chunk)
	var read, stalled int
	var maxWait time.Duration
	start := time.Now()
	for read < 60<<20 && time.Since(start) < 45*time.Second {
		want := time.Duration(float64(read)/bitrate*float64(time.Second))
		if elapsed := time.Since(start); elapsed < want {
			time.Sleep(want - elapsed)
		}
		t0 := time.Now()
		n, rerr := resp.Body.Read(buf)
		wait := time.Since(t0)
		if wait > maxWait {
			maxWait = wait
		}
		// 单次等待超过 200ms 视为卡顿（播放器会重新缓冲）。
		if wait > 200*time.Millisecond {
			stalled++
		}
		read += n
		if rerr != nil {
			break
		}
	}
	t.Logf("按 0.9MB/s 消费 45 秒：共读 %.1f MB，卡顿 %d 次，最长单次等待 %s",
		float64(read)/1048576, stalled, maxWait.Round(time.Millisecond))
	if stalled > 3 {
		t.Errorf("卡顿 %d 次过多：说明缺预缓冲，上游抖动直接传导到播放端", stalled)
	}
}
