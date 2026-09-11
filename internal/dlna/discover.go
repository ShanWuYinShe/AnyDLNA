package dlna

import (
	"context"
	"sync"
	"time"
)

// DiscoverRenderers 搜索局域网并解析所有具备 AVTransport 服务的渲染设备。
// 同一设备可能通过多个地址被发现，结果按 UDN 去重。
func DiscoverRenderers(ctx context.Context, timeout time.Duration) ([]*Device, error) {
	raws, err := searchDevices(ctx, timeout)
	if err != nil {
		return nil, err
	}

	client := defaultHTTPClient()
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		out []*Device
	)
	for _, raw := range raws {
		wg.Add(1)
		go func(location string) {
			defer wg.Done()
			dev, err := Describe(ctx, client, location)
			if err != nil || !dev.HasAVTransport() {
				return
			}
			mu.Lock()
			out = append(out, dev)
			mu.Unlock()
		}(raw.Location)
	}
	wg.Wait()

	seen := map[string]bool{}
	unique := out[:0:0]
	for _, dev := range out {
		if seen[dev.UDN] {
			continue
		}
		seen[dev.UDN] = true
		unique = append(unique, dev)
	}
	return unique, nil
}
