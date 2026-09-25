package dlna

import (
	"context"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// ListenAlive 常驻监听 SSDP NOTIFY ssdp:alive 组播广播，
// 把媒体渲染设备的线索（USN + LOCATION，按 LOCATION 去重）发到返回的 channel。
// 1900 端口被其他应用占用时返回错误，调用方可降级为仅主动搜索；
// ctx 取消后 channel 关闭，监听结束。
func ListenAlive(ctx context.Context) (<-chan rawDevice, error) {
	conn, err := net.ListenMulticastUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP(ssdpIP), Port: ssdpPort})
	if err != nil {
		return nil, err
	}
	out := make(chan rawDevice, 16)
	var (
		mu   sync.Mutex
		seen = map[string]bool{}
	)
	go func() {
		defer close(out)
		defer conn.Close()
		buf := make([]byte, 8192)
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			kind, h := parseSSDPHeaders(buf[:n])
			if kind != "notify" || !strings.EqualFold(h["NTS"], "ssdp:alive") || !isRendererTarget(h["NT"]) {
				continue
			}
			location := h["LOCATION"]
			if location == "" {
				continue
			}
			mu.Lock()
			dup := seen[location]
			seen[location] = true
			mu.Unlock()
			if dup {
				continue
			}
			select {
			case out <- rawDevice{USN: h["USN"], Location: location}:
			case <-ctx.Done():
				return
			}
		}
	}()
	// ctx 取消时关闭 socket 让读循环退出。
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	return out, nil
}

// descDedupTTL 是同一描述地址的重复解析抑制窗口。
const descDedupTTL = 10 * time.Minute

// descDedupLimit 是描述地址去重表的条数上限：条目按 LOCATION 无限增长
// 是长驻会话的慢性泄漏（数量随站点/端口变化无上限），超限后淘汰最旧的。
const descDedupLimit = 256

// WatchRenderers 常驻监听设备广播并解析描述，每发现一台新的、
// 具备 AVTransport 的渲染设备就回调一次 onDevice（按 UDN 去重，
// 同一描述地址 descDedupTTL 内不重复解析）。常驻发现与主动搜索互补：
// 部分电视不应答 M-SEARCH，但会周期性广播 alive。
func WatchRenderers(ctx context.Context, onDevice func(*Device)) error {
	alive, err := ListenAlive(ctx)
	if err != nil {
		return err
	}
	client := defaultHTTPClient()
	var (
		mu       sync.Mutex
		seenUDN  = map[string]bool{}
		lastDesc = map[string]time.Time{}
	)
	go func() {
		for clue := range alive {
			mu.Lock()
			if t, ok := lastDesc[clue.Location]; ok && time.Since(t) < descDedupTTL {
				mu.Unlock()
				continue
			}
			pruneLastDescLocked(lastDesc, time.Now(), descDedupLimit)
			lastDesc[clue.Location] = time.Now()
			mu.Unlock()

			dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			dev, err := Describe(dctx, client, clue.Location)
			cancel()
			if err != nil || !dev.HasAVTransport() {
				continue
			}
			mu.Lock()
			isNew := !seenUDN[dev.UDN]
			seenUDN[dev.UDN] = true
			mu.Unlock()
			if isNew && onDevice != nil {
				onDevice(dev)
			}
		}
	}()
	return nil
}

// pruneLastDescLocked 限制描述地址去重表的大小：先删除已过抑制窗口的
// 条目，仍超限时淘汰最旧的，只保留最近 limit 条。调用方须持有表锁；
// 被淘汰的地址最多代价是重复解析一次，无正确性影响。
func pruneLastDescLocked(m map[string]time.Time, now time.Time, limit int) {
	for loc, t := range m {
		if now.Sub(t) >= descDedupTTL {
			delete(m, loc)
		}
	}
	if len(m) <= limit {
		return
	}
	type entry struct {
		loc string
		t   time.Time
	}
	entries := make([]entry, 0, len(m))
	for loc, t := range m {
		entries = append(entries, entry{loc, t})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].t.Before(entries[j].t) })
	for _, e := range entries[:len(entries)-limit] {
		delete(m, e.loc)
	}
}
