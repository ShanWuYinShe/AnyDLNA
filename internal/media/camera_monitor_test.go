package media

import (
	"context"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

// TestCameraStreamMonitor 真实采集 60 秒监控（门控 ANYDLNA_CAMERA_MONITOR=1）。
// 目标：用数据回答「服务端到底有没有周期性停滞」——
//   - CBR（-muxrate 3M）下每秒码率应恒定在约 375KB/s；
//   - 块间最大间隔（数据停滞）应远小于 1 秒；
//   - 若服务端存在 ~40 秒周期的停滞，每秒码率表会清晰呈现空洞。
func TestCameraStreamMonitor(t *testing.T) {
	if os.Getenv("ANYDLNA_CAMERA_MONITOR") == "" {
		t.Skip("监控用例耗时 60 秒：设 ANYDLNA_CAMERA_MONITOR=1 启用")
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
	pr, pw := io.Pipe()
	cancel, done, err := tc.StreamTo(pw)
	if err != nil {
		t.Fatalf("启动采集失败: %v", err)
	}
	defer cancel()
	defer pw.Close()

	const runFor = 60 * time.Second
	st := struct {
		mu     sync.Mutex
		start  time.Time
		last   time.Time
		total  int64
		maxGap time.Duration
		perSec map[int64]int64
		gaps   []time.Duration
	}{perSec: map[int64]int64{}}
	st.start = time.Now()
	st.last = st.start

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 64*1024)
		for {
			n, rerr := pr.Read(buf)
			now := time.Now()
			st.mu.Lock()
			if n > 0 {
				st.total += int64(n)
				gap := now.Sub(st.last)
				if gap > st.maxGap {
					st.maxGap = gap
				}
				if gap > 500*time.Millisecond {
					st.gaps = append(st.gaps, gap)
				}
				st.last = now
				sec := int64(now.Sub(st.start).Seconds())
				st.perSec[sec] += int64(n)
			}
			st.mu.Unlock()
			if rerr != nil {
				return
			}
		}
	}()

	time.Sleep(runFor)
	// 先关闭写端让读协程从 Read 返回，再等统计协程与采集清理。
	pw.Close()
	wg.Wait()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Log("警告：采集进程 5 秒内未退出")
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	elapsed := time.Since(st.start).Seconds()
	avg := float64(st.total) / elapsed
	t.Logf("总时长 %.1fs 总量 %d 字节 平均码率 %.0f KB/s", elapsed, st.total, avg/1024)
	t.Logf("最大块间停滞 %v；超过 500ms 的停滞 %d 次", st.maxGap, len(st.gaps))
	for _, g := range st.gaps {
		t.Logf("  停滞: %v", g)
	}
	line := ""
	for sec := int64(0); sec < int64(runFor/time.Second); sec++ {
		line += itoaKb(st.perSec[sec]) + " "
	}
	t.Logf("每秒 KB: %s", line)

	// 校验：VBR 下平均码率不应异常（静止场景通常几十 KB/s 起）；
	// 服务端不应有显著的数据停滞空洞（停滞即电视端卡顿的直接证据）。
	if avg < 8*1024 {
		t.Errorf("平均码率异常偏低: %.0f KB/s", avg/1024)
	}
	if st.maxGap > 2*time.Second {
		t.Errorf("服务端存在明显数据停滞: %v", st.maxGap)
	}
}

func itoaKb(n int64) string {
	kb := n / 1024
	if kb < 10 {
		return "  " + intToStr(int(kb))
	}
	if kb < 100 {
		return " " + intToStr(int(kb))
	}
	return intToStr(int(kb))
}

func intToStr(v int) string {
	if v == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
