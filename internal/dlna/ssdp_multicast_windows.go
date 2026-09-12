//go:build windows

package dlna

import (
	"net"
	"syscall"
)

// setMulticastIf 把 socket 的组播出口接口固定到 ifaceIP，避免默认路由指向虚拟网卡。
// Windows 上 IP_MULTICAST_IF 取 4 字节的 in_addr（与 Go 标准库
// net/sockoptip4_windows.go 的做法一致），而非 BSD 的 IPMreq 结构。
func setMulticastIf(conn *net.UDPConn, ifaceIP net.IP) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	ip4 := ifaceIP.To4()
	if ip4 == nil {
		return
	}
	var addr [4]byte
	copy(addr[:], ip4)
	_ = raw.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInet4Addr(syscall.Handle(fd), syscall.IPPROTO_IP,
			syscall.IP_MULTICAST_IF, addr)
	})
}
