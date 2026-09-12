package main

import (
	"context"
	"os"
	"testing"

	"AnyDLNA/internal/media"
)

// TestNegotiationEndToEnd 用真实设备验证「查询能力 → 决定输出方案」的完整链路。
// 需要设置 ANYDLNA_TV_HOST=<电视IP> 才会运行。
//
// 验证的是通用机制而非特定型号：设备上报什么，就据此决定能否免转码。
func TestNegotiationEndToEnd(t *testing.T) {
	host := os.Getenv("ANYDLNA_TV_HOST")
	if host == "" {
		t.Skip("未设置 ANYDLNA_TV_HOST，跳过真实设备测试")
	}

	app := NewApp()
	app.ctx = context.Background()

	info, err := app.AddDeviceManually(host)
	if err != nil {
		t.Skipf("设备不可达，跳过: %v", err)
	}
	t.Logf("设备: %s (%s)", info.Name, info.Host)

	// 1) 界面展示用的能力信息。
	formats := app.DeviceCapabilityInfo(info.UDN)
	t.Logf("能力查询: queried=%v 视频格式=%d 种 MP4=%v MKV=%v TS=%v",
		formats.Queried, len(formats.VideoMIMEs),
		formats.SupportsMP4, formats.SupportsMKV, formats.SupportsTS)

	// 2) 换算成决策输入。
	caps := app.capabilitiesFor(info.UDN)
	if caps.Queried != formats.Queried {
		t.Errorf("能力查询结果不一致: %v vs %v", caps.Queried, formats.Queried)
	}

	// 3) 本地 MP4（H.264+AAC 且 faststart）：设备声明支持 MP4 时应直出。
	local := &media.Info{
		Path: "/tmp/movie.mp4", Container: "mov,mp4,m4a,3gp,3g2,mj2",
		VideoCodec: "h264", AudioCodec: "aac", FastStart: true,
	}
	localPlan := media.PlanForLocal(local, caps)
	t.Logf("本地 MP4 → mode=%s container=%s mime=%s",
		localPlan.Mode, localPlan.Container, localPlan.OutputMIME())
	if formats.SupportsMP4 {
		if !localPlan.IsDirect() {
			t.Errorf("设备声明支持 MP4，本地 MP4 应直出，实际 %s", localPlan.Mode)
		}
	} else if localPlan.Mode == media.OutputDirect {
		t.Error("设备未声明支持 MP4，不应直出")
	}

	// 4) 非 faststart 的 MP4 不能直出（否则设备要下完整个文件才起播）。
	slow := *local
	slow.FastStart = false
	if media.PlanForLocal(&slow, caps).IsDirect() {
		t.Error("非 faststart 的 MP4 不应直出")
	}

	// 5) 在线源（B 站 H.264+AAC）：只能换封装，且视频必须免转码。
	onlinePlan := media.PlanForOnline("avc1.640033", "mp4a.40.2", caps)
	t.Logf("在线 H.264 → mode=%s container=%s", onlinePlan.Mode, onlinePlan.Container)
	if onlinePlan.NeedsVideoEncode() {
		t.Error("在线 H.264 源不应重编码视频")
	}

	// 6) 设备不支持的编码仍必须转码，不受能力声明影响。
	hevc := *local
	hevc.VideoCodec = "hevc"
	if !media.PlanForLocal(&hevc, caps).NeedsVideoEncode() {
		t.Error("HEVC 必须重编码视频，与设备容器能力无关")
	}
}
