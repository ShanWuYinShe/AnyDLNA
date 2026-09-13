package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalGUIPath 是 macOS 上从 Finder / Dock 启动的 GUI 应用实际拿到的 PATH。
// 它不含 Homebrew，这正是外部工具「明明装了却报未安装」的根因。
const minimalGUIPath = "/usr/bin:/bin:/usr/sbin:/sbin"

// TestResolveToolUnderGUIPath 回归测试：
// 在 GUI 应用的精简 PATH 下，仍应能通过常见安装目录找到工具。
//
// 这是真实 bug 的回归防护——此前只用 exec.LookPath，导致 Homebrew 安装的
// yt-dlp/ffmpeg 在 GUI 启动的应用里一律「未找到」。
func TestResolveToolUnderGUIPath(t *testing.T) {
	// 模拟 GUI 环境：PATH 只保留系统目录。
	t.Setenv("PATH", minimalGUIPath)
	// 清掉可能存在的覆盖变量，确保走自动搜索。
	t.Setenv(envYtDlpPath, "")
	t.Setenv(envFFmpegPath, "")

	for _, name := range []string{"yt-dlp", "ffmpeg", "ffprobe"} {
		t.Run(name, func(t *testing.T) {
			path, ok := ResolveTool(name)
			if !ok {
				// 本机确实没装时跳过（例如 CI 环境），但只要装了就必须找到。
				if _, err := os.Stat(filepath.Join("/opt/homebrew/bin", name)); err == nil {
					t.Fatalf("%s 已安装但未被解析到——GUI PATH 兼容性回归", name)
				}
				if _, err := os.Stat(filepath.Join("/usr/local/bin", name)); err == nil {
					t.Fatalf("%s 已安装但未被解析到——GUI PATH 兼容性回归", name)
				}
				t.Skipf("本机未安装 %s，跳过", name)
			}
			if !filepath.IsAbs(path) {
				t.Errorf("应返回绝对路径，得到 %q", path)
			}
			if st, err := os.Stat(path); err != nil || st.IsDir() {
				t.Errorf("解析结果不是可用文件: %q (%v)", path, err)
			}
		})
	}
}

// TestToolEnvIncludesSearchDirs 确认传给子进程的 PATH 已补上工具目录。
// 这一点至关重要：yt-dlp 合并分离的音视频流时会自己调用 ffmpeg，
// 若子进程 PATH 仍是精简值，yt-dlp 会因找不到 ffmpeg 而失败。
func TestToolEnvIncludesSearchDirs(t *testing.T) {
	t.Setenv("PATH", minimalGUIPath)

	env := ToolEnv()
	var pathValue string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			pathValue = strings.TrimPrefix(kv, "PATH=")
			break
		}
	}
	if pathValue == "" {
		t.Fatal("ToolEnv 未包含 PATH")
	}
	// 原有的系统目录必须保留。
	for _, dir := range strings.Split(minimalGUIPath, ":") {
		if !strings.Contains(pathValue, dir) {
			t.Errorf("不应丢失原有 PATH 项 %q: %s", dir, pathValue)
		}
	}
	// 真实存在的工具目录应被追加。
	dirs := existingToolDirs()
	if len(dirs) == 0 {
		t.Skip("本机没有可追加的工具目录")
	}
	for _, dir := range dirs {
		if !strings.Contains(pathValue, dir) {
			t.Errorf("应追加工具目录 %q: %s", dir, pathValue)
		}
	}
	// 不存在的目录不应被塞进 PATH，避免污染。
	for _, dir := range toolSearchDirs() {
		if strings.Contains(pathValue, dir) && !containsString(dirs, dir) {
			t.Errorf("不应加入不存在的目录 %q", dir)
		}
	}
	if strings.Contains(pathValue, "::") {
		t.Errorf("PATH 中不应出现空项: %s", pathValue)
	}
}

// TestResolveToolEnvOverride 覆盖环境变量显式指定的情形。
func TestResolveToolEnvOverride(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envYtDlpPath, fake)

	got, ok := ResolveTool("yt-dlp")
	if !ok {
		t.Fatal("应解析到环境变量指定的路径")
	}
	// macOS 上临时目录可能是 /var -> /private/var 的符号链接，比较时统一求值。
	wantResolved, _ := filepath.EvalSymlinks(fake)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("应优先使用环境变量指定的路径: got %q, want %q", got, fake)
	}

	// 指定了不存在的路径时不静默回退（否则用户会以为配置生效了）。
	t.Setenv(envYtDlpPath, filepath.Join(dir, "not-exist"))
	if _, ok := ResolveTool("yt-dlp"); ok {
		t.Error("显式指定不存在的路径时不应回退到自动搜索")
	}
}

// TestResolveToolRejectsDirectory 确认不会把目录当成可执行文件。
func TestResolveToolRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envYtDlpPath, dir)
	if _, ok := ResolveTool("yt-dlp"); ok {
		t.Error("目录不应被当作可执行文件")
	}
}

// TestMissingToolErrorIsActionable 确认缺失提示说明了搜索范围与解决办法，
// 避免用户误判为「没安装」而重复安装。
func TestMissingToolErrorIsActionable(t *testing.T) {
	msg := MissingToolError("yt-dlp").Error()
	if !strings.Contains(msg, "yt-dlp") {
		t.Errorf("应包含工具名: %q", msg)
	}
	// 必须说明搜索范围，让用户知道不是「没装」而是「没找到」。
	if !strings.Contains(msg, "搜索") {
		t.Errorf("应说明已搜索的位置: %q", msg)
	}
	// 必须给出环境变量覆盖办法。
	if !strings.Contains(msg, envYtDlpPath) {
		t.Errorf("应提示环境变量覆盖方式: %q", msg)
	}
	// 应给出安装方式。
	if !strings.Contains(msg, "install") {
		t.Errorf("应给出安装提示: %q", msg)
	}
	t.Logf("缺失提示: %s", msg)
}

// TestResolveToolUnknownName 覆盖未登记的工具名（无覆盖变量与安装提示）。
func TestResolveToolUnknownName(t *testing.T) {
	t.Setenv("PATH", minimalGUIPath)
	if _, ok := ResolveTool("definitely-not-a-real-tool-xyz"); ok {
		t.Error("不存在的工具不应被解析到")
	}
	msg := MissingToolError("definitely-not-a-real-tool-xyz").Error()
	if !strings.Contains(msg, "definitely-not-a-real-tool-xyz") {
		t.Errorf("提示应包含工具名: %q", msg)
	}
}

// TestToolStatusReportsEachTool 确认启动自检会逐个报告三个工具的解析结果，
// 且未找到时明确标注，而不是留空让人误以为没问题。
func TestToolStatusReportsEachTool(t *testing.T) {
	t.Setenv("PATH", minimalGUIPath)
	t.Setenv(envYtDlpPath, "")
	t.Setenv(envFFmpegPath, "")
	t.Setenv(envFFprobePath, "")

	got := ToolStatus()
	t.Logf("启动自检输出: %s", got)

	for _, name := range []string{"yt-dlp", "ffmpeg", "ffprobe"} {
		if !strings.Contains(got, name+"=") {
			t.Errorf("应包含 %s 的解析结果: %q", name, got)
		}
	}
	// 恰好报告三个工具，避免拼装时漏项。
	if n := strings.Count(got, "="); n != 3 {
		t.Errorf("应恰好报告三个工具，实际 %d 项: %q", n, got)
	}
	// 本机装了工具时，应给出路径而不是「未找到」。
	for _, name := range []string{"yt-dlp", "ffmpeg", "ffprobe"} {
		if _, ok := ResolveTool(name); !ok {
			continue
		}
		if strings.Contains(got, name+"=未找到") {
			t.Errorf("%s 可解析，但自检报未找到: %q", name, got)
		}
	}
}

// containsString 判断切片是否包含指定元素。
func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestBundledToolDirFrom 模拟 .app 目录布局，自带目录存在即命中、否则为空。
func TestBundledToolDirFrom(t *testing.T) {
	root := t.TempDir()
	macOS := filepath.Join(root, "AnyDLNA.app", "Contents", "MacOS")
	resTools := filepath.Join(root, "AnyDLNA.app", "Contents", "Resources", "tools")
	if err := os.MkdirAll(macOS, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(macOS, "any_dlna")
	if got := bundledToolDirFrom(exe); got != "" {
		t.Fatalf("目录不存在时应返回空，实际 %q", got)
	}
	if err := os.MkdirAll(resTools, 0o755); err != nil {
		t.Fatal(err)
	}
	got := bundledToolDirFrom(exe)
	want, _ := filepath.EvalSymlinks(resTools)
	gotEval, _ := filepath.EvalSymlinks(got)
	if gotEval != want {
		t.Fatalf("应定位到 Resources/tools: got %q want %q", got, resTools)
	}
}

// TestBundledJSRuntimeArgs 无自带目录时不加 flag（开发环境行为不变）。
func TestBundledJSRuntimeArgs(t *testing.T) {
	if bundledToolDir() != "" {
		t.Skip("自带目录存在，跳过缺省断言")
	}
	if args := bundledJSRuntimeArgs(); len(args) != 0 {
		t.Fatalf("无自带 qjs 时不应加参数: %v", args)
	}
}
