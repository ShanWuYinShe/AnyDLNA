package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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

// ExtractEmbeddedSubtitle 尝试从媒体文件中提取最佳内嵌文本字幕并保存至临时缓存。
// 优先选择中文字幕（按语言代码 chi/zho/chs/cht 或标题标识匹配）；
// 无中文时降级选用首路字幕；若无内嵌字幕或提取失败则返回空串与错误。
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

	bestMap := "0:s:0"
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	if m := findBestSubtitleMap(probeCtx, ffmpegPath, videoPath); m != "" {
		bestMap = m
	}
	cancel()

	// 结合路径、大小、修改时间与字幕流映射计算哈希，避免重复提取
	key := fmt.Sprintf("%s|%d|%d|%s", videoPath, st.Size(), st.ModTime().UnixNano(), bestMap)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))[:16]
	destPath := filepath.Join(cacheDir, filepath.Base(videoPath)+"."+hash+".srt")

	if fi, err := os.Stat(destPath); err == nil && fi.Size() > 0 {
		return destPath, nil
	}

	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", videoPath,
		"-map", bestMap,
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

// findBestSubtitleMap 解析 ffmpeg -i 的流元数据输出，寻找最适合的中文字幕流（0:s:N）。
func findBestSubtitleMap(ctx context.Context, ffmpegPath, videoPath string) string {
	cmd := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-i", videoPath)
	out, _ := cmd.CombinedOutput()
	return findBestSubtitleMapFromOutput(string(out))
}

// findBestSubtitleMapFromOutput 从 ffmpeg 文本输出中识别字幕轨，优先命中中文，无中文降级首路字幕。
func findBestSubtitleMapFromOutput(output string) string {
	lines := strings.Split(output, "\n")
	type subEntry struct {
		index    int
		language string
		title    string
	}

	var subs []subEntry
	var currentSub *subEntry

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// 检测 Stream 行
		if strings.HasPrefix(trimmed, "Stream #") {
			if strings.Contains(trimmed, "Subtitle:") {
				lang := ""
				if openParen := strings.Index(trimmed, "("); openParen != -1 {
					if colon := strings.Index(trimmed, "):"); colon != -1 && colon > openParen {
						lang = strings.ToLower(strings.TrimSpace(trimmed[openParen+1 : colon]))
					}
				}
				subs = append(subs, subEntry{
					index:    len(subs),
					language: lang,
				})
				currentSub = &subs[len(subs)-1]
			} else {
				currentSub = nil
			}
			continue
		}

		// 提取 subtitle metadata 中的 title
		if currentSub != nil && strings.HasPrefix(trimmed, "title") {
			if colon := strings.Index(trimmed, ":"); colon != -1 {
				currentSub.title = strings.ToLower(strings.TrimSpace(trimmed[colon+1:]))
			}
		}
	}

	if len(subs) == 0 {
		return ""
	}

	// 优先寻找带有中文特征的字幕流
	for _, sub := range subs {
		isChinese := false
		switch sub.language {
		case "chi", "zho", "chs", "cht", "zh", "cmn":
			isChinese = true
		}
		if !isChinese && sub.title != "" {
			if strings.Contains(sub.title, "中") ||
				strings.Contains(sub.title, "简") ||
				strings.Contains(sub.title, "繁") ||
				strings.Contains(sub.title, "chinese") ||
				strings.Contains(sub.title, "chs") ||
				strings.Contains(sub.title, "cht") {
				isChinese = true
			}
		}
		if isChinese {
			return fmt.Sprintf("0:s:%d", sub.index)
		}
	}

	return "0:s:0"
}

// NormalizeSubtitleEncoding 检测并归一化字幕文件字符编码为纯 UTF-8，
// 自动剥离 UTF-8 BOM，并将 GBK / GB2312 / GB18030 等中文常见编码转码为 UTF-8。
// 彻底杜绝三星、LG、索尼等智能电视因字符集不匹配导致的字幕乱码。
func NormalizeSubtitleEncoding(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	// 1. 若带 UTF-8 BOM（\xef\xbb\xbf），剥离之（部分电视遇到 BOM 会报解析错误或首行显示乱码字符）
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	// 2. 检查是否为有效 UTF-8
	if utf8.Valid(data) {
		return data
	}

	// 3. 非 UTF-8，尝试按 GBK / GB18030 转码
	reader := transform.NewReader(bytes.NewReader(data), simplifiedchinese.GB18030.NewDecoder())
	decoded, err := io.ReadAll(reader)
	if err == nil && len(decoded) > 0 && utf8.Valid(decoded) {
		return decoded
	}

	// 转码失败或未知编码时回退原数据
	return data
}

// ConvertASSToSRT 把 ASS / SSA 特效字幕解析并转换为所有电视均可兼容的标准 SRT 文本。
func ConvertASSToSRT(assData []byte) []byte {
	lines := strings.Split(string(assData), "\n")
	var srtBuf strings.Builder
	idx := 1

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), "dialogue:") {
			continue
		}
		// Dialogue: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		content := strings.TrimSpace(line[colonIdx+1:])
		parts := strings.SplitN(content, ",", 10)
		if len(parts) < 10 {
			continue
		}
		start := formatASSTimeToSRT(parts[1])
		end := formatASSTimeToSRT(parts[2])
		text := stripASSTags(parts[9])
		if text == "" {
			continue
		}

		fmt.Fprintf(&srtBuf, "%d\n%s --> %s\n%s\n\n", idx, start, end, text)
		idx++
	}

	if srtBuf.Len() == 0 {
		return nil
	}
	return []byte(srtBuf.String())
}

// formatASSTimeToSRT 将 ASS 时间 "0:01:20.12" 格式化为 SRT 时间 "00:01:20,120"
func formatASSTimeToSRT(t string) string {
	t = strings.TrimSpace(t)
	parts := strings.Split(t, ":")
	if len(parts) != 3 {
		return "00:00:00,000"
	}
	h, errH := strconv.Atoi(parts[0])
	m, errM := strconv.Atoi(parts[1])
	secParts := strings.Split(parts[2], ".")
	s, errS := strconv.Atoi(secParts[0])
	ms := 0
	if len(secParts) > 1 {
		frac := secParts[1]
		if len(frac) == 1 {
			v, _ := strconv.Atoi(frac)
			ms = v * 100
		} else if len(frac) == 2 {
			v, _ := strconv.Atoi(frac)
			ms = v * 10
		} else if len(frac) >= 3 {
			ms, _ = strconv.Atoi(frac[:3])
		}
	}
	if errH != nil || errM != nil || errS != nil {
		return "00:00:00,000"
	}
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

// stripASSTags 去除 ASS 格式样式标签（如 {\pos(100,200)}），并替换换行符
func stripASSTags(text string) string {
	var b strings.Builder
	inTag := false
	for _, r := range text {
		if r == '{' {
			inTag = true
			continue
		}
		if r == '}' {
			inTag = false
			continue
		}
		if !inTag {
			b.WriteRune(r)
		}
	}
	s := b.String()
	s = strings.ReplaceAll(s, `\N`, "\n")
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.ReplaceAll(s, `\h`, " ")
	return strings.TrimSpace(s)
}
