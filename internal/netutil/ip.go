// Package netutil 提供网络相关的小工具函数。
package netutil

import "net"

// LANIP 返回本机在局域网中的 IPv4 地址。
// 通过对公共地址发起 UDP 拨接（不实际发包）让内核选择默认路由的出口地址，
// 该地址即电视等局域网设备可达的本机地址。
func LANIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()

	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", net.InvalidAddrError(conn.LocalAddr().String())
	}
	return addr.IP.String(), nil
}
