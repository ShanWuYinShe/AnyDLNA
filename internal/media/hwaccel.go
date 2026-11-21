package media

import (
	"context"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// VideoEncoderConfig 描述一种视频编码器及其参数配置与可用性探测参数。
type VideoEncoderConfig struct {
	Name      string   // 编码器名称（如 "h264_videotoolbox", "h264_nvenc"）
	Args      []string // 实际转码时注入的完整视频编码与码率参数
	ProbeArgs []string // 用于快速测试编码器是否能在本机硬件上正常初始化的最小测试参数
}

var (
	hwMu            sync.Mutex
	cachedEncoder   *VideoEncoderConfig
	encoderSelected bool

	// customEncoderProber 供单测注入 mock 探测逻辑；为 nil 时执行真实的 probeEncoderCmd
	customEncoderProber func(ctx context.Context, ffmpegPath string, cfg *VideoEncoderConfig) bool
)

// ResetHardwareEncoderCache 重置编码器缓存（供单测使用）。
func ResetHardwareEncoderCache() {
	hwMu.Lock()
	defer hwMu.Unlock()
	cachedEncoder = nil
	encoderSelected = false
}

// SetCustomEncoderProber 设置自定义编码器探测器（供单测使用）。
func SetCustomEncoderProber(prober func(ctx context.Context, ffmpegPath string, cfg *VideoEncoderConfig) bool) {
	hwMu.Lock()
	defer hwMu.Unlock()
	customEncoderProber = prober
	cachedEncoder = nil
	encoderSelected = false
}

// DefaultCPUEncoderConfig 返回通用的 CPU 软编配置（libx264），在没有硬件加速或自检失败时兜底。
func DefaultCPUEncoderConfig() *VideoEncoderConfig {
	return &VideoEncoderConfig{
		Name: "libx264",
		Args: []string{
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
			"-maxrate", "4M", "-bufsize", "8M",
			"-g", "60",
			"-pix_fmt", "yuv420p",
		},
		ProbeArgs: []string{"-c:v", "libx264"},
	}
}

// GetCandidateEncodersForOS 返回指定操作系统下按推荐优先级排列的硬件加速编码器候选列表。
func GetCandidateEncodersForOS(goos string) []*VideoEncoderConfig {
	switch goos {
	case "darwin":
		return []*VideoEncoderConfig{
			{
				Name: "h264_videotoolbox",
				Args: []string{
					"-c:v", "h264_videotoolbox",
					"-b:v", "6M", "-maxrate", "10M", "-bufsize", "12M",
					"-g", "60",
					"-pix_fmt", "yuv420p",
				},
				ProbeArgs: []string{"-c:v", "h264_videotoolbox"},
			},
		}
	case "windows":
		return []*VideoEncoderConfig{
			// 1. NVIDIA NVENC (GeForce / Quadro / Tesla)
			{
				Name: "h264_nvenc",
				Args: []string{
					"-c:v", "h264_nvenc",
					"-preset", "p4", "-tune", "ll", "-rc", "vbr", "-cq", "23",
					"-b:v", "6M", "-maxrate", "10M", "-bufsize", "12M",
					"-g", "60",
					"-pix_fmt", "yuv420p",
				},
				ProbeArgs: []string{"-c:v", "h264_nvenc"},
			},
			// 2. Intel QuickSync Video (Core / Xeon iGPU)
			{
				Name: "h264_qsv",
				Args: []string{
					"-c:v", "h264_qsv",
					"-preset", "veryfast", "-global_quality", "23",
					"-b:v", "6M", "-maxrate", "10M", "-bufsize", "12M",
					"-g", "60",
					"-pix_fmt", "yuv420p",
				},
				ProbeArgs: []string{"-c:v", "h264_qsv"},
			},
			// 3. AMD AMF (Radeon GPU / APU)
			{
				Name: "h264_amf",
				Args: []string{
					"-c:v", "h264_amf",
					"-quality", "speed", "-rc", "cbr",
					"-b:v", "6M", "-maxrate", "10M", "-bufsize", "12M",
					"-g", "60",
					"-pix_fmt", "yuv420p",
				},
				ProbeArgs: []string{"-c:v", "h264_amf"},
			},
			// 4. Windows Media Foundation (Direct3D 11 / Media Foundation)
			{
				Name: "h264_mf",
				Args: []string{
					"-c:v", "h264_mf",
					"-b:v", "6M", "-maxrate", "10M", "-bufsize", "12M",
					"-g", "60",
					"-pix_fmt", "yuv420p",
				},
				ProbeArgs: []string{"-c:v", "h264_mf"},
			},
		}
	case "linux":
		return []*VideoEncoderConfig{
			// 1. NVIDIA NVENC (Linux 闭源驱动下广泛使用)
			{
				Name: "h264_nvenc",
				Args: []string{
					"-c:v", "h264_nvenc",
					"-preset", "p4", "-tune", "ll", "-rc", "vbr", "-cq", "23",
					"-b:v", "6M", "-maxrate", "10M", "-bufsize", "12M",
					"-g", "60",
					"-pix_fmt", "yuv420p",
				},
				ProbeArgs: []string{"-c:v", "h264_nvenc"},
			},
			// 2. Intel QuickSync (QSV)
			{
				Name: "h264_qsv",
				Args: []string{
					"-c:v", "h264_qsv",
					"-preset", "veryfast", "-global_quality", "23",
					"-b:v", "6M", "-maxrate", "10M", "-bufsize", "12M",
					"-g", "60",
					"-pix_fmt", "yuv420p",
				},
				ProbeArgs: []string{"-c:v", "h264_qsv"},
			},
			// 3. VA-API (Intel/AMD 开源核显与显卡)
			{
				Name: "h264_vaapi",
				Args: []string{
					"-c:v", "h264_vaapi",
					"-b:v", "6M", "-maxrate", "10M", "-bufsize", "12M",
					"-g", "60",
				},
				ProbeArgs: []string{"-c:v", "h264_vaapi"},
			},
		}
	default:
		return nil
	}
}

// probeEncoderCmd 实际执行短帧测试，验证编码器是否能在本机硬件上正常编码。
func probeEncoderCmd(ctx context.Context, ffmpegPath string, cfg *VideoEncoderConfig) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// 使用 lavfi 生成极小虚拟帧 (64x64, 0.04秒，约1-2帧)，测试硬件上下文创建与帧编码
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "nullsrc=s=64x64:d=0.04",
	}
	args = append(args, cfg.ProbeArgs...)
	args = append(args, "-f", "null", "-")

	cmd := exec.CommandContext(probeCtx, ffmpegPath, args...)
	return cmd.Run() == nil
}

// SelectVideoEncoder 探测并返回最适合当前系统的视频编码器配置。
// 检测结果在进程生命周期内缓存，避免每次投屏重复探测引起延迟。
func SelectVideoEncoder(goos string) *VideoEncoderConfig {
	hwMu.Lock()
	defer hwMu.Unlock()

	if encoderSelected {
		return cachedEncoder
	}

	ffmpegPath, ok := ResolveTool("ffmpeg")
	if !ok && customEncoderProber == nil {
		cachedEncoder = DefaultCPUEncoderConfig()
		encoderSelected = true
		return cachedEncoder
	}

	candidates := GetCandidateEncodersForOS(goos)
	ctx := context.Background()

	for _, cand := range candidates {
		var passed bool
		if customEncoderProber != nil {
			passed = customEncoderProber(ctx, ffmpegPath, cand)
		} else {
			passed = probeEncoderCmd(ctx, ffmpegPath, cand)
		}

		if passed {
			Diagf("启用硬件加速转码: %s", cand.Name)
			cachedEncoder = cand
			encoderSelected = true
			return cachedEncoder
		}
	}

	Diagf("未检测到可用硬件加速编码器，回退至 CPU 软件编码 (libx264)")
	cachedEncoder = DefaultCPUEncoderConfig()
	encoderSelected = true
	return cachedEncoder
}

// GetVideoEncoderArgs 返回当前最佳视频编码参数列表（硬件加速优先，安全降级软解软编）。
// 设置 -g 60 确保每 2 秒至少一个关键帧，电视端起播与跳转（Seek）后无需长时间等待 IDR 帧即可解码画面。
func GetVideoEncoderArgs() []string {
	enc := SelectVideoEncoder(runtime.GOOS)
	if enc == nil {
		return DefaultCPUEncoderConfig().Args
	}
	return enc.Args
}

// HwaccelDecoderArgs 返回输入端自适应硬件加速解码参数。
// 在视频需要转码（非 -c:v copy）时置于 ffmpeg 输入参数 -i 之前。
// ffmpeg 的 -hwaccel auto 会在 GPU 硬件及驱动支持当前视频源封装与像素格式时自动激活硬件解码；
// 若不支持或遇到特殊流，自动无缝降级为 CPU 软件解码，安全可靠。
func HwaccelDecoderArgs() []string {
	return []string{"-hwaccel", "auto"}
}
