package media

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withTempResolveCache 把缓存指向临时文件并隔离全局状态。
func withTempResolveCache(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "resolve_cache.json")
	resolveCacheMu.Lock()
	oldPath, oldCache, oldLoaded := resolveCacheOverridePath, resolveCache, resolveCacheLoaded
	resolveCacheOverridePath = path
	resolveCache, resolveCacheLoaded = nil, false
	resolveCacheMu.Unlock()
	t.Cleanup(func() {
		resolveCacheMu.Lock()
		resolveCacheOverridePath, resolveCache, resolveCacheLoaded = oldPath, oldCache, oldLoaded
		resolveCacheMu.Unlock()
	})
	return path
}

func sampleResolved() *Resolved {
	return &Resolved{Title: "T", DurationSec: 60, Extractor: "youtube", VideoCodec: "avc1", AudioCodec: "mp4a"}
}

// TestResolveUsesResolveCache 预览解析复用解析缓存：命中时不调用 yt-dlp
// （本机有无 yt-dlp 都应命中，恰证明「解析 → 投屏」不会重复解析）。
func TestResolveUsesResolveCache(t *testing.T) {
	withTempResolveCache(t)
	storeResolveCache("https://cache-hit.example/v", sampleResolved(), []string{"http://a/v"})
	res, err := Resolve(context.Background(), "https://cache-hit.example/v", Options{ProxyMode: ProxyModeNone})
	if err != nil {
		t.Fatalf("缓存命中应直接返回: %v", err)
	}
	if res.Title != "T" || res.DurationSec != 60 {
		t.Errorf("命中结果不一致: %+v", res)
	}
}

// TestResolveCacheHitAndMiss 存取命中、未命中与空直链不缓存。
func TestResolveCacheHitAndMiss(t *testing.T) {
	withTempResolveCache(t)
	if _, _, ok := lookupResolveCache("u"); ok {
		t.Fatal("空缓存不应命中")
	}
	storeResolveCache("u", sampleResolved(), []string{"http://a/v", "http://a/au"})
	res, urls, ok := lookupResolveCache("u")
	if !ok || res.Title != "T" || len(urls) != 2 {
		t.Fatalf("应命中: %+v %v %v", res, urls, ok)
	}
	// 空直链不缓存（如下游回退取链的站点），避免占位。
	storeResolveCache("nou", sampleResolved(), nil)
	if _, _, ok := lookupResolveCache("nou"); ok {
		t.Error("空直链不应缓存")
	}
}

// TestResolveCacheExpiry 过期条目不命中。
func TestResolveCacheExpiry(t *testing.T) {
	withTempResolveCache(t)
	storeResolveCache("u", sampleResolved(), []string{"http://a/v"})
	resolveCacheMu.Lock()
	e := resolveCache["u"]
	e.FetchedAt = time.Now().Add(-2 * resolveCacheTTL)
	resolveCache["u"] = e
	resolveCacheMu.Unlock()
	if _, _, ok := lookupResolveCache("u"); ok {
		t.Error("过期条目不应命中")
	}
}

// TestResolveCachePersist 落盘后重载仍命中，坏文件不炸。
func TestResolveCachePersist(t *testing.T) {
	path := withTempResolveCache(t)
	storeResolveCache("u", sampleResolved(), []string{"http://a/v"})
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("应落盘: %v", err)
	}
	// 模拟重启：清空内存重载。
	resolveCacheMu.Lock()
	resolveCache, resolveCacheLoaded = nil, false
	resolveCacheMu.Unlock()
	if _, urls, ok := lookupResolveCache("u"); !ok || len(urls) != 1 {
		t.Fatalf("重载后应命中: %v %v", urls, ok)
	}
	// 坏文件容忍。
	if err := os.WriteFile(path, []byte("{坏"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolveCacheMu.Lock()
	resolveCache, resolveCacheLoaded = nil, false
	resolveCacheMu.Unlock()
	if _, _, ok := lookupResolveCache("u"); ok {
		t.Error("坏文件不应命中，但也不应报错")
	}
}
