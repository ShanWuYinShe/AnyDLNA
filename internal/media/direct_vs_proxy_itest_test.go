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

// TestDirectVsProxySoak 用真实中转代码对比「代理拉取」与「直连拉取」的吞吐。
//
// 结论将决定 B 站这类国内源的中转默认路径：
//   - 若直连更快更稳，Upstream 对国内 CDN 应默认不走代理；
//   - 若代理更稳，维持现状。
//
// 设 ANYDLNA_BILI_PAGE 才跑。
func TestDirectVsProxySoak(t *testing.T) {
	page := os.Getenv("ANYDLNA_BILI_PAGE")
	if page == "" {
		t.Skip("未设置 ANYDLNA_BILI_PAGE")
	}

	for _, mode := range []Options{
		{ProxyMode: ProxyModeSystem}, // 现状：经本机代理
		{ProxyMode: ProxyModeNone},   // 直连：电视同款条件
	} {
		name := "代理"
		if mode.ProxyMode == ProxyModeNone {
			name = "直连"
		}
		t.Run(name, func(t *testing.T) {
			urls, headers, variants, _, err := resolveURLsWithVariants(context.Background(), page, mode)
			if err != nil || len(urls) == 0 {
				t.Fatalf("解析失败: %v", err)
			}
			var hs map[string]string
			if len(headers) > 0 {
				hs = headers[0]
			}
			var vs []resolvedVariant
			if len(variants) > 0 {
				vs = variants[0]
			}
			up, uerr := newUpstreamWithVariants(urls[0], hs, vs, mode)
			if uerr != nil {
				t.Fatalf("建上游失败: %v", uerr)
			}
			defer up.Close()
			srv := httptest.NewServer(up)
			defer srv.Close()

			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", up.Size()-1))
			resp, derr := http.DefaultClient.Do(req)
			if derr != nil {
				t.Fatalf("请求失败: %v", derr)
			}
			defer resp.Body.Close()

			start := time.Now()
			var total int64
			buf := make([]byte, 256*1024)
			for time.Since(start) < 40*time.Second {
				n, rerr := resp.Body.Read(buf)
				total += int64(n)
				if rerr != nil {
					if rerr != io.EOF {
						t.Errorf("%s: 中途断连（%.1f MB 处）: %v", name, float64(total)/1048576, rerr)
					}
					break
				}
			}
			// 必须核对是否拉完：中途断连被误当正常结束会得出错误结论。
			expect := up.Size()
			complete := total == expect
			mbps := float64(total) / time.Since(start).Seconds() / 1048576
			t.Logf("%s: %s 内拉取 %.1f/%.1f MB（完整=%v），平均 %.2f MB/s",
				name, time.Since(start).Round(time.Second), float64(total)/1048576, float64(expect)/1048576, complete, mbps)
			if !complete {
				t.Errorf("%s 未拉完整：%.1f / %.1f MB", name, float64(total)/1048576, float64(expect)/1048576)
			}
			clearResolveCacheForTest()
			time.Sleep(2 * time.Second)
		})
	}
}
