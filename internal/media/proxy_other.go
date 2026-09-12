//go:build !darwin && !windows && !linux

package media

// systemProxy 在其他平台不读取系统代理设置，仅依赖代理环境变量。
func systemProxy() string { return "" }
