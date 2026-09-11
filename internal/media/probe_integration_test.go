package media

import (
	"context"
	"os"
	"testing"
)

// TestProbeIntegration 依赖 ffmpeg 生成真实样本的集成测试。
// 设置 ANYDLNA_PROBE_SAMPLE=<文件路径> 时才会运行。
func TestProbeIntegration(t *testing.T) {
	path := os.Getenv("ANYDLNA_PROBE_SAMPLE")
	if path == "" {
		t.Skip("未设置 ANYDLNA_PROBE_SAMPLE，跳过集成测试")
	}
	info, err := Probe(context.Background(), path)
	if err != nil {
		t.Fatalf("Probe 失败: %v", err)
	}
	t.Logf("探测结果: %+v", info)
	if info.VideoCodec == "" {
		t.Error("未解析出视频编码")
	}
	if info.DurationSec <= 0 {
		t.Error("未解析出时长")
	}
	if info.Title == "" {
		t.Error("未解析出标题")
	}
	// 判定结果与编码一致：h264+aac+mp4 应直出。
	if got := info.NeedsTranscode(); got != (info.VideoCodec != "h264" || info.AudioCodec != "aac") {
		t.Errorf("NeedsTranscode=%v 与编码 %s/%s 不一致", got, info.VideoCodec, info.AudioCodec)
	}
}
