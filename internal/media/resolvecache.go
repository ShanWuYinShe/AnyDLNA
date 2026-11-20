package media

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 在线解析缓存：同一视频重复投屏（调试、重播、断线重连）时跳过 yt-dlp，
// 省掉 7~30 秒的解析。直链带 expire（通常数小时有效），缓存 TTL 取 1 小时，
// 与转码器的直链复用窗口（directCacheTTL）一致。
//
// 内存 + 落盘两级：内存保证同次运行即时命中，落盘保证第二天打开 App 重播
// 昨天的视频依然秒开。文件损坏/过期只影响命中率，不影响正确性
// （未命中就走正常解析）。

// resolveCacheTTL 与 directCacheTTL 同值，语义见上。
const resolveCacheTTL = time.Hour

// resolveCacheMaxEntries 落盘条数上限，防止文件无限增长。
const resolveCacheMaxEntries = 50

// resolveCacheOverridePath 供单测指定缓存文件；空串表示用默认位置。
var resolveCacheOverridePath string

type resolveCacheEntry struct {
	Resolved Resolved `json:"resolved"`
	URLs     []string `json:"urls"`
	// Headers 是每条直链必需的 HTTP 请求头（与 URLs 一一对应），
	// 例如 B 站 CDN 校验 Referer。旧缓存条目无此字段，按空处理。
	Headers   []map[string]string `json:"headers,omitempty"`
	FetchedAt time.Time           `json:"fetched_at"`
}

var resolveCacheMu sync.Mutex
var resolveCache map[string]resolveCacheEntry
var resolveCacheLoaded bool

// resolveCacheFile 返回缓存文件路径。
func resolveCacheFile() (string, error) {
	if resolveCacheOverridePath != "" {
		return resolveCacheOverridePath, nil
	}
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "resolve_cache.json"), nil
}

// loadResolveCacheLocked 惰性加载落盘缓存；调用方须持有 resolveCacheMu。
func loadResolveCacheLocked() {
	if resolveCacheLoaded {
		return
	}
	resolveCacheLoaded = true
	resolveCache = map[string]resolveCacheEntry{}
	path, err := resolveCacheFile()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return
	}
	var disk map[string]resolveCacheEntry
	if err := json.Unmarshal(data, &disk); err != nil {
		return
	}
	now := time.Now()
	for url, e := range disk {
		if now.Sub(e.FetchedAt) < resolveCacheTTL && len(e.URLs) > 0 {
			resolveCache[url] = e
		}
	}
}

// saveResolveCacheLocked 落盘（只保留未过期的前 N 条）；调用方须持有 resolveCacheMu。
// 落盘失败只记 Diag，不阻塞投屏。
func saveResolveCacheLocked() {
	path, err := resolveCacheFile()
	if err != nil {
		return
	}
	now := time.Now()
	out := make(map[string]resolveCacheEntry, len(resolveCache))
	for url, e := range resolveCache {
		if now.Sub(e.FetchedAt) >= resolveCacheTTL {
			continue
		}
		out[url] = e
		if len(out) >= resolveCacheMaxEntries {
			break
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		Diagf("解析缓存落盘失败: %v", err)
	}
}

// resolveCacheKey 生成缓存键：解析结果随清晰度档位不同而不同
// （直链与编码都会变），必须把分辨率上限并入键；0（最高可用）保持纯 URL，
// 与历史缓存条目兼容。
func resolveCacheKey(url string, maxHeight int) string {
	if maxHeight <= 0 {
		return url
	}
	return fmt.Sprintf("%s#h%d", url, maxHeight)
}

// lookupResolveCache 命中返回解析结果与直链。
func lookupResolveCache(url string) (*Resolved, []string, bool) {
	resolved, urls, _, ok := lookupResolveCacheWithHeaders(url)
	return resolved, urls, ok
}

// lookupResolveCacheWithHeaders 命中返回解析结果、直链及其必需的请求头。
func lookupResolveCacheWithHeaders(url string) (*Resolved, []string, []map[string]string, bool) {
	resolveCacheMu.Lock()
	defer resolveCacheMu.Unlock()
	loadResolveCacheLocked()
	e, ok := resolveCache[url]
	if !ok || time.Since(e.FetchedAt) >= resolveCacheTTL || len(e.URLs) == 0 {
		return nil, nil, nil, false
	}
	resolved := e.Resolved
	return &resolved, append([]string(nil), e.URLs...), cloneHeaders(e.Headers), true
}

// cloneHeaders 深拷贝请求头切片（与直链一一对应）。
func cloneHeaders(headers []map[string]string) []map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make([]map[string]string, len(headers))
	for i, h := range headers {
		if len(h) == 0 {
			continue
		}
		c := make(map[string]string, len(h))
		for k, v := range h {
			c[k] = v
		}
		out[i] = c
	}
	return out
}

// storeResolveCache 写入内存与落盘。
func storeResolveCache(url string, resolved *Resolved, urls []string) {
	storeResolveCacheWithHeaders(url, resolved, urls, nil)
}

// storeResolveCacheWithHeaders 写入内存与落盘（含每条直链的请求头）。
func storeResolveCacheWithHeaders(url string, resolved *Resolved, urls []string, headers []map[string]string) {
	if resolved == nil || len(urls) == 0 {
		return
	}
	resolveCacheMu.Lock()
	defer resolveCacheMu.Unlock()
	loadResolveCacheLocked()
	resolveCache[url] = resolveCacheEntry{
		Resolved:  *resolved,
		URLs:      append([]string(nil), urls...),
		Headers:   cloneHeaders(headers),
		FetchedAt: time.Now(),
	}
	saveResolveCacheLocked()
}
