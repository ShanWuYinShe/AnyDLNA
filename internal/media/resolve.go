package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

// Resolved 是 yt-dlp 解析在线视频得到的关键元数据。
type Resolved struct {
	Title       string  `json:"title"`
	DurationSec float64 `json:"durationSec"`
	IsLive      bool    `json:"isLive"`
	Extractor   string  `json:"extractor"`
	Uploader    string  `json:"uploader"`
}

// HasYtDlp 报告 yt-dlp 是否可用。
func HasYtDlp() bool {
	_, err := exec.LookPath("yt-dlp")
	return err == nil
}

// Resolve 用 yt-dlp 解析视频页面 URL，提取标题、时长与直播标记。
// 仅读取元数据（-J），不拉取媒体流。
func Resolve(ctx context.Context, url string) (*Resolved, error) {
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		return nil, fmt.Errorf("未找到 yt-dlp，请先安装：brew install yt-dlp")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "yt-dlp",
		"-J", "--no-playlist", "--no-warnings", url,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("解析视频失败（站点不支持或网络不可达）: %w", err)
	}

	var raw struct {
		Title     string  `json:"title"`
		Duration  float64 `json:"duration"`
		IsLive    bool    `json:"is_live"`
		Extractor string  `json:"extractor_key"`
		Uploader  string  `json:"uploader"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("解析 yt-dlp 输出失败: %w", err)
	}
	return &Resolved{
		Title:       raw.Title,
		DurationSec: raw.Duration,
		IsLive:      raw.IsLive,
		Extractor:   raw.Extractor,
		Uploader:    raw.Uploader,
	}, nil
}

// ytDlpStreamArgs 构造把在线视频（已合并音视频）写到 stdout 的 yt-dlp 参数。
// startSec>0 且非直播时用 --download-sections 实现快进到指定位置。
func ytDlpStreamArgs(url string, startSec float64, isLive bool) []string {
	args := []string{"-q", "--no-playlist", "--no-warnings"}
	if startSec > 0 && !isLive {
		args = append(args, "--download-sections", "*"+strconv.FormatFloat(startSec, 'f', 2, 64)+"-inf")
	}
	return append(args, "-o", "-", url)
}
