package media

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config 是应用的可持久化配置。
type Config struct {
	Proxy         string `json:"proxy"`          // 在线视频访问代理，如 http://127.0.0.1:10809；空为直连
	CookieBrowser string `json:"cookie_browser"` // yt-dlp 读取登录态的浏览器（chrome/safari/firefox/edge）；空为不使用
}

// ConfigPath 返回配置文件路径（随系统用户配置目录）。
func ConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "AnyDLNA", "config.json"), nil
}

// LoadConfig 读取配置；文件不存在或损坏时返回零值配置，不阻塞启动。
func LoadConfig() Config {
	var c Config
	path, err := ConfigPath()
	if err != nil {
		return c
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	_ = json.Unmarshal(data, &c)
	return c
}

// SaveConfig 写入配置文件。
func SaveConfig(c Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
