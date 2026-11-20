package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestReconnectReusesTransportWhenAudioFails 音频轨持续 403 时，电视端重连
// 必须复用已建好的视频传输，不能每次重连都重建上游。
//
// 背景（实测日志）：第二个视频的音频直链落在持续返回 403 的节点上，每轮
// 重连都重建整台上游、又因音频 403 失败一次，电视端连续 4 次都只拿到
// 0 字节（每次 15 秒）。根因是复用判断用 len(directUps) == len(urls)，
// 降级后两者永远不等。
func TestReconnectReusesTransportWhenAudioFails(t *testing.T) {
	payload := make([]byte, 4<<20)
	for i := range payload {
		payload[i] = byte(i)
	}
	var audioCalls int32
	video := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "v.mp4", time.Time{}, newBRS(payload))
	}))
	defer video.Close()
	audio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&audioCalls, 1)
		http.Error(w, "rejected", http.StatusForbidden)
	}))
	defer audio.Close()

	tc := NewURLTranscoder("https://example.com/v", false, Options{ProxyMode: ProxyModeNone},
		Plan{Mode: OutputRemux, Container: ContainerMPEGTS, CopyVideo: true}, "generic",
		[]string{video.URL, audio.URL})

	tc.mu.Lock()
	defer tc.mu.Unlock()

	// 首次建：音频 403 → 降级为只建视频。
	if _, _, _, err := tc.buildDirectLocked([]string{video.URL, audio.URL}, nil); err != nil {
		t.Fatalf("首次建传输失败: %v", err)
	}
	if !tc.audioUnavailable {
		t.Error("音频探测失败后应记住 audioUnavailable")
	}
	if got := len(tc.directUps); got != 1 {
		t.Fatalf("降级后应只有 1 路上游，实际 %d", got)
	}
	firstKey := tc.directKey
	before := atomic.LoadInt32(&audioCalls)

	// 模拟电视端多次重连：应复用，不重建、不再探测音频。
	for i := 0; i < 3; i++ {
		if _, _, _, err := tc.buildDirectLocked([]string{video.URL, audio.URL}, nil); err != nil {
			t.Fatalf("第%d次重连失败: %v", i+1, err)
		}
		if tc.directKey != firstKey {
			t.Fatalf("第%d次重连发生了重建（key 变化），应复用", i+1)
		}
	}
	if after := atomic.LoadInt32(&audioCalls); after != before {
		t.Errorf("已知音频不可用后仍在探测音频：多探测 %d 次", after-before)
	}
	_ = context.Background()
}
