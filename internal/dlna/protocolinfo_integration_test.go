package dlna

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestRealDeviceProtocolInfoIntegration 对真实设备查询格式能力。
// 需要设置 ANYDLNA_TV_HOST=<电视IP> 才会运行（默认跳过，避免依赖具体设备）。
//
// 该测试验证的是通用机制：设备描述 → ConnectionManager → GetProtocolInfo → 解析。
// 不针对任何特定品牌或型号做假设。
func TestRealDeviceProtocolInfoIntegration(t *testing.T) {
	host := os.Getenv("ANYDLNA_TV_HOST")
	if host == "" {
		t.Skip("未设置 ANYDLNA_TV_HOST，跳过真实设备测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dev, err := DescribeByHost(ctx, host)
	if err != nil {
		t.Skipf("设备不可达，跳过: %v", err)
	}
	t.Logf("设备: %s (%s %s)", dev.FriendlyName, dev.Manufacturer, dev.ModelName)
	if !dev.HasConnectionManager() {
		t.Skip("该设备未提供 ConnectionManager，无法协商（应用会回退通用策略）")
	}

	caps, err := NewRenderer(dev).QueryProtocolInfo(ctx)
	if err != nil {
		t.Fatalf("查询格式能力失败: %v", err)
	}
	if !caps.Queried {
		t.Fatal("应报告已查询")
	}
	if len(caps.Sink) == 0 {
		t.Fatal("设备未上报任何格式")
	}

	videos := caps.VideoMIMEs()
	t.Logf("设备声明 %d 条格式，其中视频格式 %d 种", len(caps.Sink), len(videos))

	// 关键断言：解析结果必须是规范化、去重、可比较的 MIME。
	seen := map[string]bool{}
	for _, m := range videos {
		if seen[m] {
			t.Errorf("存在重复格式: %q", m)
		}
		seen[m] = true
	}
	for _, want := range []string{"video/mp4", "video/mp2t", "video/x-matroska"} {
		if caps.SupportsMIME(want) {
			t.Logf("  支持 %s", want)
		} else {
			t.Logf("  不支持 %s", want)
		}
	}

	// 记录该设备的 DLNA profile 声明情况，便于判断其能力列表的可信度。
	withProfile := 0
	for _, info := range caps.Sink {
		if info.Profile != "" {
			withProfile++
		}
	}
	t.Logf("其中带 DLNA.ORG_PN profile 声明的条目: %d/%d", withProfile, len(caps.Sink))
	if withProfile == 0 {
		t.Log("注意：该设备未声明任何具体 profile，其格式列表为 MIME 级声明，" +
			"应用据此判断能否直出，但无法据此推断编码级能力。")
	}
}
