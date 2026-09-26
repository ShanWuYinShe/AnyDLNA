package media

import (
	"context"
	"io"
	"os"
	"testing"
	"time"
)

// TestCameraSessionProducesStream 真实采集验证（门控 ANYDLNA_CAMERA_ITEST=1，
// 需要本机摄像头与 macOS 摄像头权限）：avfoundation 采集 + 实时转码产出的
// MPEG-TS 流必须非空，且以 TS 同步字节 0x47 高密度出现——这是「采集 → 转码」
// 段的端到端证据；「流服务 → 电视」段与在线视频共用同一管线，已由 faketv E2E 覆盖。
func TestCameraSessionProducesStream(t *testing.T) {
	if os.Getenv("ANYDLNA_CAMERA_ITEST") == "" {
		t.Skip("需要真实摄像头：设 ANYDLNA_CAMERA_ITEST=1 启用")
	}

	// 先枚举设备：取第一个视频设备；无设备则跳过。
	devs, err := ListCameras(context.Background())
	if err != nil {
		t.Fatalf("枚举采集设备失败: %v", err)
	}
	var video string
	for _, d := range devs {
		if d.Kind == "video" {
			video = d.Index
			break
		}
	}
	if video == "" {
		t.Skip("未检测到摄像头设备")
	}
	t.Logf("使用摄像头设备序号 %s", video)

	tc := NewCameraTranscoder(video, "", Plan{Mode: OutputTranscode, Container: ContainerMPEGTS})
	pr, pw := io.Pipe()
	cancel, done, err := tc.StreamTo(pw)
	if err != nil {
		t.Fatalf("启动采集失败: %v", err)
	}
	defer cancel()
	defer pw.Close()

	// 采集 3 秒：流应持续产出。MPEG-TS 以 188 字节包、0x47 同步字节分帧，
	// 统计 0x47 密度做校验（阈值放宽一半，避免与块边界相关的偶发误判）。
	deadline := time.After(3 * time.Second)
	buf := make([]byte, 32*1024)
	total, syncCount := 0, 0
readLoop:
	for {
		select {
		case <-deadline:
			break readLoop
		case <-done:
			t.Fatal("采集进程提前退出")
		default:
		}
		n, rerr := pr.Read(buf)
		total += n
		for _, b := range buf[:n] {
			if b == 0x47 {
				syncCount++
			}
		}
		if rerr == io.EOF {
			break readLoop
		}
		if rerr != nil {
			t.Fatalf("读取流失败: %v", rerr)
		}
	}
	defer func() {
		t.Logf("采集产出 %d 字节，0x47 同步字节 %d 个（188 字节包理论上限 %d）",
			total, syncCount, total/188)
	}()
	if total < 32*1024 {
		t.Fatalf("3 秒采集产出过少: %d 字节", total)
	}
	if syncCount < total/188/2 {
		t.Fatalf("0x47 密度过低，不像 MPEG-TS 流: %d 字节中仅 %d 个同步字节", total, syncCount)
	}
}

// TestCameraReconnectStaysLive 电视端会周期性发起并存连接（先探测后播放、
// 播放中重连）。hub 架构下并存连接只是新增消费者，采集进程不受影响：
// 第一个连接保持活动时开第二个连接，两者都必须持续产出流。
// （旧实现每次连接启动一份采集进程，第二个会抢占独占的摄像头导致失败。）
func TestCameraReconnectStaysLive(t *testing.T) {
	if os.Getenv("ANYDLNA_CAMERA_ITEST") == "" {
		t.Skip("需要真实摄像头：设 ANYDLNA_CAMERA_ITEST=1 启用")
	}
	devs, err := ListCameras(context.Background())
	if err != nil {
		t.Fatalf("枚举采集设备失败: %v", err)
	}
	var video string
	for _, d := range devs {
		if d.Kind == "video" {
			video = d.Index
			break
		}
	}
	if video == "" {
		t.Skip("未检测到摄像头设备")
	}

	tc := NewCameraTranscoder(video, "", Plan{Mode: OutputTranscode, Container: ContainerMPEGTS})

	// 第一个连接：读 1 秒，保持活动（不 cancel）。
	pr1, pw1 := io.Pipe()
	cancel1, done1, err := tc.StreamTo(pw1)
	if err != nil {
		t.Fatalf("第一个连接启动失败: %v", err)
	}
	defer cancel1()
	defer pw1.Close()
	buf1 := make([]byte, 16*1024)
	deadline := time.After(1 * time.Second)
read1:
	for {
		select {
		case <-deadline:
			break read1
		case <-done1:
			t.Fatal("第一个连接提前退出")
		default:
		}
		if _, err := pr1.Read(buf1); err == io.EOF {
			break read1
		}
	}

	// 第二个连接：重连必须成功且持续产出（修复前会因摄像头被旧进程占用而失败）。
	pr2, pw2 := io.Pipe()
	cancel2, done2, err := tc.StreamTo(pw2)
	if err != nil {
		t.Fatalf("重连启动失败: %v", err)
	}
	defer cancel2()
	defer pw2.Close()

	deadline2 := time.After(2 * time.Second)
	buf2 := make([]byte, 32*1024)
	total, syncCount := 0, 0
read2:
	for {
		select {
		case <-deadline2:
			break read2
		case <-done2:
			t.Fatal("重连的采集进程提前退出（疑似摄像头仍被旧进程占用）")
		default:
		}
		n, rerr := pr2.Read(buf2)
		total += n
		for _, b := range buf2[:n] {
			if b == 0x47 {
				syncCount++
			}
		}
		if rerr == io.EOF {
			break read2
		}
		if rerr != nil {
			t.Fatalf("重连读取流失败: %v", rerr)
		}
	}
	if total < 32*1024 {
		t.Fatalf("重连后 2 秒产出过少: %d 字节", total)
	}
	if syncCount < total/188/2 {
		t.Fatalf("重连流 0x47 密度过低: %d 字节中仅 %d 个同步字节", total, syncCount)
	}
	cancel1()
}
