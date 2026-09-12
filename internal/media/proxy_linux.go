//go:build linux

package media

// systemProxy 在 Linux 上读取桌面环境的代理设置。
// 目前覆盖 GNOME 系（gsettings）；其他桌面环境可依赖代理环境变量。
func systemProxy() string { return gnomeProxy() }
