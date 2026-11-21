package media

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// subtitleExtensions 支持的外挂字幕后缀及优先级。
var subtitleExtensions = []string{
	".chs.srt", ".sc.srt", ".zh.srt", ".cn.srt", ".zh-cn.srt", ".zh-hans.srt",
	".cht.srt", ".tc.srt", ".zh-tw.srt", ".zh-hant.srt",
	".srt",
	".chs.vtt", ".sc.vtt", ".zh.vtt", ".vtt",
	".chs.ass", ".sc.ass", ".zh.ass", ".ass",
	".chs.ssa", ".ssa",
	".en.srt", ".eng.srt",
}

// FindExternalSubtitle 查找与媒体文件同名（或同前缀）的外挂字幕。
// 返回找到的字幕绝对路径，未找到时返回空串。
func FindExternalSubtitle(videoPath string) string {
	videoPath = strings.TrimSpace(videoPath)
	if videoPath == "" {
		return ""
	}
	dir := filepath.Dir(videoPath)
	ext := filepath.Ext(videoPath)
	base := strings.TrimSuffix(filepath.Base(videoPath), ext)

	// 1. 优先按已知扩展名顺序在同目录下精确探测
	for _, subExt := range subtitleExtensions {
		candidate := filepath.Join(dir, base+subExt)
		if isRegularFile(candidate) {
			return candidate
		}
	}

	// 2. 检查子目录 subs/ 或 subtitles/
	for _, subDir := range []string{"subs", "subtitles", "Subs", "Subtitles"} {
		for _, subExt := range subtitleExtensions {
			candidate := filepath.Join(dir, subDir, base+subExt)
			if isRegularFile(candidate) {
				return candidate
			}
		}
	}

	// 3. 扫描同目录下以 base 开头的所有字幕文件（模糊匹配，处理如 video.zh-CN.default.srt）
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if strings.HasPrefix(strings.ToLower(name), strings.ToLower(base)) {
				lowerName := strings.ToLower(name)
				if strings.HasSuffix(lowerName, ".srt") || strings.HasSuffix(lowerName, ".vtt") ||
					strings.HasSuffix(lowerName, ".ass") || strings.HasSuffix(lowerName, ".ssa") {
					return filepath.Join(dir, name)
				}
			}
		}
	}

	return ""
}

func isRegularFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() > 0
}

// ExtractEmbeddedSubtitle 尝试从媒体文件中提取第一路内嵌文本字幕并保存至临时缓存。
// 若无内嵌字幕或提取失败则返回空串与错误。
func ExtractEmbeddedSubtitle(ctx context.Context, videoPath string) (string, error) {
	videoPath = strings.TrimSpace(videoPath)
	if videoPath == "" {
		return "", errors.New("视频路径为空")
	}
	st, err := os.Stat(videoPath)
	if err != nil || st.IsDir() {
		return "", errors.New("视频文件不可读")
	}

	ffmpegPath, ok := ResolveTool("ffmpeg")
	if !ok {
		return "", errors.New("缺少 ffmpeg 工具")
	}

	cacheDir := filepath.Join(os.TempDir(), "anydlna_embedded_subs")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}

	// 结合路径、大小与修改时间计算哈希，避免重复提取
	key := fmt.Sprintf("%s|%d|%d", videoPath, st.Size(), st.ModTime().UnixNano())
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))[:16]
	destPath := filepath.Join(cacheDir, filepath.Base(videoPath)+"."+hash+".srt")

	if fi, err := os.Stat(destPath); err == nil && fi.Size() > 0 {
		return destPath, nil
	}

	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", videoPath,
		"-map", "0:s:0",
		"-c:s", "srt",
		destPath,
	)
	if err := cmd.Run(); err != nil {
		_ = os.Remove(destPath)
		return "", err
	}

	if fi, err := os.Stat(destPath); err == nil && fi.Size() > 0 {
		return destPath, nil
	}
	_ = os.Remove(destPath)
	return "", errors.New("提取字幕为空")
}
