package media

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestURLStreamIntegration 走通 CastURL 的完整数据路径：
// 本地 HTTP 提供 mp4 → yt-dlp 解析/拉流 → ffmpeg 转码 → MPEG-TS。
// 需要环境变量 ANYDLNA_STREAM_ITEST=1 且安装 yt-dlp/ffmpeg 才会运行。
func TestURLStreamIntegration(t *testing.T) {
	if os.Getenv("ANYDLNA_STREAM_ITEST") == "" {
		t.Skip("未设置 ANYDLNA_STREAM_ITEST，跳过在线流集成测试")
	}
	sampleDir := os.Getenv("ANYDLNA_STREAM_SAMPLE_DIR")
	if sampleDir == "" {
		t.Fatal("缺少 ANYDLNA_STREAM_SAMPLE_DIR（存放测试 mp4 的目录）")
	}
	if !HasYtDlp() || !HasFFmpeg() {
		t.Fatal("缺少 yt-dlp 或 ffmpeg")
	}

	// 用本地 HTTP 目录模拟在线站点（yt-dlp generic 解析直链）。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fileSrv := &http.Server{Handler: http.FileServer(http.Dir(sampleDir))}
	go func() { _ = fileSrv.Serve(ln) }()
	defer fileSrv.Close()
	base := fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)

	// 1) Resolve：元数据解析。
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Resolve(ctx, base+"/sample.mp4", Options{ProxyMode: ProxyModeNone})
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	t.Logf("解析结果: %+v", res)
	if res.IsLive {
		t.Fatalf("本地样本不应被判定为直播: %+v", res)
	}
	// 注意：generic 直链可能没有时长信息（yt-dlp 不探测媒体内容），DurationSec==0 合法。

	// 2) 流会话：电视端视角拉取 MPEG-TS。
	ss, err := NewStreamServer()
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	id, err := ss.AddTranscodeURL(ctx, base+"/sample.mp4", res.Title, res.IsLive,
		Options{ProxyMode: ProxyModeNone}, PlanForOnline(res.VideoCodec, res.AudioCodec, DeviceCapabilities{}), res.Extractor)
	if err != nil {
		t.Fatalf("注册流会话（预取直链）失败: %v", err)
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/t/%s", ss.Port(), id))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("拉流状态码 %d", resp.StatusCode)
	}

	// MPEG-TS 包固定 188 字节，同步字节 0x47；读 20 个包校验。
	const packets = 20
	buf := make([]byte, 188*packets)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("读取 MPEG-TS 流失败: %v", err)
	}
	for i := 0; i < packets; i++ {
		if buf[i*188] != 0x47 {
			t.Fatalf("第 %d 个 TS 包同步字节错误: %#x", i, buf[i*188])
		}
	}

	// 3) Seek 重启：设置偏移后重新拉流仍应产出有效 TS。
	if err := ss.SetTranscodeOffset(id, 1); err != nil {
		t.Fatal(err)
	}
	resp2, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/t/%s", ss.Port(), id))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	buf2 := make([]byte, 188*5)
	if _, err := io.ReadFull(resp2.Body, buf2); err != nil {
		t.Fatalf("Seek 后拉流失败: %v", err)
	}
}
