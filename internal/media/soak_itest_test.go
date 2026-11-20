package media

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestBilibiliSoak 对整段视频持续拉流 60 秒，统计分段速率与失败次数。
//
// 单次短读只能说明「起播没问题」，用户抱怨的是「播一会儿就卡」。这里
// 顺序读完整段（模拟 ffmpeg 边播边读），暴露中途断流、空洞与速率塌陷。
func TestBilibiliSoak(t *testing.T) {
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
		t.Fatalf("上游建立失败: %v", err)
	}
	defer up.Close()
	size := up.Size()
	t.Logf("上游长度 %d（%.0f MB），编码变体 %d 个", size, float64(size)/1048576, len(variants[0]))

	srv := httptest.NewServer(up)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", size-1))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	start := time.Now()
	var total int64
	lastAt := start
	var lastN int64
	buf := make([]byte, 256*1024)
	for time.Since(start) < 60*time.Second {
		n, rerr := resp.Body.Read(buf)
		total += int64(n)
		if el := time.Since(lastAt); el >= 10*time.Second {
			seg := float64(total-lastN) / el.Seconds() / 1048576
			t.Logf("累计 %5.1f MB，最近 10s 速率 %.2f MB/s", float64(total)/1048576, seg)
			lastAt, lastN = time.Now(), total
		}
		if rerr != nil {
			if rerr == io.EOF {
				t.Logf("读到结尾，累计 %.1f MB", float64(total)/1048576)
			} else {
				t.Errorf("读取中断: %v（累计 %.1f MB）", rerr, float64(total)/1048576)
			}
			break
		}
	}
	overall := float64(total) / time.Since(start).Seconds() / 1048576
	t.Logf("60s 内共 %.1f MB，平均 %.2f MB/s", float64(total)/1048576, overall)
	if overall < 1.0 {
		t.Errorf("持续吞吐 %.2f MB/s 不足以播放 1080P", overall)
	}
}
