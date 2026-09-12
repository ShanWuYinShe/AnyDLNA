package dlna

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	ssdpIP   = "239.255.255.250"
	ssdpPort = 1900
)

// rawDevice 是 SSDP 收集到的原始设备线索。
type rawDevice struct {
	USN      string
	Location string
}

// searchDevices 发现局域网内的 UPnP 设备线索：
// 1) 在每个具备 IPv4 的接口上组播 M-SEARCH（多轮发送，兼容接口选择错误的场景）；
// 2) 同时被动监听 NOTIFY ssdp:alive 广播（部分设备不应答搜索但会主动广播）。
// 结果按 USN 与 LOCATION 双重去重。
func searchDevices(ctx context.Context, timeout time.Duration) ([]rawDevice, error) {
	c := &collector{byUSN: map[string]bool{}, byLocation: map[string]bool{}}
	deadline := time.Now().Add(timeout)

	// 被动监听 NOTIFY；端口被其他应用占用时跳过（不影响主动搜索）。
	if passive, err := net.ListenMulticastUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP(ssdpIP), Port: ssdpPort}); err == nil {
		defer passive.Close()
		go collectFrom(c, passive)
	}

	// 每个接口一个 socket，绑定源地址并固定组播出口接口。
	var sockets []*net.UDPConn
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for i := range ifaces {
		iface := &ifaces[i]
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil || ipnet.IP.IsLoopback() {
				continue
			}
			conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ipnet.IP})
			if err != nil {
				continue
			}
			setMulticastIf(conn, ipnet.IP)
			sockets = append(sockets, conn)
			go collectFrom(c, conn)
			break // 每个接口只取第一个 IPv4 地址
		}
	}
	if len(sockets) == 0 {
		return nil, fmt.Errorf("没有可用的 IPv4 网络接口")
	}
	defer func() {
		for _, conn := range sockets {
			conn.Close()
		}
	}()

	// 多轮发送 M-SEARCH，留下余量时间接收响应（设备按 MX 随机延迟应答）。
	sendRound := func() {
		for _, conn := range sockets {
			// 多种目标类型并发搜索：部分设备只对特定 ST 应答。
			for _, st := range []string{
				AVTransportServiceType,
				"urn:schemas-upnp-org:device:MediaRenderer:1",
				"upnp:rootdevice",
				"ssdp:all",
			} {
				msg := msearchMessage(st)
				_, _ = conn.WriteToUDP([]byte(msg), &net.UDPAddr{IP: net.ParseIP(ssdpIP), Port: ssdpPort})
			}
		}
	}
	sendRound()
	for i := 0; i < 2 && time.Now().Add(2*time.Second).Before(deadline); i++ {
		select {
		case <-ctx.Done():
			return c.snapshot(), nil
		case <-time.After(1500 * time.Millisecond):
		}
		sendRound()
	}
	if rest := time.Until(deadline); rest > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(rest):
		}
	}
	return c.snapshot(), nil
}

// msearchMessage 构造一条 M-SEARCH 请求。
func msearchMessage(st string) string {
	return strings.Join([]string{
		"M-SEARCH * HTTP/1.1",
		"HOST: " + ssdpIP + ":1900",
		`MAN: "ssdp:discover"`,
		"MX: 3",
		"ST: " + st,
		"", "",
	}, "\r\n")
}

// setMulticastIf 把 socket 的组播出口接口固定到 ifaceIP，避免默认路由指向虚拟网卡。
func setMulticastIf(conn *net.UDPConn, ifaceIP net.IP) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		var mreq syscall.IPMreq
		copy(mreq.Multiaddr[:], ifaceIP.To4())
		_ = syscall.SetsockoptIPMreq(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, &mreq)
	})
}

// collector 并发安全地汇总设备线索。
type collector struct {
	mu         sync.Mutex
	byUSN      map[string]bool
	byLocation map[string]bool
	out        []rawDevice
}

func (c *collector) add(usn, location string) {
	if location == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if usn != "" && c.byUSN[usn] {
		return
	}
	if c.byLocation[location] {
		return
	}
	c.byUSN[usn] = true
	c.byLocation[location] = true
	c.out = append(c.out, rawDevice{USN: usn, Location: location})
}

func (c *collector) snapshot() []rawDevice {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]rawDevice(nil), c.out...)
}

// collectFrom 持续读取 socket 上的 SSDP 包：
// 接收 M-SEARCH 的单播响应（HTTP/1.1 200）与 NOTIFY ssdp:alive 广播。
// 只保留渲染设备相关的 NOTIFY，避免路由器/打印机等设备产生大量无效描述请求。
func collectFrom(c *collector, conn *net.UDPConn) {
	buf := make([]byte, 8192)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		kind, h := parseSSDPHeaders(buf[:n])
		switch kind {
		case "search-response":
			c.add(h["USN"], h["LOCATION"])
		case "notify":
			if strings.EqualFold(h["NTS"], "ssdp:alive") && isRendererTarget(h["NT"]) {
				c.add(h["USN"], h["LOCATION"])
			}
		}
	}
}

// isRendererTarget 判断 NOTIFY 的 NT 是否指向媒体渲染设备。
func isRendererTarget(nt string) bool {
	nt = strings.ToLower(nt)
	return strings.Contains(nt, "mediarenderer") || strings.Contains(nt, "avtransport")
}

// parseSSDPHeaders 解析一条 SSDP 报文：返回报文种类与头字段（键统一大写）。
func parseSSDPHeaders(data []byte) (kind string, headers map[string]string) {
	text := string(data)
	lines := strings.Split(text, "\r\n")
	if len(lines) == 0 {
		return "other", nil
	}
	switch {
	case strings.HasPrefix(strings.ToUpper(lines[0]), "HTTP/"):
		kind = "search-response"
	case strings.HasPrefix(strings.ToUpper(lines[0]), "NOTIFY"):
		kind = "notify"
	case strings.HasPrefix(strings.ToUpper(lines[0]), "M-SEARCH"):
		fallthrough
	default:
		return "other", nil // 自己发出的 M-SEARCH（组播回环）或其他无关报文
	}
	headers = map[string]string{}
	for _, line := range lines[1:] {
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		headers[strings.ToUpper(strings.TrimSpace(line[:idx]))] = strings.TrimSpace(line[idx+1:])
	}
	return kind, headers
}
