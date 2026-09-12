package media

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// newTestFile 生成临时视频字节文件供直出测试。
func newTestFile(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sample.mp4")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStreamServerDirectWithRange(t *testing.T) {
	srv, err := NewStreamServer()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	content := []byte(strings.Repeat("0123456789", 100)) // 1000 字节
	path := newTestFile(t, content)
	id := srv.AddDirect(path, "video/mp4", "sample")
	url := srv.URL("127.0.0.1", id, true)
	url = url[:strings.LastIndex(url, "/f/")] + "/f/" + id

	// 全量拉取。
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(body) != len(content) {
		t.Fatalf("全量拉取异常: status=%d len=%d", resp.StatusCode, len(body))
	}

	// Range 拉取：模拟电视端拖动进度。
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=100-109")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(part) != string(content[100:110]) {
		t.Fatalf("Range 拉取异常: status=%d body=%q", resp.StatusCode, part)
	}
}

func TestStreamServerSessionIsolation(t *testing.T) {
	srv, err := NewStreamServer()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	path := newTestFile(t, []byte("video-bytes"))
	directID := srv.AddDirect(path, "video/mp4", "sample")

	// 直出会话不能经转码路由访问。
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(srv.Port()) + "/t/" + directID)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("转码路由误命中直出会话: status=%d", resp.StatusCode)
	}

	// 未注册的会话 404。
	resp, err = http.Get("http://127.0.0.1:" + strconv.Itoa(srv.Port()) + "/f/deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知会话应 404: status=%d", resp.StatusCode)
	}

	// Remove 后同样 404。
	srv.Remove(directID)
	resp, err = http.Get("http://127.0.0.1:" + strconv.Itoa(srv.Port()) + "/f/" + directID)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("会话移除后应 404: status=%d", resp.StatusCode)
	}
}

func TestStreamServerSetTranscodeOffset(t *testing.T) {
	srv, err := NewStreamServer()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	if err := srv.SetTranscodeOffset("no-such", 10); err == nil {
		t.Fatal("不存在的会话应报错")
	}

	path := newTestFile(t, []byte("x"))
	id := srv.AddTranscode(path, "sample", Plan{Mode: OutputTranscode})
	// RestartAt 只记录偏移并终止（未启动的）进程，不应报错。
	srv.SetTranscodeOffset(id, 42.5)
	// 此时无电视拉流，不应有 ffmpeg 残留进程。
	srv.Remove(id)
}
