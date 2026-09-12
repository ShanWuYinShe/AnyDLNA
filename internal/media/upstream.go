package media

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// Go 原生上游传输：yt-dlp 只负责解析出直链之后，下载、并发、定位、代理
// 全部由本文件接管，ffmpeg 只做合并/remux，不再直连 CDN。
//
// 原理：每个直链对应一个 Upstream——代理感知的 http.Client 并发 Range 拉取
// 上游内容，存入本地稀疏缓存（临时文件 + 位图）；同时 Upstream 在本机回环
// 起一个只供 ffmpeg 访问的 HTTP 服务，完整支持 Range。ffmpeg 用
// `-ss 秒 -i http://127.0.0.1:端口/v` 定位时，自己发 Range 请求，
// Upstream 按需优先拉取跳转位置（缓存命中则瞬间返回），电视重拉即跳转。
//
// 为什么不用 ffmpeg 直连直链：ffmpeg 只认 http(s) 代理、不支持 socks5，
// 且它的重试/超时不可控；Go 的 http.Client 两种代理都支持，超时、重试、
// 并发数全部可调，跳转与代理问题第一次变得可观测、可控制。

const (
	// upstreamChunkSize 是稀疏缓存的分片大小。1MB 是兼顾 Range 粒度与
	// 磁盘/内存开销的折中：太小则请求过多，太大则跳转首包等待过久。
	upstreamChunkSize = 1 << 20
	// upstreamWorkers 是每个直链的并发拉取连接数。与之前 yt-dlp 原生下载器
	// 的 8 并发同量级，但 Go 端按需调度：顺序预取 + 跳转优先。
	upstreamWorkers = 4
	// upstreamChunkRetries 是单个分片失败后的重试次数，耗尽则整路标记失败
	// （ServeHTTP 返回 502，ffmpeg 报错而非静默卡死）。
	upstreamChunkRetries = 3
	// upstreamStatTimeout 是探测上游长度与 Range 支持的超时。
	upstreamStatTimeout = 20 * time.Second
)

// upstreamUA 是向上游请求时带的 UA。googlevideo 不校验 UA；B 站等站点走
// 管道模式不经过这里，填浏览器 UA 只是为了不在 CDN 日志里显得异常。
const upstreamUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

// newProxyHTTPClient 按设置里的代理构造上游请求客户端。
// http(s) 代理走 Transport.Proxy；socks5 走 x/net/proxy 的 DialContext；
// none 模式显式禁用代理（否则 Transport 会读环境变量，无法真正直连）。
func newProxyHTTPClient(opts Options) (*http.Client, error) {
	addr := EffectiveProxy(opts)
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		MaxIdleConnsPerHost:   upstreamWorkers * 2,
	}
	if addr == "" {
		return &http.Client{Transport: transport}, nil
	}
	u, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("代理地址无效 %q: %w", addr, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(u)
	case "socks5", "socks5h", "socks":
		dialer, err := proxy.SOCKS5("tcp", u.Host, nil, &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second})
		if err != nil {
			return nil, fmt.Errorf("SOCKS5 代理不可用 %q: %w", addr, err)
		}
		transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.Dial(network, "")
		}
		// 注意：x/net/proxy 的 Dialer 接口不接受目标地址以外的 ctx，
		// 这里按 Transport 语义用 dialer 直连上游主机；上游主机从请求解析。
		transport.DialContext = socksDialContext(dialer)
	default:
		return nil, fmt.Errorf("不支持的代理协议 %q（仅支持 http/https/socks5）", u.Scheme)
	}
	return &http.Client{Transport: transport}, nil
}

// socksDialContext 把 x/net/proxy 的 Dialer 适配为 Transport.DialContext。
// SOCKS 握手本身很快，ctx 取消通过关闭返回的连接传导（上层调用方负责）。
func socksDialContext(dialer proxy.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		type result struct {
			conn net.Conn
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			c, err := dialer.Dial(network, addr)
			ch <- result{c, err}
		}()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-ch:
			return r.conn, r.err
		}
	}
}

// Upstream 是一路直链的 Go 原生拉取器：并发 Range 下载 + 稀疏缓存 + Range 服务。
type Upstream struct {
	url    string
	client *http.Client

	mu     sync.Mutex
	cond   *sync.Cond
	size   int64 // 上游总长度；-1 表示未知（不支持 Range 的回退模式）
	ranged bool
	file   *os.File
	have   []bool // 按 chunkSize 切分的位图（仅 Range 模式）
	filled int64  // 非 Range 模式下已顺序填充的字节数
	fetch  map[int64]bool
	wants  map[int64]time.Time // 分片→最近请求时间，调度优先用
	fatal  error
	closed bool

	ctx    context.Context
	cancel context.CancelFunc
}

// NewUpstream 探测上游（长度与 Range 支持）并建好缓存。探测失败直接返回错误，
// 由调用方在投屏点击时展示，而不是等电视拉流时才转圈。
func NewUpstream(rawURL string, opts Options) (*Upstream, error) {
	client, err := newProxyHTTPClient(opts)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	up := &Upstream{url: rawURL, client: client, ctx: ctx, cancel: cancel}
	up.cond = sync.NewCond(&up.mu)
	// 探测重试：googlevideo 会瞬时拒绝（403/超时），同一直链小退避重试，
	// 恢复通常只要 1-2 秒；比直接换新直链（重新走一次 yt-dlp，十几秒）便宜得多。
	var serr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				cancel()
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		if serr = up.stat(); serr == nil {
			break
		}
		Diagf("上游探测第%d次失败 host=%s err=%v", attempt+1, shortHost(rawURL), serr)
	}
	if serr != nil {
		cancel()
		return nil, serr
	}
	f, err := os.CreateTemp("", "anydlna-upstream-*")
	if err != nil {
		cancel()
		return nil, fmt.Errorf("创建上游缓存失败: %w", err)
	}
	up.file = f
	if up.size >= 0 {
		n := (up.size + upstreamChunkSize - 1) / upstreamChunkSize
		up.have = make([]bool, n)
	}
	up.fetch = map[int64]bool{}
	Diagf("上游就绪 长度=%d Range=%v host=%.60s", up.size, up.ranged, up.url)
	return up, nil
}

// stat 探测上游总长度与 Range 支持（Range: bytes=0-0）。
func (up *Upstream) stat() error {
	ctx, cancel := context.WithTimeout(up.ctx, upstreamStatTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, up.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", upstreamUA)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := up.client.Do(req)
	if err != nil {
		return fmt.Errorf("连接上游 %s 失败: %w", shortHost(up.url), err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		up.ranged = true
		up.size = parseTotalFromContentRange(resp.Header.Get("Content-Range"))
		if up.size < 0 {
			up.size = -1
		}
		return nil
	case http.StatusOK:
		// 上游不支持 Range：退化为顺序拉取（跳转不可用，行为与旧管道一致）。
		up.ranged = false
		up.size = resp.ContentLength
		return nil
	default:
		return fmt.Errorf("上游 %s 返回 %s", shortHost(up.url), resp.Status)
	}
}

// shortHost 截取 URL 的 host 部分用于报错（完整直链上千字符，不能进日志）。
func shortHost(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	if len(rawURL) > 60 {
		return rawURL[:60]
	}
	return rawURL
}

// parseTotalFromContentRange 从 "bytes 0-0/12345" 解析出 12345。
func parseTotalFromContentRange(v string) int64 {
	parts := strings.Split(v, "/")
	if len(parts) != 2 {
		return -1
	}
	n, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// Size 返回上游总长度（-1 表示未知）。
func (up *Upstream) Size() int64 {
	up.mu.Lock()
	defer up.mu.Unlock()
	return up.size
}

// WarmHead 后台预取头部最多 n 字节（moov 索引区）：ffmpeg 起播/定位的
// 第一次读取几乎总是头部，预热后首包等待只剩定位点的拉取。
// 非阻塞，失败由后续正式读取的重试覆盖，不单独报错。
func (up *Upstream) WarmHead(n int64) {
	up.mu.Lock()
	defer up.mu.Unlock()
	if up.closed || up.fatal != nil {
		return
	}
	for off := int64(0); off < n; off += upstreamChunkSize {
		up.noteWantLocked(off)
	}
	up.kickWorkersLocked()
}

// ensureLocked 保证 [start, end) 可读，必要时拉起 worker 并阻塞等待。
// 调用方须持有 up.mu；等待期间释放锁，靠 cond 唤醒。
// ctx 用于客户端断开时提前返回。
func (up *Upstream) ensureLocked(ctx context.Context, start, end int64) error {
	for {
		// 每次循环都刷新需求（阻塞中的跳转等待者保持新鲜，不会被
		// 顺序播放流的请求位置淹没，见 pickChunkLocked）。
		up.noteWantLocked(start)
		if up.fatal != nil {
			return up.fatal
		}
		if up.closed {
			return fmt.Errorf("上游已关闭")
		}
		ready := true
		if up.size >= 0 && start >= up.size {
			return io.EOF
		}
		if end > up.size && up.size >= 0 {
			end = up.size
		}
		for off := start; off < end; {
			if up.ranged {
				idx := off / upstreamChunkSize
				if idx < int64(len(up.have)) && up.have[idx] {
					off = (idx + 1) * upstreamChunkSize
					continue
				}
			} else if off < up.filled {
				// 非 Range 模式：只支持从头顺序填充，读取位置必须已填。
				off = up.filled
				continue
			}
			ready = false
			break
		}
		if ready {
			return nil
		}
		up.kickWorkersLocked()
		// 等待数据或关闭：cond 等待与 ctx 取消二选一。
		waitDone := make(chan struct{})
		go func() {
			up.mu.Lock()
			up.cond.Wait()
			up.mu.Unlock()
			close(waitDone)
		}()
		up.mu.Unlock()
		select {
		case <-ctx.Done():
			<-waitDone
			up.mu.Lock()
			return ctx.Err()
		case <-waitDone:
			up.mu.Lock()
		}
	}
}

// wantTTL 是需求位置的保鲜期：超过它未再被请求的区域视为读者已离开。
const wantTTL = 20 * time.Second

// noteWantLocked 记录请求位置供调度优先；调用方须持有 up.mu。
// 按分片去重 + 保鲜：顺序播放流的高频请求只刷新自己所在分片，不会把
// 跳转等待者的位置挤掉（旧实现是定长 8 的追加队列，跳转位置几秒就被淹没，
// 跳转流永远等不到 worker，表现为跳转后长时间零字节）。
func (up *Upstream) noteWantLocked(off int64) {
	if up.wants == nil {
		up.wants = map[int64]time.Time{}
	}
	up.wants[off/upstreamChunkSize] = time.Now()
}

// kickWorkersLocked 按需拉起 worker 至上限；调用方须持有 up.mu。
// 有未服务的远跳转需求时多允许一个 worker，避免跳转等满顺序预取。
func (up *Upstream) kickWorkersLocked() {
	active := 0
	for range up.fetch {
		active++
	}
	limit := upstreamWorkers
	if up.hasUnservedJumpLocked() {
		limit++
	}
	for active < limit {
		idx, ok := up.pickChunkLocked()
		if !ok {
			return
		}
		up.fetch[idx] = true
		active++
		go up.fetchChunk(idx)
	}
}

// hasUnservedJumpLocked 报告是否存在未服务的远跳转需求：保鲜需求中，
// 落在顺序前沿 8MB 之后、且其后 8MB 内仍有空洞。调用方须持有 up.mu。
func (up *Upstream) hasUnservedJumpLocked() bool {
	if !up.ranged {
		return false
	}
	front := up.seqFrontLocked()
	now := time.Now()
	for c, at := range up.wants {
		if now.Sub(at) >= wantTTL || c <= front+8 {
			continue
		}
		for i := c; i < c+8 && i < int64(len(up.have)); i++ {
			if !up.have[i] && !up.fetch[i] {
				return true
			}
		}
	}
	return false
}

// seqFrontLocked 返回顺序前沿（第一个空洞分片）；调用方须持有 up.mu。
func (up *Upstream) seqFrontLocked() int64 {
	for i := range up.have {
		if !up.have[int64(i)] {
			return int64(i)
		}
	}
	return int64(len(up.have))
}

// pickChunkLocked 选下一个要拉的分片：远跳转优先，其次新需求附近，再顺序预取。
// 非 Range 模式只允许一个顺序拉取者。调用方须持有 up.mu。
func (up *Upstream) pickChunkLocked() (int64, bool) {
	if !up.ranged {
		if len(up.fetch) > 0 {
			return 0, false
		}
		return 0, true
	}
	claimed := func(i int64) bool {
		if i < 0 || i >= int64(len(up.have)) || up.have[i] {
			return true
		}
		return up.fetch[i]
	}
	now := time.Now()
	type want struct {
		chunk int64
		at    time.Time
	}
	var ws []want
	for c, at := range up.wants {
		if now.Sub(at) < wantTTL {
			ws = append(ws, want{c, at})
		}
	}
	sort.Slice(ws, func(a, b int) bool { return ws[a].at.After(ws[b].at) })
	front := up.seqFrontLocked()
	// 远跳转优先：落在顺序前沿 8MB 之后的需求先找其后 8MB 内空洞。
	// 跳转流不等顺序预取填完，全程最多慢一个分片的拉取时间。
	for _, w := range ws {
		if w.chunk <= front+8 {
			continue
		}
		for i := w.chunk; i < w.chunk+8; i++ {
			if i >= int64(len(up.have)) {
				break
			}
			if !claimed(i) {
				return i, true
			}
		}
	}
	// 其余需求按由新到老，各找其后 8MB 内空洞。
	for _, w := range ws {
		for i := w.chunk; i < w.chunk+8; i++ {
			if i >= int64(len(up.have)) {
				break
			}
			if !claimed(i) {
				return i, true
			}
		}
	}
	// 顺序预取：找第一个空洞。
	for i := range up.have {
		if !claimed(int64(i)) {
			return int64(i), true
		}
	}
	return 0, false
}

// fetchChunk 拉取单个分片，成功写入缓存，失败重试耗尽后标记整路失败。
// 非 Range 模式下 idx 无意义：单 worker 从头顺序拉取（跳过已填部分）。
func (up *Upstream) fetchChunk(idx int64) {
	defer func() {
		up.mu.Lock()
		delete(up.fetch, idx)
		up.cond.Broadcast()
		up.mu.Unlock()
	}()
	up.mu.Lock()
	ranged := up.ranged
	size := up.size
	up.mu.Unlock()

	start := idx * upstreamChunkSize
	end := int64(-1)
	if ranged && size >= 0 {
		end = start + upstreamChunkSize - 1
		if end >= size {
			end = size - 1
		}
	}

	var lastErr error
	for attempt := 0; attempt < upstreamChunkRetries; attempt++ {
		if up.ctx.Err() != nil {
			return
		}
		// 退避重试：限流中的上游经不起热循环 hammer，1s/2s 阶梯等待。
		if attempt > 0 {
			select {
			case <-up.ctx.Done():
				return
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		ctx, cancel := context.WithTimeout(up.ctx, 30*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, up.url, nil)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", upstreamUA)
		if ranged {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		}
		resp, err := up.client.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		want := http.StatusOK
		if ranged {
			want = http.StatusPartialContent
		}
		if resp.StatusCode != want {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			cancel()
			lastErr = fmt.Errorf("上游返回 %s", resp.Status)
			continue
		}
		var skip, pos int64
		if ranged {
			pos = start
		} else {
			// 非 Range 只能从头取：跳过已填部分，续写水位线。
			up.mu.Lock()
			skip, pos = up.filled, up.filled
			up.mu.Unlock()
		}
		n, err := up.writeSkipping(resp.Body, pos, skip)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		up.mu.Lock()
		if ranged {
			if idx < int64(len(up.have)) {
				up.have[idx] = true
			}
		} else {
			up.filled = pos + n
			if size >= 0 && up.filled > size {
				up.filled = size
			}
		}
		up.mu.Unlock()
		return
	}
	up.mu.Lock()
	if up.fatal == nil {
		up.fatal = fmt.Errorf("拉取上游分片 %d 失败（已重试）: %v", idx, lastErr)
		Diagf("上游失败 host=%.60s err=%v", up.url, up.fatal)
	}
	up.mu.Unlock()
}

// writeSkipping 把 body 写入缓存文件的 pos 偏移处，先丢弃前 skip 字节，
// 返回实际写入字节数。
func (up *Upstream) writeSkipping(body io.Reader, pos, skip int64) (int64, error) {
	if skip > 0 {
		if _, err := io.CopyN(io.Discard, body, skip); err != nil {
			return 0, err
		}
	}
	return up.writeAtFull(body, pos)
}

// writeAtFull 把 body 写入缓存文件的 start 偏移处，返回写入字节数。
func (up *Upstream) writeAtFull(body io.Reader, start int64) (int64, error) {
	buf := make([]byte, 128*1024)
	var n int64
	for {
		c, rerr := body.Read(buf)
		if c > 0 {
			up.mu.Lock()
			_, werr := up.file.WriteAt(buf[:c], start+n)
			up.mu.Unlock()
			if werr != nil {
				return n, werr
			}
			n += int64(c)
		}
		if rerr != nil {
			if rerr == io.EOF {
				return n, nil
			}
			return n, rerr
		}
	}
}

// ServeHTTP 以标准 Range 语义对外服务（ffmpeg 是唯一的客户端）。
func (up *Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "只支持 GET/HEAD", http.StatusMethodNotAllowed)
		return
	}
	up.mu.Lock()
	size := up.size
	up.mu.Unlock()

	start, end := int64(0), size-1
	status := http.StatusOK
	if size < 0 {
		// 长度未知：只支持从头顺序播（上游不支持 Range 的回退）。
		if rng := r.Header.Get("Range"); rng != "" {
			http.Error(w, "上游不支持定位", http.StatusRequestedRangeNotSatisfiable)
			return
		}
	} else if rng := r.Header.Get("Range"); rng != "" {
		s, e, ok := parseRange(rng, size)
		if !ok {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			http.Error(w, "Range 非法", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		start, end = s, e
		status = http.StatusPartialContent
	}

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "video/mp4")
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	}
	if status == http.StatusPartialContent {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	}
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}

	const step = 256 * 1024
	for off := start; off <= end; {
		if r.Context().Err() != nil {
			return
		}
		to := off + step - 1
		if to > end {
			to = end
		}
		up.mu.Lock()
		err := up.ensureLocked(r.Context(), off, to+1)
		var chunk []byte
		if err == nil {
			chunk = make([]byte, to-off+1)
			_, err = up.file.ReadAt(chunk, off)
		}
		up.mu.Unlock()
		if err != nil {
			// 中途失败：直接断流（ffmpeg 会报错而非静默卡死）。
			return
		}
		if _, err := w.Write(chunk); err != nil {
			return
		}
		off = to + 1
	}
}

// parseRange 解析 "bytes=start-end"（end 可空），返回闭区间。
func parseRange(v string, size int64) (int64, int64, bool) {
	v = strings.TrimSpace(strings.TrimPrefix(v, "bytes="))
	parts := strings.SplitN(v, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	var start, end int64
	var err error
	if parts[0] == "" {
		// 后缀 Range（最后 N 字节）：ffmpeg 定位不用，仍正确支持。
		n, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, true
	}
	start, err = strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false
	}
	if strings.TrimSpace(parts[1]) == "" {
		return start, size - 1, true
	}
	end, err = strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil || end < start {
		return 0, 0, false
	}
	if end >= size {
		end = size - 1
	}
	return start, end, true
}

// Close 终止拉取、关闭服务 associated 资源并删除缓存文件。
func (up *Upstream) Close() error {
	up.mu.Lock()
	if up.closed {
		up.mu.Unlock()
		return nil
	}
	up.closed = true
	up.mu.Unlock()
	up.cancel()
	up.cond.Broadcast()
	var err error
	if up.file != nil {
		name := up.file.Name()
		_ = up.file.Close()
		err = os.Remove(name)
	}
	return err
}
