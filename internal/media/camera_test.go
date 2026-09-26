package media

import "testing"

// sampleAVFoundationOutput 是 ffmpeg -list_devices 的典型 stderr 输出
// （含视频段、音频段、屏幕捕获设备与同名双栖设备）。
const sampleAVFoundationOutput = "[AVFoundation indev @ 0x600000cfc280] AVFoundation video devices (some may be both video and audio devices):\n" +
	"[AVFoundation indev @ 0x600000cfc280] [0] FaceTime HD Camera\n" +
	"[AVFoundation indev @ 0x600000cfc280] [1] Capture screen 0\n" +
	"[AVFoundation indev @ 0x600000cfc280] [2] iPhone 摄像头\n" +
	"[AVFoundation indev @ 0x600000cfc280] AVFoundation audio devices:\n" +
	"[AVFoundation indev @ 0x600000cfc280] [0] MacBook Pro Microphone\n" +
	"[AVFoundation indev @ 0x600000cfc280] [2] iPhone 麦克风\n"

func TestParseAVFoundationDevices(t *testing.T) {
	devs := parseAVFoundationDevices(sampleAVFoundationOutput)

	var names []string
	for _, d := range devs {
		names = append(names, d.Kind+":"+d.Index+":"+d.Name)
	}
	want := []string{
		"video:0:FaceTime HD Camera",
		// 屏幕捕获设备不属于摄像头，应被过滤。
		"video:2:iPhone 摄像头",
		"audio:0:MacBook Pro Microphone",
		"audio:2:iPhone 麦克风",
	}
	if len(names) != len(want) {
		t.Fatalf("设备数量不一致: got %v want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("第 %d 项不一致: got %q want %q", i, names[i], want[i])
		}
	}
}

// TestParseAVFoundationDevicesEmpty 无 avfoundation 输出（如 ffmpeg 缺失
// 报错文本）时应返回空列表而不是误报设备。
func TestParseAVFoundationDevicesEmpty(t *testing.T) {
	if devs := parseAVFoundationDevices("ffmpeg version 6.0\nUnknown input format: 'avfoundation'\n"); len(devs) != 0 {
		t.Errorf("无关输出不应解析出设备: %+v", devs)
	}
	if devs := parseAVFoundationDevices(""); len(devs) != 0 {
		t.Errorf("空输出不应解析出设备: %+v", devs)
	}
}

// TestPlanForCamera 摄像头始终完整转码；容器按设备能力选择。
func TestPlanForCamera(t *testing.T) {
	// 未查询到能力：保守用 MPEG-TS。
	if got := PlanForCamera(DeviceCapabilities{}); got.Mode != OutputTranscode || got.Container != ContainerMPEGTS {
		t.Errorf("默认应为 TS 完整转码: %+v", got)
	}
	// 设备声明只支持 MP4：换碎片化 MP4。
	onlyMP4 := capsWith("video/mp4")
	if got := PlanForCamera(onlyMP4); got.Container != ContainerFMP4 {
		t.Errorf("仅支持 MP4 的设备应用碎片化 MP4: %+v", got)
	}
}
