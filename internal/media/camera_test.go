package media

import (
	"sync"
	"testing"
	"time"
)

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

// TestCameraHubFanout 常规分发两消费者各收全部块；慢消费者缓冲塞满后
// 整块丢弃且 dispatch 不阻塞（死锁会被 go test 超时杀死）；注销后不再收到。
func TestCameraHubFanout(t *testing.T) {
	h := newCameraHub(&CameraSource{VideoDevice: "0"}, Plan{})
	ch1, unsub1 := h.subscribe()
	defer unsub1()
	ch2, unsub2 := h.subscribe()
	defer unsub2()

	// 常规分发：两个消费者各收到全部块。
	const n = 3
	for i := 0; i < n; i++ {
		h.dispatch([]byte("block"))
	}
	for i := 0; i < n; i++ {
		if b := <-ch1; string(b) != "block" {
			t.Fatalf("消费 1 第 %d 块异常: %q", i, b)
		}
		if b := <-ch2; string(b) != "block" {
			t.Fatalf("消费 2 第 %d 块异常: %q", i, b)
		}
	}

	// 慢消费者语义：600 块远超 256 缓冲，未读的一方整块丢弃；dispatch
	// 必须不阻塞（若阻塞，本测试会被 go test 的 10 分钟超时杀死）。
	for i := 0; i < 600; i++ {
		h.dispatch([]byte("drop"))
	}

	// 注销后 channel 关闭：缓冲中的旧块仍可读出，排空后立即得到关闭信号，
	// 且不再有新块投递。
	unsub1()
	for {
		if _, ok := <-ch1; !ok {
			break
		}
	}
	h.dispatch([]byte("after-unsub"))
	if b, ok := <-ch1; ok {
		t.Errorf("已注销消费者不应再收到数据: %q", b)
	}

	// 排空 ch2 的丢弃积压后，应能继续收到新块（丢弃机制未卡死 channel）。
	for len(ch2) > 0 {
		<-ch2
	}
	h.dispatch([]byte("final"))
	if b := <-ch2; string(b) != "final" {
		t.Errorf("未注销消费者应能继续收到新块: %q", b)
	}
}

// TestCameraHubCloseSubsOnEnd 采集进程退出（closeSubs）后，
// 全部订阅者的 channel 应关闭，消费者循环随之结束。
func TestCameraHubCloseSubsOnEnd(t *testing.T) {
	h := newCameraHub(&CameraSource{VideoDevice: "0"}, Plan{})
	ch, unsub := h.subscribe()
	defer unsub()

	var wg sync.WaitGroup
	wg.Add(1)
	ended := make(chan struct{})
	go func() {
		defer wg.Done()
		for range ch {
		}
		close(ended)
	}()

	h.mu.Lock()
	for c := range h.subs {
		close(c)
	}
	h.subs = map[chan []byte]struct{}{}
	h.mu.Unlock()

	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("channel 关闭后消费者循环应结束")
	}
	wg.Wait()
}
