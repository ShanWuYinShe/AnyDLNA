// 摄像头实时采集源：把本机摄像头（可选麦克风）经 avfoundation 实时采集，
// 走与在线视频相同的转码管线（H.264/AAC → MPEG-TS/碎片化 MP4）投到电视。
// 目前仅支持 macOS；Windows（dshow）/ Linux（v4l2）待后续扩展。
package media

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// CameraDevice 是检测到的一路采集设备（摄像头或麦克风）。
type CameraDevice struct {
	Kind  string `json:"kind"`  // video / audio
	Index string `json:"index"` // avfoundation 设备序号（如 "0"）
	Name  string `json:"name"`
}

// CameraSource 描述一路摄像头实时采集输入。
type CameraSource struct {
	VideoDevice string // 摄像头设备序号，如 "0"
	AudioDevice string // 麦克风设备序号；空为不采集声音
}

// ListCameras 列出本机可用的采集设备（摄像头与麦克风）。
// 原理：ffmpeg -list_devices 会把设备清单打到 stderr 后以非零码退出
// （无输出文件所致），因此启动错误与预期退出要区别对待。
// 注意：首次调用会触发 macOS 的摄像头/麦克风权限弹窗，需用户允许。
func ListCameras(ctx context.Context) ([]CameraDevice, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("摄像头采集暂仅支持 macOS（当前 %s）", runtime.GOOS)
	}
	cmd := toolCmdContext(ctx, "ffmpeg", "-hide_banner",
		"-f", "avfoundation", "-list_devices", "true", "-i", "")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr != nil {
		if _, isExit := runErr.(*exec.ExitError); !isExit {
			// 不是预期的「无输出文件」退出，而是 ffmpeg 本身没跑起来。
			return nil, fmt.Errorf("检测采集设备失败（需已安装 ffmpeg）: %w", runErr)
		}
	}
	devs := parseAVFoundationDevices(stderr.String())
	if len(devs) == 0 && strings.TrimSpace(stderr.String()) == "" {
		return nil, fmt.Errorf("检测采集设备失败：ffmpeg 无输出（请确认已安装 ffmpeg）")
	}
	return devs, nil
}

// deviceLineRe 匹配设备条目行末尾的「[N] 设备名」。
// 行前缀形如 "[AVFoundation indev @ 0x...] "，设备名本身可能含 "]"，
// 因此只锚定行尾。
var deviceLineRe = regexp.MustCompile(`\[(\d+)\]\s+(.+)$`)

// parseAVFoundationDevices 解析 ffmpeg -list_devices 的 stderr：
//
//	[AVFoundation indev @ ...] AVFoundation video devices (some may be both video and audio devices):
//	[AVFoundation indev @ ...] [0] FaceTime HD Camera
//	[AVFoundation indev @ ...] AVFoundation audio devices:
//	[AVFoundation indev @ ...] [0] MacBook Pro Microphone
//
// 段落头决定后续条目归类；"Capture screen" 是屏幕捕获设备，不属于摄像头。
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
		if strings.HasPrefix(name, "Capture screen") {
			continue
		}
		devs = append(devs, CameraDevice{Kind: kind, Index: m[1], Name: name})
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

// cameraInputArgs 构造 avfoundation 采集的输入参数：
// 720p30 采集（实时转码的编码开销与清晰度的折中，见 outputArgs）。
func cameraInputArgs(c *CameraSource) []string {
	return []string{
		"-f", "avfoundation",
		"-framerate", "30",
		"-video_size", "1280x720",
		"-i", cameraInput(c),
	}
}

// CameraLabel 返回会话诊断用的设备描述。
func CameraLabel(videoDevice, audioDevice string) string {
	if audioDevice != "" {
		return fmt.Sprintf("video=%s audio=%s", videoDevice, audioDevice)
	}
	return "video=" + videoDevice
}
