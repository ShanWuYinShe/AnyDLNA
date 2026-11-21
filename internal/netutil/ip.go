// Package netutil 提供网络相关的小工具函数。
package netutil

import (
	"net"
	"strings"
)

// LANIP 返回本机在局域网中的 IPv4 地址。
// 优先通过对公共地址发起 UDP 拨接（不实际发包）让内核选择默认路由的出口地址；
// 若无公网路由（离线/纯局域网/防火墙阻止），则自动扫描本机非回环网卡选取活跃的局域网 IPv4。
func LANIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP.To4() != nil && !addr.IP.IsLoopback() && !addr.IP.IsUnspecified() {
			return addr.IP.String(), nil
		}
	}
	return fallbackLANIP()
}

// fallbackLANIP 扫描本机物理/局域网网络接口，选出处于运行状态的私有 IPv4 地址。
func fallbackLANIP() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	var candidate string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsUnspecified() || ip4.IsLinkLocalUnicast() {
				continue
			}
			// 优先选择标准私有网段（192.168.x.x, 10.x.x.x, 172.16-31.x.x）
			if ip4.IsPrivate() {
				return ip4.String(), nil
			}
			if candidate == "" {
				candidate = ip4.String()
			}
		}
	}
	if candidate != "" {
		return candidate, nil
	}
	return "", &net.OpError{Op: "dial", Net: "udp", Err: net.UnknownNetworkError("未找到有效的局域网 IPv4 地址")}
}

// LANIPFor 返回本机与 targetHost 通信时使用的局域网 IPv4 地址。
// targetHost 可以是 IP 或 host:port；若为空或拨接失败，回退到默认的 LANIP()。
//
// 为什么需要按目标设备选择 IP：当本机开启 VPN / TUN（如 Surge、Clash、Tailscale 等）
// 或存在多块物理/虚拟网卡（Wi-Fi、以太网、Docker 桥接网卡）时，默认网关 (8.8.8.8)
// 往往指向虚拟网卡（如 198.18.0.1 或 10.x.x.x），电视端无法访问此虚拟地址。
// 通过针对电视的实际 IP 探测内核路由，内核会精确返回与电视处于同一物理局域网的本机网卡 IP。
func LANIPFor(targetHost string) (string, error) {
	host := strings.TrimSpace(targetHost)
	if host == "" {
		return LANIP()
	}
	if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
		host = h
	}
	// 对目标设备发起 UDP 拨接探测（端口 80 仅用于构建套接字，UDP 不发包）
	conn, err := net.Dial("udp", net.JoinHostPort(host, "80"))
	if err != nil {
		return LANIP()
	}
	defer conn.Close()

	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP.To4() == nil || addr.IP.IsLoopback() || addr.IP.IsUnspecified() {
		return LANIP()
	}
	return addr.IP.String(), nil
}
