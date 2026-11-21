package media

import (
	"context"
	"testing"
)

func TestGetCandidateEncodersForOS(t *testing.T) {
	darwinCands := GetCandidateEncodersForOS("darwin")
	if len(darwinCands) != 1 || darwinCands[0].Name != "h264_videotoolbox" {
		t.Fatalf("darwin 候选配置不符合预期: %+v", darwinCands)
	}

	winCands := GetCandidateEncodersForOS("windows")
	if len(winCands) < 4 {
		t.Fatalf("windows 候选配置数量过少: %d", len(winCands))
	}
	expectedWin := []string{"h264_nvenc", "h264_qsv", "h264_amf", "h264_mf"}
	for i, name := range expectedWin {
		if winCands[i].Name != name {
			t.Errorf("windows 候选顺序不一致: got %s, want %s", winCands[i].Name, name)
		}
	}

	linuxCands := GetCandidateEncodersForOS("linux")
	if len(linuxCands) < 3 {
		t.Fatalf("linux 候选配置数量过少: %d", len(linuxCands))
	}
	expectedLinux := []string{"h264_nvenc", "h264_qsv", "h264_vaapi"}
	for i, name := range expectedLinux {
		if linuxCands[i].Name != name {
			t.Errorf("linux 候选顺序不一致: got %s, want %s", linuxCands[i].Name, name)
		}
	}

	otherCands := GetCandidateEncodersForOS("freebsd")
	if len(otherCands) != 0 {
		t.Errorf("未知平台应返回 nil: %+v", otherCands)
	}
}

func TestSelectVideoEncoderWithMock(t *testing.T) {
	defer ResetHardwareEncoderCache()
	defer SetCustomEncoderProber(nil)

	// 1. Windows: nvenc 失败，qsv 成功
	ResetHardwareEncoderCache()
	SetCustomEncoderProber(func(ctx context.Context, ffmpegPath string, cfg *VideoEncoderConfig) bool {
		return cfg.Name == "h264_qsv"
	})
	enc := SelectVideoEncoder("windows")
	if enc.Name != "h264_qsv" {
		t.Errorf("Windows 预期选择 h264_qsv, 实际得到: %s", enc.Name)
	}

	// 2. 全部失败时降级到 libx264
	ResetHardwareEncoderCache()
	SetCustomEncoderProber(func(ctx context.Context, ffmpegPath string, cfg *VideoEncoderConfig) bool {
		return false
	})
	enc = SelectVideoEncoder("windows")
	if enc.Name != "libx264" {
		t.Errorf("所有硬件编码器失败时预期回退 libx264, 实际得到: %s", enc.Name)
	}

	// 3. Linux: nvenc 成功
	ResetHardwareEncoderCache()
	SetCustomEncoderProber(func(ctx context.Context, ffmpegPath string, cfg *VideoEncoderConfig) bool {
		return cfg.Name == "h264_nvenc"
	})
	enc = SelectVideoEncoder("linux")
	if enc.Name != "h264_nvenc" {
		t.Errorf("Linux 预期选择 h264_nvenc, 实际得到: %s", enc.Name)
	}

	// 4. Darwin: videotoolbox 成功
	ResetHardwareEncoderCache()
	SetCustomEncoderProber(func(ctx context.Context, ffmpegPath string, cfg *VideoEncoderConfig) bool {
		return cfg.Name == "h264_videotoolbox"
	})
	enc = SelectVideoEncoder("darwin")
	if enc.Name != "h264_videotoolbox" {
		t.Errorf("Darwin 预期选择 h264_videotoolbox, 实际得到: %s", enc.Name)
	}
}

func TestHwaccelDecoderArgs(t *testing.T) {
	args := HwaccelDecoderArgs()
	if len(args) != 2 || args[0] != "-hwaccel" || args[1] != "auto" {
		t.Fatalf("HwaccelDecoderArgs 返回异常: %v", args)
	}
}

func TestGetVideoEncoderArgs(t *testing.T) {
	defer ResetHardwareEncoderCache()
	defer SetCustomEncoderProber(nil)

	ResetHardwareEncoderCache()
	SetCustomEncoderProber(func(ctx context.Context, ffmpegPath string, cfg *VideoEncoderConfig) bool {
		return false // 强制回退软编测试参数
	})
	args := GetVideoEncoderArgs()
	if !containsAll(args, []string{"-c:v", "libx264", "-g", "60", "-pix_fmt", "yuv420p"}) {
		t.Errorf("GetVideoEncoderArgs 参数缺失核心字段: %v", args)
	}
}
