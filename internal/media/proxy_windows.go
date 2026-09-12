//go:build windows

package media

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// systemProxy 在 Windows 上读取当前用户的 Internet 代理设置。
// 位置：HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings。
func systemProxy() string {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()

	enabled := false
	if v, _, err := key.GetIntegerValue("ProxyEnable"); err == nil {
		enabled = v != 0
	}
	server, _, _ := key.GetStringValue("ProxyServer")
	pacURL, _, _ := key.GetStringValue("ProxyPacUrl")
	bypass, _, _ := key.GetStringValue("ProxyOverride")
	return parseWindowsProxy(enabled, strings.TrimSpace(server), pacURL, bypass)
}
