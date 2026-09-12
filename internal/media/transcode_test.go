package media

import (
	"path/filepath"
	"testing"
	"time"
)

// blockingWriter 永远阻塞在 Write 上，从而让转码进程一直存活，
// 便于测试进程组的生命周期（否则短样本会让进程瞬间结束）。
type blockingWriter struct {
	release chan struct{}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	<-b.release
	return len(p), nil
}

// makeSampleVideo 用 ffmpeg 生成一个测试用短视频；缺少 ffmpeg 时跳过测试。
func makeSampleVideo(t *testing.T) string {
	t.Helper()
	if !HasFFmpeg() {
		t.Skip("未安装 ffmpeg，跳过多路拉流测试")
	}
	path := filepath.Join(t.TempDir(), "sample.mp4")
	cmd := toolCmd("ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=640x480:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440",
		"-t", "8", "-pix_fmt", "yuv420p",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac",
		path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("生成测试视频失败（跳过）: %v %s", err, out)
	}
	return path
}

// TestConcurrentStreamsAreIndependent 回归测试：同一会话被并发拉流时，
// 每路进程必须独立可取消，且 Stop 必须能覆盖全部。
//
// 背景：电视端常见做法是先发探测连接、再发真正的播放连接，因此同一会话上
// 会同时存在多路 StreamTo。早期实现把 ffmpeg/yt-dlp/done 放在单份字段里，
// 后一次拉流直接覆盖前一次：
//   - 先前那路既无法取消（cancel 里判断 t.cmd != cmd 后直接返回），
//     也不在 Stop 的管辖范围内，进程永久泄漏并持续占用带宽；
//   - 任一路断开都会把 t.cmd 置空，使其余路彻底失控。
//
// 该测试固定住「每路独立、Stop 全覆盖」这一约束。
func TestConcurrentStreamsAreIndependent(t *testing.T) {
	path := makeSampleVideo(t)

	tc := NewTranscoder(path, Plan{
		Mode: OutputRemux, Container: ContainerMPEGTS,
		CopyVideo: true, CopyAudio: true,
	})
	defer tc.Stop()

	release := make(chan struct{})
	defer close(release)

	// 两路并发拉流。
	cancel1, done1, err := tc.StreamTo(&blockingWriter{release: release})
	if err != nil {
		t.Fatalf("第一路启动失败: %v", err)
	}
	cancel2, done2, err := tc.StreamTo(&blockingWriter{release: release})
	if err != nil {
		t.Fatalf("第二路启动失败: %v", err)
	}
	// 第二路由 Stop 统一回收；这里保留 cancel2 以确认它可重复调用而无副作用。
	defer cancel2()

	if n := tc.liveStreams(); n != 2 {
		t.Fatalf("应有 2 路在运行，实际 %d", n)
	}

	// 取消第一路：只能终止它自己，第二路必须继续存活。
	cancel1()
	if n := tc.liveStreams(); n != 1 {
		t.Errorf("取消第一路后应剩 1 路，实际 %d（先前那路未被回收即为泄漏）", n)
	}
	select {
	case <-done1:
	default:
		t.Error("被取消的那路应已结束")
	}
	select {
	case <-done2:
		t.Error("取消第一路不应影响第二路")
	default:
	}

	// Stop 必须覆盖剩余的所有路。
	tc.Stop()
	if n := tc.liveStreams(); n != 0 {
		t.Errorf("Stop 后不应有残留拉流，实际 %d", n)
	}
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Error("Stop 应终止第二路")
	}
}

// TestStopWithoutStreamsIsNoop 确认没有拉流时 Stop/RestartAt 不会 panic。
func TestStopWithoutStreamsIsNoop(t *testing.T) {
	tc := NewTranscoder("/nonexistent", Plan{})
	tc.Stop()
	tc.RestartAt(12.5)
	if n := tc.liveStreams(); n != 0 {
		t.Errorf("不应有拉流，实际 %d", n)
	}
	if tc.offset != 12500 {
		t.Errorf("RestartAt 应记录起始位置，实际 %d", tc.offset)
	}
}

// liveStreams 返回当前登记的拉流数量（仅测试使用）。
func (t *Transcoder) liveStreams() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.streams)
}
