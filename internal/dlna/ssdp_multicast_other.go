//go:build !windows

package dlna

import (
	"net"
	"syscall"
)

// setMulticastIf 把 socket 的组播出口接口固定到 ifaceIP，避免默认路由指向虚拟网卡。
// 非 Windows 平台沿用 BSD socket 语义。
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
