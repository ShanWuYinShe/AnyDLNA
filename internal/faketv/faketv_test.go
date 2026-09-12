package faketv

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"AnyDLNA/internal/dlna"
)

// TestDiscoverFindsFakeTV 假电视应能被局域网搜索发现（含 AVTransport 校验）。
func TestDiscoverFindsFakeTV(t *testing.T) {
	tv, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer tv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	devs, err := dlna.DiscoverRenderers(ctx, 6*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devs {
		if d.FriendlyName == "FakeTV" && d.HasAVTransport() {
			t.Logf("发现假电视: %s", d.Location)
			return
		}
	}
	t.Fatalf("未发现假电视（共 %d 台）", len(devs))
}

// TestControlRoundTrip 下发→播放→暂停→跳转记录→音量→位置轮询→停止全走通。
func TestControlRoundTrip(t *testing.T) {
	tv, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer tv.Close()

	// 静态字节源，模拟流服务。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 188*10)
	for i := range payload {
		if i%188 == 0 {
			payload[i] = 0x47
		}
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	streamURL := fmt.Sprintf("http://127.0.0.1:%d/x.ts", ln.Addr().(*net.TCPAddr).Port)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dev, err := dlna.Describe(ctx, http.DefaultClient, tv.DescriptionURL())
	if err != nil {
		t.Fatal(err)
	}
	r := dlna.NewRenderer(dev)
	if err := r.SetAVTransportURI(ctx, streamURL, "meta"); err != nil {
		t.Fatal(err)
	}
	if got := tv.URI(); got != streamURL {
		t.Fatalf("假电视收到的 URI 不对: %q", got)
	}
	if err := r.Play(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if n, p, e := tv.Stats(); n == 0 || p == 0 || e != 0 {
		t.Fatalf("拉流统计不对: bytes=%d packets=%d errors=%d", n, p, e)
	}
	if state, err := r.TransportState(ctx); err != nil || state != "PLAYING" {
		t.Fatalf("状态应为 PLAYING: %q %v", state, err)
	}
	if err := r.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	if got := tv.State(); got != "PAUSED_PLAYBACK" {
		t.Fatalf("暂停后状态不对: %q", got)
	}
	if err := r.Seek(ctx, dlna.SeekUnitABSTime, "0:01:30"); err != nil {
		t.Fatal(err)
	}
	if seeks := tv.Seeks(); len(seeks) != 1 || seeks[0] != "0:01:30" {
		t.Fatalf("跳转记录不对: %v", seeks)
	}
	if _, err := r.GetVolume(ctx); err != nil {
		t.Fatalf("取音量失败: %v", err)
	}
	if err := r.SetVolume(ctx, 42); err != nil {
		t.Fatalf("设音量失败: %v", err)
	}
	if _, _, err := r.PositionInfo(ctx); err != nil {
		t.Fatalf("位置轮询失败: %v", err)
	}
	if err := r.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := tv.State(); got != "STOPPED" {
		t.Fatalf("停止后状态不对: %q", got)
	}
}
