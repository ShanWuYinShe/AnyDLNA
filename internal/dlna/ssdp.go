package dlna

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"
)

const ssdpAddr = "239.255.255.250:1900"

// rawDevice 是 SSDP 搜索响应中的原始设备线索。
type rawDevice struct {
	USN      string
	Location string
}

// searchDevices 组播 M-SEARCH 并收集超时时间内的全部响应，
// 按 USN 去重。搜索目标同时覆盖 ssdp:all 与 AVTransport，
// 以最大化兼容各厂商渲染设备的响应行为。
func searchDevices(ctx context.Context, timeout time.Duration) ([]rawDevice, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	deadline := time.Now().Add(timeout)
	conn.SetReadDeadline(deadline)

	// 每个 ST 独立发送一次 M-SEARCH；设备会按 MX 在随机延迟后回复。
	for _, st := range []string{"ssdp:all", AVTransportServiceType} {
		msg := strings.Join([]string{
			"M-SEARCH * HTTP/1.1",
			"HOST: " + ssdpAddr,
			`MAN: "ssdp:discover"`,
			"MX: 3",
			"ST: " + st,
			"", "",
		}, "\r\n")
		if _, err := conn.WriteToUDP([]byte(msg), &net.UDPAddr{IP: net.ParseIP("239.255.255.250"), Port: 1900}); err != nil {
			return nil, err
		}
	}

	var (
		mu   sync.Mutex
		seen = map[string]bool{}
		out  []rawDevice
	)
	buf := make([]byte, 4096)
	for {
		remain := time.Until(deadline)
		if remain <= 0 {
			break
		}
		conn.SetReadDeadline(deadline)
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				break
			}
			// 单次读失败不终止收集，继续等下一次。
			continue
		}
		dev, ok := parseSSDPResponse(buf[:n])
		if !ok {
			continue
		}
		mu.Lock()
		if !seen[dev.USN] {
			seen[dev.USN] = true
			out = append(out, dev)
		}
		mu.Unlock()
		// 外层取消（如窗口关闭）时立即停止收集。
		if ctx.Err() != nil {
			break
		}
	}
	return out, nil
}

// parseSSDPResponse 解析一条 SSDP 响应，提取 USN 与 LOCATION 头。
func parseSSDPResponse(data []byte) (rawDevice, bool) {
	text := string(data)
	lines := strings.Split(text, "\r\n")
	if len(lines) == 0 || !strings.HasPrefix(strings.ToUpper(lines[0]), "HTTP/") {
		return rawDevice{}, false
	}
	var dev rawDevice
	for _, line := range lines[1:] {
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(line[:idx]))
		val := strings.TrimSpace(line[idx+1:])
		switch key {
		case "USN":
			dev.USN = val
		case "LOCATION":
			dev.Location = val
		case "ST", "NT":
			// 保留字段，暂不使用。
		}
	}
	if dev.USN == "" || dev.Location == "" {
		return rawDevice{}, false
	}
	return dev, true
}
