package media

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestCandidateSelectionReliability 验证「全量候选 + 并发择优」对投屏成功率的
// 提升：连续多轮真实解析并建上游，统计成功率。
//
// 背景：此前只取 yt-dlp 选中的一条直链，实测连续 6 次解析都落到同一个
// Connection refused 的节点，成功率约 50%。官方客户端稳定，是因为它一次
// 拿到全部节点自行择优。本用例量化修复后的成功率。
// 设 ANYDLNA_BILI_PAGE 才跑。
func TestCandidateSelectionReliability(t *testing.T) {
	page := os.Getenv("ANYDLNA_BILI_PAGE")
	if page == "" {
		t.Skip("未设置 ANYDLNA_BILI_PAGE")
	}
	opts := Options{ProxyMode: ProxyModeSystem}
	const rounds = 5
	ok := 0
	for i := 0; i < rounds; i++ {
		urls, headers, variants, _, err := resolveURLsWithVariants(context.Background(), page, opts)
		if err != nil || len(urls) == 0 {
			t.Logf("第%d轮: 解析失败 %v", i+1, err)
			continue
		}
		var hs map[string]string
		if len(headers) > 0 {
			hs = headers[0]
		}
		cands := 1
		if len(variants) > 0 {
			cands += len(variants[0])
		}
		t0 := time.Now()
		up, uerr := newUpstreamWithVariants(urls[0], hs, firstOrNil(variants), opts)
		elapsed := time.Since(t0)
		if uerr != nil {
			t.Logf("第%d轮: 失败（%d 个候选）耗时 %s: %v", i+1, cands, elapsed.Round(time.Millisecond), uerr)
			continue
		}
		v, a, h := up.CurrentStream()
		t.Logf("第%d轮: 成功（%d 个候选）耗时 %s 选中编码=%s/%s 高度=%d",
			i+1, cands, elapsed.Round(time.Millisecond), v, a, h)
		up.Close()
		ok++
		clearResolveCacheForTest()
		time.Sleep(1500 * time.Millisecond)
	}
	t.Logf("成功率 %d/%d", ok, rounds)
	if ok < rounds {
		t.Errorf("成功率 %d/%d，候选择优未完全消除节点不可达的影响", ok, rounds)
	}
}

func firstOrNil(v [][]resolvedVariant) []resolvedVariant {
	if len(v) == 0 {
		return nil
	}
	return v[0]
}

// clearResolveCacheForTest 清空解析缓存，迫使下一轮重新解析。
func clearResolveCacheForTest() {
	resolveCacheMu.Lock()
	defer resolveCacheMu.Unlock()
	loadResolveCacheLocked()
	resolveCache = map[string]resolveCacheEntry{}
}
