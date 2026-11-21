package media

import (
	"strings"
)

// findBestAudioMapFromOutput 从 ffmpeg 文本输出中识别音频轨，优先命中中文/国语配音，无中文降级首路音频。
func findBestAudioMapFromOutput(output string) int {
	lines := strings.Split(output, "\n")
	type audioEntry struct {
		index    int
		language string
		title    string
	}

	var audios []audioEntry
	var currentAudio *audioEntry

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// 检测 Stream 行
		if strings.HasPrefix(trimmed, "Stream #") {
			if strings.Contains(trimmed, "Audio:") {
				lang := ""
				if openParen := strings.Index(trimmed, "("); openParen != -1 {
					if colon := strings.Index(trimmed, "):"); colon != -1 && colon > openParen {
						lang = strings.ToLower(strings.TrimSpace(trimmed[openParen+1 : colon]))
					}
				}
				audios = append(audios, audioEntry{
					index:    len(audios),
					language: lang,
				})
				currentAudio = &audios[len(audios)-1]
			} else {
				currentAudio = nil
			}
			continue
		}

		// 提取 audio metadata 中的 title
		if currentAudio != nil && strings.HasPrefix(trimmed, "title") {
			if colon := strings.Index(trimmed, ":"); colon != -1 {
				currentAudio.title = strings.ToLower(strings.TrimSpace(trimmed[colon+1:]))
			}
		}
	}

	if len(audios) == 0 {
		return 0
	}

	// 优先寻找带有中文特征的音轨（国语/普通话/粤语配音）
	for _, a := range audios {
		isChinese := false
		switch a.language {
		case "chi", "zho", "chs", "cht", "zh", "cmn", "yue", "mandarin", "cantonese":
			isChinese = true
		}
		if !isChinese && a.title != "" {
			if strings.Contains(a.title, "中") ||
				strings.Contains(a.title, "国语") ||
				strings.Contains(a.title, "普通话") ||
				strings.Contains(a.title, "粤语") ||
				strings.Contains(a.title, "配音") ||
				strings.Contains(a.title, "chinese") ||
				strings.Contains(a.title, "mandarin") ||
				strings.Contains(a.title, "cantonese") {
				isChinese = true
			}
		}
		if isChinese {
			return a.index
		}
	}

	return 0
}
