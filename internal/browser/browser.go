// Package browser 通过 Chrome DevTools Protocol（CDP）驱动本机已安装的
// Chromium 系浏览器，实现「在真实浏览器窗口中登录，再取回站点 Cookies」。
//
// 为什么这样设计：
//   - 纯 Go、无 cgo，各平台行为一致；
//   - 使用独立 profile 目录，绝不读写用户日常浏览器的数据；
//   - Cookie 由浏览器自身解密并通过 CDP 交出，因此能拿到 HttpOnly 条目
//     （YouTube 的 SID/HSID/__Secure-* 等登录态都是 HttpOnly，
//     前端 document.cookie 读不到）；
//   - 登录态保存在独立 profile 中，下次仍有效，无需重复登录。
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Cookie 是一条从浏览器读出的 Cookie。
type Cookie struct {
	Domain    string
	Path      string
	Name      string
	Value     string
	Secure    bool
	HttpOnly  bool
	ExpiresAt time.Time // 零值表示会话 Cookie
}

// Info 描述检测到的浏览器可执行文件。
type Info struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Manager 管理一个应用专用的浏览器实例（独立 profile 目录）。
type Manager struct {
	// ProfileDir 是本应用专用的浏览器 profile 目录。
	ProfileDir string

	mu   sync.Mutex
	cmd  *exec.Cmd
	port int
	path string // 浏览器级 WebSocket 路径
	exe  string
}

// NewManager 创建管理器；profileDir 为空时使用系统用户配置目录下的默认位置。
func NewManager(profileDir string) *Manager {
	if profileDir == "" {
		if dir, err := userDataDir(); err == nil {
			profileDir = filepath.Join(dir, "browser-profile")
		}
	}
	return &Manager{ProfileDir: profileDir}
}

// Available 返回本机检测到的可用浏览器（按偏好排序）。
func Available() []Info {
	var out []Info
	for _, b := range candidates() {
		if b.Path == "" {
			continue
		}
		st, err := os.Stat(b.Path)
		if err != nil || st.IsDir() {
			continue
		}
		out = append(out, Info{Name: b.Name, Path: b.Path})
	}
	return out
}

// Supported 报告本机是否存在可用于登录的浏览器。
func Supported() bool { return len(Available()) > 0 }

// Running 报告浏览器实例当前是否在运行。
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runningLocked()
}

// runningLocked 报告实例是否仍在运行；调用方须持有 m.mu。
func (m *Manager) runningLocked() bool {
	if m.cmd == nil || m.cmd.Process == nil {
		return false
	}
	// 进程已退出但未回收时按未运行处理。
	if m.cmd.ProcessState != nil && m.cmd.ProcessState.Exited() {
		return false
	}
	return true
}

// Executable 返回当前使用的浏览器可执行文件路径。
func (m *Manager) Executable() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.exe
}

// Start 启动浏览器（独立 profile）并打开 url；已在运行时改为新开标签页。
// 返回本次是否复用了已有实例。
func (m *Manager) Start(ctx context.Context, url string) (reused bool, err error) {
	url = normalizeURL(url)

	m.mu.Lock()
	if m.runningLocked() {
		port := m.port
		m.mu.Unlock()
		if err := m.openTab(ctx, port, url); err != nil {
			return true, err
		}
		return true, nil
	}
	m.mu.Unlock()

	bins := Available()
	if len(bins) == 0 {
		return false, errors.New("未检测到 Chrome / Edge / Brave 等浏览器，请先安装其中之一")
	}
	if err := os.MkdirAll(m.ProfileDir, 0o700); err != nil {
		return false, fmt.Errorf("创建浏览器配置目录失败: %w", err)
	}
	// 清理上次残留的端口文件，避免读到过期端口。
	portFile := filepath.Join(m.ProfileDir, "DevToolsActivePort")
	_ = os.Remove(portFile)

	args := []string{
		"--remote-debugging-port=0",
		"--user-data-dir=" + m.ProfileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-features=Translate",
	}
	if url != "" {
		args = append(args, url)
	}

	cmd := exec.Command(bins[0].Path, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("启动浏览器失败: %w", err)
	}

	port, wsPath, err := waitDevToolsPort(ctx, portFile)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return false, err
	}

	m.mu.Lock()
	m.cmd, m.port, m.path, m.exe = cmd, port, wsPath, bins[0].Path
	m.mu.Unlock()
	return false, nil
}

// Close 关闭由本管理器启动的浏览器实例并回收进程。
func (m *Manager) Close() {
	m.mu.Lock()
	cmd, port, wsPath := m.cmd, m.port, m.path
	m.cmd, m.port, m.path = nil, 0, ""
	m.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}
	// 先请求优雅退出（保证 Cookie 落盘），超时后强制结束。
	if port > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = m.callCDP(ctx, port, wsPath, "Browser.close", nil)
		cancel()
	}
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

// Reset 关闭浏览器并删除独立 profile（等同退出登录、清除该浏览器状态）。
func (m *Manager) Reset() error {
	m.Close()
	if err := os.RemoveAll(m.ProfileDir); err != nil {
		return fmt.Errorf("清除浏览器登录状态失败: %w", err)
	}
	return nil
}

// Cookies 通过 CDP 读取当前浏览器实例的全部 Cookie（含 HttpOnly）。
func (m *Manager) Cookies(ctx context.Context) ([]Cookie, error) {
	m.mu.Lock()
	port, wsPath := m.port, m.path
	running := m.runningLocked()
	m.mu.Unlock()

	if !running {
		return nil, errors.New("浏览器未在运行，请先打开登录窗口")
	}
	raw, err := m.callCDP(ctx, port, wsPath, "Storage.getCookies", map[string]any{})
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Cookies []struct {
			Name     string  `json:"name"`
			Value    string  `json:"value"`
			Domain   string  `json:"domain"`
			Path     string  `json:"path"`
			Secure   bool    `json:"secure"`
			HTTPOnly bool    `json:"httpOnly"`
			Expires  float64 `json:"expires"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析 Cookie 结果失败: %w", err)
	}

	out := make([]Cookie, 0, len(parsed.Cookies))
	for _, c := range parsed.Cookies {
		cookie := Cookie{
			Domain:   c.Domain,
			Path:     c.Path,
			Name:     c.Name,
			Value:    c.Value,
			Secure:   c.Secure,
			HttpOnly: c.HTTPOnly,
		}
		// CDP 中 expires<=0 表示会话 Cookie。
		if c.Expires > 0 {
			cookie.ExpiresAt = time.Unix(int64(c.Expires), 0)
		}
		out = append(out, cookie)
	}
	return out, nil
}

// openTab 在已运行的浏览器中打开一个新标签页。
func (m *Manager) openTab(ctx context.Context, port int, url string) error {
	if strings.TrimSpace(url) == "" {
		return nil
	}
	// /json/new 需要 PUT；旧版 Chrome 只接受 GET，失败时回退。
	target := fmt.Sprintf("http://127.0.0.1:%d/json/new?%s", port, url)
	for _, method := range []string{http.MethodPut, http.MethodGet} {
		req, err := http.NewRequestWithContext(ctx, method, target, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 400 {
			return nil
		}
	}
	return errors.New("在浏览器中打开新标签页失败")
}

// callCDP 建立到浏览器级 WebSocket 的连接并调用一次方法，返回 result 原始 JSON。
func (m *Manager) callCDP(ctx context.Context, port int, wsPath, method string, params map[string]any) (json.RawMessage, error) {
	if port <= 0 {
		return nil, errors.New("浏览器未在运行")
	}
	if wsPath == "" {
		wsPath = "/devtools/browser"
	}
	url := fmt.Sprintf("ws://127.0.0.1:%d%s", port, wsPath)

	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.DialContext(ctx, url, nil)
	if err != nil {
		return nil, fmt.Errorf("连接浏览器调试端口失败: %w", err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
	} else {
		_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	}
	if params == nil {
		params = map[string]any{}
	}
	if err := conn.WriteJSON(map[string]any{"id": 1, "method": method, "params": params}); err != nil {
		return nil, fmt.Errorf("发送 CDP 请求失败: %w", err)
	}
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return nil, fmt.Errorf("读取 CDP 响应失败: %w", err)
		}
		var resp struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(msg, &resp); err != nil || resp.ID != 1 {
			continue // 事件通知等非响应消息，跳过。
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("浏览器返回错误: %s", resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// waitDevToolsPort 等待浏览器写出 DevToolsActivePort 文件并解析端口与路径。
// 文件第一行是端口，第二行是浏览器级 WebSocket 路径。
func waitDevToolsPort(ctx context.Context, path string) (int, string, error) {
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return 0, "", err
		}
		if data, err := os.ReadFile(path); err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) >= 2 {
				port, convErr := strconv.Atoi(strings.TrimSpace(lines[0]))
				if convErr == nil && port > 0 {
					return port, strings.TrimSpace(lines[1]), nil
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return 0, "", errors.New("等待浏览器调试端口超时，请确认浏览器已正常启动")
}

// normalizeURL 补全常见输入形式为合法 URL（纯 Go，无平台依赖）。
func normalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	return raw
}

// userDataDir 返回应用数据目录（与 media 包保持一致的本平台约定）。
func userDataDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "AnyDLNA"), nil
}
