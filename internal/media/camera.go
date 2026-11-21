// 摄像头/屏幕实时采集源：把本机摄像头或屏幕画面（可选麦克风）实时采集，
// 走与在线视频相同的转码管线（H.264/AAC → MPEG-TS/碎片化 MP4）投到电视。
// 跨平台适配：macOS（avfoundation）、Windows（dshow / gdigrab）、Linux（v4l2 / x11grab）。
package media

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// CameraDevice 是检测到的一路采集设备（摄像头、麦克风或屏幕）。
type CameraDevice struct {
	Kind  string `json:"kind"`  // video / audio
	Index string `json:"index"` // 设备标识（macOS: 序号如 "0"；Windows: 设备名称或 "desktop"；Linux: "/dev/video0" 或 ":0.0"）
	Name  string `json:"name"`
	// Screen 表示该视频设备是屏幕捕获设备（采集参数与摄像头不同，
	// 见 cameraInputArgs）。
	Screen bool `json:"screen,omitempty"`
}

// CameraSource 描述一路实时采集输入（摄像头或屏幕）。
type CameraSource struct {
	VideoDevice string // 设备标识或序号
	AudioDevice string // 麦克风设备标识或序号；空为不采集声音
	Screen      bool   // 采集对象是屏幕而非摄像头
}

// ListCameras 列出本机可用的采集设备（摄像头、麦克风与屏幕）。
// 委托给当前系统的 ListCamerasForOS。
func ListCameras(ctx context.Context) ([]CameraDevice, error) {
	return ListCamerasForOS(ctx, runtime.GOOS)
}

// ListCamerasForOS 针对指定平台列出可用的采集设备。
// 原理：ffmpeg -list_devices 会把设备清单打到 stderr 后以非零码退出
// （无输出文件所致），因此启动错误与预期退出要区别对待。
func ListCamerasForOS(ctx context.Context, goos string) ([]CameraDevice, error) {
	switch goos {
	case "darwin":
		cmd := toolCmdContext(ctx, "ffmpeg", "-hide_banner",
			"-f", "avfoundation", "-list_devices", "true", "-i", "")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		if runErr != nil {
			if _, isExit := runErr.(*exec.ExitError); !isExit {
				return nil, fmt.Errorf("检测采集设备失败（需已安装 ffmpeg）: %w", runErr)
			}
		}
		devs := parseAVFoundationDevices(stderr.String())
		if len(devs) == 0 && strings.TrimSpace(stderr.String()) == "" {
			return nil, fmt.Errorf("检测采集设备失败：ffmpeg 无输出（请确认已安装 ffmpeg）")
		}
		return devs, nil

	case "windows":
		cmd := toolCmdContext(ctx, "ffmpeg", "-hide_banner",
			"-f", "dshow", "-list_devices", "true", "-i", "dummy")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		if runErr != nil {
			if _, isExit := runErr.(*exec.ExitError); !isExit {
				return nil, fmt.Errorf("检测采集设备失败（需已安装 ffmpeg）: %w", runErr)
			}
		}
		devs := parseDShowDevices(stderr.String())
		// Windows 原生通过 gdigrab 支持全屏桌面捕获，注入虚拟屏幕条目
		hasScreen := false
		for _, d := range devs {
			if d.Screen {
				hasScreen = true
				break
			}
		}
		if !hasScreen {
			devs = append([]CameraDevice{
				{
					Kind:   "video",
					Index:  "desktop",
					Name:   "桌面全屏捕获 (Desktop Screen)",
					Screen: true,
				},
			}, devs...)
		}
		return devs, nil

	case "linux":
		var devs []CameraDevice
		// 检查 X11 桌面显示
		if disp := os.Getenv("DISPLAY"); disp != "" {
			devs = append(devs, CameraDevice{
				Kind:   "video",
				Index:  disp,
				Name:   fmt.Sprintf("X11 屏幕捕获 (Display %s)", disp),
				Screen: true,
			})
		}
		// 扫描 Linux V4L2 摄像头设备节点
		v4lDevs := findLinuxV4L2Devices()
		devs = append(devs, v4lDevs...)
		return devs, nil

	default:
		return nil, fmt.Errorf("当前操作系统 (%s) 暂不支持摄像头采集", goos)
	}
}

// deviceLineRe 匹配 macOS AVFoundation 设备条目行末尾的「[N] 设备名」。
var deviceLineRe = regexp.MustCompile(`\[(\d+)\]\s+(.+)$`)

// parseAVFoundationDevices 解析 macOS ffmpeg -list_devices 的 stderr
func parseAVFoundationDevices(out string) []CameraDevice {
	var (
		devs []CameraDevice
		kind string
	)
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "AVFoundation") {
			continue
		}
		switch {
		case strings.Contains(line, "video devices"):
			kind = "video"
			continue
		case strings.Contains(line, "audio devices"):
			kind = "audio"
			continue
		}
		m := deviceLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || kind == "" {
			continue
		}
		name := strings.TrimSpace(m[2])
		dev := CameraDevice{Kind: kind, Index: m[1], Name: name, Screen: strings.HasPrefix(name, "Capture screen")}
		devs = append(devs, dev)
	}
	return devs
}

// dshowQuoteRe 匹配 Windows DirectShow 设备名称引号内容。
var dshowQuoteRe = regexp.MustCompile(`"([^"]+)"`)

// parseDShowDevices 解析 Windows ffmpeg -list_devices 的 DirectShow 输出。
func parseDShowDevices(out string) []CameraDevice {
	var (
		devs []CameraDevice
		kind string
	)
	for _, rawLine := range strings.Split(out, "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.Contains(strings.ToLower(line), "dshow") {
			continue
		}
		switch {
		case strings.Contains(line, "video devices"):
			kind = "video"
			continue
		case strings.Contains(line, "audio devices"):
			kind = "audio"
			continue
		}
		// 忽略别名行（如 Alternative name "@device_pnp..."）
		if strings.Contains(line, "Alternative name") {
			continue
		}
		m := dshowQuoteRe.FindStringSubmatch(line)
		if m == nil || kind == "" {
			continue
		}
		name := strings.TrimSpace(m[1])
		lowerName := strings.ToLower(name)
		isScreen := strings.Contains(lowerName, "screen") || strings.Contains(lowerName, "desktop")
		dev := CameraDevice{
			Kind:   kind,
			Index:  name,
			Name:   name,
			Screen: isScreen,
		}
		devs = append(devs, dev)
	}
	return devs
}

// findLinuxV4L2Devices 查找 Linux 上的 V4L2 摄像头设备节点。
func findLinuxV4L2Devices() []CameraDevice {
	var devs []CameraDevice
	matches, err := filepath.Glob("/dev/video*")
	if err != nil {
		return devs
	}
	for _, m := range matches {
		devs = append(devs, CameraDevice{
			Kind:   "video",
			Index:  m,
			Name:   fmt.Sprintf("V4L2 摄像头 (%s)", filepath.Base(m)),
			Screen: false,
		})
	}
	return devs
}

// cameraInput 拼装 avfoundation 的 -i 设备串："视频序号" 或 "视频:音频"。
func cameraInput(c *CameraSource) string {
	if c.AudioDevice != "" {
		return c.VideoDevice + ":" + c.AudioDevice
	}
	return c.VideoDevice
}

// cameraInputArgs 构造当前操作系统采集的输入参数。
func cameraInputArgs(c *CameraSource) []string {
	return cameraInputArgsForOS(c, runtime.GOOS)
}

// cameraInputArgsForOS 根据指定操作系统构造采集输入参数。
// 摄像头固定 720p30 采集；屏幕设备按原生分辨率采集并用 scale 滤镜降到 720p 偶数尺寸。
func cameraInputArgsForOS(c *CameraSource, goos string) []string {
	switch goos {
	case "windows":
		if c.Screen || c.VideoDevice == "desktop" {
			args := []string{
				"-f", "gdigrab",
				"-framerate", "30",
				"-draw_mouse", "1",
				"-i", "desktop",
			}
			if c.AudioDevice != "" {
				args = append(args, "-f", "dshow", "-i", "audio="+c.AudioDevice)
			}
			args = append(args, "-vf", "scale=w=1280:h=720:force_original_aspect_ratio=decrease:force_divisible_by=2")
			return args
		}
		dshowInput := "video=" + c.VideoDevice
		if c.AudioDevice != "" {
			dshowInput += ":audio=" + c.AudioDevice
		}
		return []string{
			"-f", "dshow",
			"-framerate", "30",
			"-video_size", "1280x720",
			"-i", dshowInput,
		}

	case "linux":
		if c.Screen {
			disp := c.VideoDevice
			if disp == "" {
				disp = ":0.0"
			}
			args := []string{
				"-f", "x11grab",
				"-framerate", "30",
				"-draw_mouse", "1",
				"-i", disp,
			}
			if c.AudioDevice != "" {
				args = append(args, "-f", "pulse", "-i", c.AudioDevice)
			}
			args = append(args, "-vf", "scale=w=1280:h=720:force_original_aspect_ratio=decrease:force_divisible_by=2")
			return args
		}
		dev := c.VideoDevice
		if dev == "" {
			dev = "/dev/video0"
		}
		args := []string{
			"-f", "v4l2",
			"-framerate", "30",
			"-video_size", "1280x720",
			"-i", dev,
		}
		if c.AudioDevice != "" {
			args = append(args, "-f", "pulse", "-i", c.AudioDevice)
		}
		return args

	default: // 默认走 macOS (avfoundation)
		if c.Screen {
			return append([]string{
				"-f", "avfoundation",
				"-framerate", "30",
				"-capture_cursor", "1",
				"-i", cameraInput(c),
			}, "-vf", "scale=w=1280:h=720:force_original_aspect_ratio=decrease:force_divisible_by=2")
		}
		return []string{
			"-f", "avfoundation",
			"-framerate", "30",
			"-video_size", "1280x720",
			"-i", cameraInput(c),
		}
	}
}

// CameraLabel 返回会话诊断用的设备描述。
func CameraLabel(videoDevice, audioDevice string) string {
	if audioDevice != "" {
		return fmt.Sprintf("video=%s audio=%s", videoDevice, audioDevice)
	}
	return "video=" + videoDevice
}
