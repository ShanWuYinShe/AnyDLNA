package media

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
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

// ytDlpCommonArgs 构造代理与 Cookie 来源相关的公共参数。
// proxy 非空时经代理访问；cookieBrowser 非空时读取该浏览器的登录态
// （YouTube 等站点对机房 IP 要求 bot 验证，需携带浏览器 Cookies）。
func ytDlpCommonArgs(proxy, cookieBrowser string) []string {
	var args []string
	if proxy != "" {
		args = append(args, "--proxy", proxy)
	}
	if cookieBrowser != "" {
		args = append(args, "--cookies-from-browser", cookieBrowser)
	}
	return args
}

// Resolve 用 yt-dlp 解析视频页面 URL，提取标题、时长与直播标记。
// 仅读取元数据（-J），不拉取媒体流；proxy/cookieBrowser 语义见 ytDlpCommonArgs。
// yt-dlp 的报错（如站点验证提示）会截取关键内容返回，便于前端直接展示。
func Resolve(ctx context.Context, url, proxy, cookieBrowser string) (*Resolved, error) {
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		return nil, fmt.Errorf("未找到 yt-dlp，请先安装：brew install yt-dlp")
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	args := append([]string{"-J", "--no-playlist", "--no-warnings"}, ytDlpCommonArgs(proxy, cookieBrowser)...)
	cmd := exec.CommandContext(ctx, "yt-dlp", append(args, url)...)
	var stderr limitBuffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := ytDlpErrTail(stderr.String()); msg != "" {
			return nil, fmt.Errorf("解析视频失败: %s", msg)
		}
		return nil, fmt.Errorf("解析视频失败（站点不支持、网络不可达或代理不可用）: %w", err)
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

// ytDlpErrTail 提取 yt-dlp 报错的最后几行（错误摘要在末尾），最长 300 字符。
func ytDlpErrTail(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	lines := strings.Split(stderr, "\n")
	if n := len(lines); n > 3 {
		lines = lines[n-3:]
	}
	tail := strings.Join(lines, " ")
	if len(tail) > 300 {
		tail = tail[len(tail)-300:]
	}
	return tail
}

// TestProxy 通过代理请求一个轻量连通性端点，验证代理配置是否可用。
func TestProxy(ctx context.Context, proxy string) error {
	if proxy == "" {
		return fmt.Errorf("未设置代理")
	}
	pu, err := url.Parse(proxy)
	if err != nil || pu.Host == "" {
		return fmt.Errorf("代理地址无效（示例：http://127.0.0.1:10809）")
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(pu), // 仅此请求走代理，不读环境变量
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.gstatic.com/generate_204", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("代理不可用: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("代理可用但出口异常: HTTP %d", resp.StatusCode)
	}
	return nil
}

// ytDlpStreamArgs 构造把在线视频（已合并音视频）写到 stdout 的 yt-dlp 参数。
// startSec>0 且非直播时用 --download-sections 实现快进到指定位置；
// proxy/cookieBrowser 语义见 ytDlpCommonArgs。
func ytDlpStreamArgs(url string, startSec float64, isLive bool, proxy, cookieBrowser string) []string {
	args := append([]string{"-q", "--no-playlist", "--no-warnings"}, ytDlpCommonArgs(proxy, cookieBrowser)...)
	if startSec > 0 && !isLive {
		args = append(args, "--download-sections", "*"+strconv.FormatFloat(startSec, 'f', 2, 64)+"-inf")
	}
	return append(args, "-o", "-", url)
}
