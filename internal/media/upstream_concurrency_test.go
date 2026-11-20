package media

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestUpstreamConcurrentReadThroughput 并发拉取时吞吐不应被锁串行化。
//
// 背景：缓存文件的 WriteAt/ReadAt 曾在持有 up.mu 的情况下执行，导致 4 路
// 并发 Range 拉取退化成单路（实测 254MB 视频的吞吐从 12 MB/s 掉到 0.5 MB/s，
// 电视端表现为持续低速率缓冲）。若磁盘 IO 又回到锁内，这里会明显变慢。
func TestUpstreamConcurrentReadThroughput(t *testing.T) {
	const size = 8 << 20
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "v.mp4", time.Time{}, bytes.NewReader(payload))
	}))
	defer srv.Close()

	up, err := NewUpstream(srv.URL, Options{ProxyMode: ProxyModeNone})
	if err != nil {
		t.Fatalf("建上游失败: %v", err)
	}
	defer up.Close()

	serve := httptest.NewServer(up)
	defer serve.Close()

	const workers = 4
	const span = size / workers
	var wg sync.WaitGroup
	errs := make([]error, workers)
	start := time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, serve.URL, nil)
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", i*span, (i+1)*span-1))
			resp, derr := http.DefaultClient.Do(req)
			if derr != nil {
				errs[i] = derr
				return
			}
			defer resp.Body.Close()
			n, cerr := io.Copy(io.Discard, resp.Body)
			if cerr != nil {
				errs[i] = cerr
			} else if n != span {
				errs[i] = io.ErrUnexpectedEOF
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	for i, e := range errs {
		if e != nil {
			t.Fatalf("worker %d 失败: %v", i, e)
		}
	}
	t.Logf("4 路并发读取 %d 字节，用时 %s", size, elapsed.Round(time.Millisecond))
	if elapsed > 5*time.Second {
		t.Errorf("并发读取 %s 过慢，疑似锁竞争把磁盘 IO 串行化", elapsed.Round(time.Millisecond))
	}
}
