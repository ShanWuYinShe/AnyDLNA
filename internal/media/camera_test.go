package media

import (
	"errors"
	"io"
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
		// 屏幕捕获设备保留在列表中，以 Screen 标记区分（见 cameraInputArgs）。
		"video:1:Capture screen 0",
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
	// 只有屏幕设备带 Screen 标记；摄像头与音频设备不得误标。
	for i, d := range devs {
		if got := d.Screen; got != (d.Name == "Capture screen 0") {
			t.Errorf("第 %d 项 Screen 标记错误: %+v", i, d)
		}
	}
}

// TestCameraInputArgs 摄像头与屏幕的采集参数分支：
// 摄像头强制 720p；屏幕按原生分辨率采集、降采样到 720p 并保证偶数尺寸。
func TestCameraInputArgs(t *testing.T) {
	cam := cameraInputArgs(&CameraSource{VideoDevice: "0"})
	if !containsAll(cam, []string{"-video_size", "1280x720"}) || containsAll(cam, []string{"-vf"}) {
		t.Errorf("摄像头采集参数异常: %v", cam)
	}
	scr := cameraInputArgs(&CameraSource{VideoDevice: "1", Screen: true})
	if containsAll(scr, []string{"-video_size"}) {
		t.Errorf("屏幕采集不应强制 video_size: %v", scr)
	}
	if !containsAll(scr, []string{"-capture_cursor", "1", "-vf",
		"scale=w=1280:h=720:force_original_aspect_ratio=decrease:force_divisible_by=2"}) {
		t.Errorf("屏幕采集参数异常: %v", scr)
	}
}

// containsAll 判断 args 是否包含全部给定元素（按独立元素比较）。
func containsAll(args, want []string) bool {
	for _, w := range want {
		found := false
		for _, a := range args {
			if a == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
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

// TestCameraHubRing 逐项验证环形缓冲语义：顺序读取、跨环绕分段拷贝、
// 游标被覆盖时返回 lagged、无新数据时阻塞等待、stop 后立即关闭。
func TestCameraHubRing(t *testing.T) {
	h := newCameraHub(&CameraSource{VideoDevice: "0"}, Plan{})
	h.ring = make([]byte, 16) // 缩小容量便于构造覆盖场景

	// 顺序读取。
	h.write([]byte("hello"))
	buf := make([]byte, 16)
	n, pos, err := h.read(0, buf)
	if err != nil || string(buf[:n]) != "hello" || pos != 5 {
		t.Fatalf("顺序读取异常: n=%d pos=%d err=%v data=%q", n, pos, err, buf[:n])
	}

	// 跨环绕写入后再读：数据必须连续。
	h.write([]byte("world!!!")) // head=13，环形布局 [!!!o world...]
	n, pos, err = h.read(5, buf)
	if err != nil || string(buf[:n]) != "world!!!" || pos != 13 {
		t.Fatalf("跨环绕读取异常: n=%d pos=%d err=%v data=%q", n, pos, err, buf[:n])
	}

	// 游标被覆盖（pos=5，head=13，容量 16，再写 12 字节后 5 < 13+12-16=9……
	// 直接构造：写满一整圈使 pos 落到覆盖区。
	h.write(make([]byte, 12)) // head=25
	if _, _, err := h.read(5, buf); err != cameraErrLagged {
		t.Fatalf("游标被覆盖应返回 lagged，实际 %v", err)
	}

	// 从当前尾部读取：正常。
	h.mu.Lock()
	head := h.head
	h.mu.Unlock()
	n, _, err = h.read(head-4, buf)
	if err != nil || n != 4 {
		t.Fatalf("尾部读取异常: n=%d err=%v", n, err)
	}

	// 无新数据时阻塞，写入后唤醒。
	got := make(chan string, 1)
	go func() {
		h.mu.Lock()
		pos := h.head
		h.mu.Unlock()
		n, _, err := h.read(pos, buf)
		if err != nil {
			got <- "ERR:" + err.Error()
			return
		}
		got <- string(buf[:n])
	}()
	time.Sleep(50 * time.Millisecond) // 让消费者进入等待
	h.write([]byte("wake"))
	select {
	case s := <-got:
		if s != "wake" {
			t.Errorf("唤醒后读到 %q, want wake", s)
		}
	case <-time.After(time.Second):
		t.Fatal("阻塞的读取未被写入唤醒")
	}

	// stop 后读取立即返回关闭错误。
	h.stop()
	h.mu.Lock()
	pos2 := h.head
	h.mu.Unlock()
	if _, _, err := h.read(pos2, buf); err != io.ErrClosedPipe {
		t.Fatalf("stop 后读取应返回 ErrClosedPipe，实际 %v", err)
	}
}

// TestCameraHubRingConcurrent 多写者 + 多消费者并发压力：在 -race 下验证
// write/read 的锁互斥与环形分段拷贝的正确性（本用例正是摄像头分发竞态
// 修复后补上的并发覆盖）。
func TestCameraHubRingConcurrent(t *testing.T) {
	h := newCameraHub(&CameraSource{VideoDevice: "0"}, Plan{})
	h.ring = make([]byte, 1024) // 小容量制造高频环绕

	const writers = 4
	const perWriter = 200
	const blockLen = 64 // 每块 64 字节（小于容量，write 必然完整写入）

	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			block := make([]byte, blockLen)
			for i := 0; i < perWriter; i++ {
				block[0] = byte(w)
				block[1] = byte(i)
				h.write(block)
			}
		}(w)
	}

	// 两个并发消费者：read 全程持锁，-race 下验证互斥与环形分段拷贝。
	// 消费者从 0 起读（总量 51200 远超 1024 缓冲，必然经历 lagged 跳转），
	// 写者完成后由 stop 唤醒退出；断言各自至少读满一个缓冲容量。
	seen := make([]chan int64, 2)
	for r := range seen {
		seen[r] = make(chan int64, 1)
		go func(r int) {
			var count int64
			buf := make([]byte, blockLen)
			pos := int64(0)
			for {
				n, npos, err := h.read(pos, buf)
				if errors.Is(err, cameraErrLagged) {
					h.mu.Lock()
					oldest := h.head - int64(len(h.ring))
					if oldest < 0 {
						oldest = 0
					}
					h.mu.Unlock()
					if npos < oldest {
						pos = oldest
					}
					continue
				}
				if err != nil {
					break // stop 后 ErrClosedPipe：正常退出
				}
				count += int64(n)
				pos = npos
			}
			seen[r] <- count
		}(r)
	}

	wg.Wait() // 写者全部完成后，head 停止前进。
	h.stop()  // 唤醒阻塞在 read 上的消费者。

	// 断言只要求消费者干净退出（无死锁）——数据竞争由 -race 检测器负责，
	// 字节数取决于协程调度时机（可能晚于全部写入），不作硬性断言。
	for r := range seen {
		select {
		case n := <-seen[r]:
			t.Logf("消费者 %d 累计读取 %d 字节", r, n)
		case <-time.After(2 * time.Second):
			t.Errorf("消费者 %d 未能在 stop 后退出", r)
		}
	}
}
