//go:build !darwin && !linux && !windows

package browser

// platformBrowserPaths 其他平台暂不内置已知路径，仅依赖 PATH 查找。
func platformBrowserPaths() []string { return nil }

// platformBrowserGlobs 其他平台无版本化目录需要匹配。
func platformBrowserGlobs() []string { return nil }
