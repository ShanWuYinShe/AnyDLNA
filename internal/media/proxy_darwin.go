//go:build darwin

package media

// systemProxy 在 macOS 上读取系统网络代理设置（scutil --proxy）。
func systemProxy() string { return scutilProxy() }
