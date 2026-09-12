package media

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"AnyDLNA/internal/dlna"
	"AnyDLNA/internal/faketv"
)

// TestFakeTVE2E 用假电视走完整投屏链路：发现→下发→播放→校验→跳转→再校验。
// 需要 yt-dlp/ffmpeg 与 ANYDLNA_STREAM_SAMPLE_DIR（带音视频的 mp4）。
// 在线分支走 generic 解析，行为与真实站点一致（预取直链→ffmpeg 直连）。
func TestFakeTVE2E(t *testing.T) {
	if os.Getenv("ANYDLNA_STREAM_ITEST") == "" {
		t.Skip("未设置 ANYDLNA_STREAM_ITEST，跳过假电视端到端测试")
	}
	sampleDir := os.Getenv("ANYDLNA_STREAM_SAMPLE_DIR")
	if sampleDir == "" {
		t.Fatal("缺少 ANYDLNA_STREAM_SAMPLE_DIR（存放测试 mp4 的目录）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// 1) 假电视上线，应用侧应能发现。
	tv, err := faketv.New()
	if err != nil {
		t.Fatal(err)
	}
	defer tv.Close()
	dev, err := dlna.Describe(ctx, http.DefaultClient, tv.DescriptionURL())
	if err != nil {
		t.Fatalf("读取假电视描述失败: %v", err)
	}
	if dev.FriendlyName != "FakeTV" || !dev.HasAVTransport() {
		t.Fatalf("假电视描述不符合渲染器契约: %+v", dev)
	}

	// 2) 建流会话（与 CastURL 同样的调用顺序，样本经本地 HTTP 供 yt-dlp 解析）。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fileSrv := &http.Server{Handler: http.FileServer(http.Dir(sampleDir))}
	go func() { _ = fileSrv.Serve(ln) }()
	defer fileSrv.Close()
	srcURL := fmt.Sprintf("http://127.0.0.1:%d/sample.mp4", ln.Addr().(*net.TCPAddr).Port)

	ss, err := NewStreamServer()
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	res, urls, err := ResolveDirect(ctx, srcURL, Options{ProxyMode: ProxyModeNone})
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	id, err := ss.AddTranscodeURL(ctx, srcURL, res.Title, res.IsLive,
		Options{ProxyMode: ProxyModeNone}, PlanForOnline(res.VideoCodec, res.AudioCodec, DeviceCapabilities{}), res.Extractor, urls)
	if err != nil {
		t.Fatalf("注册流会话失败: %v", err)
	}
	playURL := ss.URL("127.0.0.1", id, false)

	// 3) 下发并播放，假电视拉流。
	renderer := dlna.NewRenderer(dev)
	if err := renderer.SetAVTransportURI(ctx, playURL, dlna.BuildDIDLMetadata("sample", playURL, "video/mp2t")); err != nil {
		t.Fatalf("下发播放地址失败: %v", err)
	}
	if err := renderer.Play(ctx); err != nil {
		t.Fatalf("启动播放失败: %v", err)
	}
	time.Sleep(6 * time.Second)
	n, packets, errs := tv.Stats()
	if n == 0 || packets == 0 {
		t.Fatalf("假电视未收到流: bytes=%d packets=%d", n, packets)
	}
	if errs != 0 {
		t.Fatalf("TS 同步错误 %d（bytes=%d packets=%d）", errs, n, packets)
	}
	if state, err := renderer.TransportState(ctx); err != nil || state != "PLAYING" {
		t.Fatalf("状态应为 PLAYING: %q %v", state, err)
	}
	t.Logf("播放中: %d 字节 %d 包零错误", n, packets)

	// 4) 跳转：与 SeekTo 同样的调用顺序（偏移→重下发→重播→重拉流）。
	if err := ss.SetTranscodeOffset(id, 5); err != nil {
		t.Fatal(err)
	}
	if err := renderer.SetAVTransportURI(ctx, playURL+"?t=5", dlna.BuildDIDLMetadata("sample", playURL, "video/mp2t")); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Play(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second)
	n2, packets2, errs2 := tv.Stats()
	if n2 <= n || packets2 <= packets {
		t.Fatalf("跳转后无新数据: 之前 %d(%d包) 之后 %d(%d包)", n, packets, n2, packets2)
	}
	if errs2 != 0 {
		t.Fatalf("跳转后 TS 同步错误 %d", errs2)
	}
	t.Logf("跳转后: 累计 %d 字节 %d 包零错误", n2, packets2)

	// 5) 停止。
	if err := renderer.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if got := tv.State(); got != "STOPPED" {
		t.Fatalf("停止后状态应为 STOPPED: %q", got)
	}
}
