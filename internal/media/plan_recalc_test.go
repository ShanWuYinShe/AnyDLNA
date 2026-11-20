package media

import "testing"

// TestEffectivePlanFollowsActualStream 换候选后必须按实际在用的流重算输出方案。
//
// 场景：投屏时 yt-dlp 选的是 1080p/avc1（免转码直通），但该节点不可达，
// 传输层换到了 720p/avc1 或 1080p/hev1。若仍沿用投屏时算出的 plan：
//   - 换到 hev1：ffmpeg 按 -c:v copy 把 HEVC 塞进 MPEG-TS，电视解不了（黑屏）；
//   - 换到更低分辨率：多做了本不需要的转码（白耗 CPU，反而卡）。
func TestEffectivePlanFollowsActualStream(t *testing.T) {
	// 无 directUps（本地文件/摄像头/管道）：plan 原样返回。
	tc := &Transcoder{plan: Plan{Mode: OutputRemux, CopyVideo: true}}
	if got := tc.effectivePlanLocked(); got.Mode != OutputRemux {
		t.Errorf("无在线上游时应返回原 plan，实际 %s", got.Mode)
	}

	// 在线源：按 CurrentStream 报出的编码重算。
	tc2 := &Transcoder{
		srcURL: "https://example.com/v",
		plan:   Plan{Mode: OutputRemux, CopyVideo: true, CopyAudio: true}, // 按 avc1 算的
		directUps: []*Upstream{{
			videoCodec: "hev1.1.6.L150", // 实际换成了 HEVC
			audioCodec: "mp4a.40.2",
			height:     1080,
		}},
	}
	got := tc2.effectivePlanLocked()
	if got.CopyVideo {
		t.Error("实际流是 HEVC 时必须重算为转码，不能继续 -c:v copy")
	}
	if got.Mode != OutputTranscode {
		t.Errorf("HEVC 应重算为 transcode，实际 %s", got.Mode)
	}

	// 换回实际仍是 avc1：应保持免转码。
	tc3 := &Transcoder{
		srcURL:    "https://example.com/v",
		plan:      Plan{Mode: OutputRemux, CopyVideo: true, CopyAudio: true},
		directUps: []*Upstream{{videoCodec: "avc1.640032", audioCodec: "mp4a.40.2", height: 1080}},
	}
	if got := tc3.effectivePlanLocked(); !got.CopyVideo {
		t.Error("实际流是 H.264 时应保持免转码直通")
	}
}

// TestCurrentStreamTracksVariant 切换候选后 CurrentStream 应反映新编码。
func TestCurrentStreamTracksVariant(t *testing.T) {
	up := &Upstream{
		videoCodec: "avc1.640032", audioCodec: "mp4a.40.2", height: 1080,
		variants: []resolvedVariant{
			{URL: "https://n2/hev1", Codec: "hev1.1.6.L150", Height: 1080},
		},
	}
	v, a, h := up.CurrentStream()
	if v != "avc1.640032" || a != "mp4a.40.2" || h != 1080 {
		t.Errorf("未切换时应返回主链编码，实际 %s/%s/%d", v, a, h)
	}
	up.variantIdx = 1
	v, _, h = up.CurrentStream()
	if v != "hev1.1.6.L150" || h != 1080 {
		t.Errorf("切换后应返回候选编码，实际 %s/%d", v, h)
	}
}
