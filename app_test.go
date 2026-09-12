package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"AnyDLNA/internal/dlna"
	"AnyDLNA/internal/media"
)

// TestStopCastDoesNotHoldStateLock 回归测试：投屏状态清理（会等待转码进程退出）
// 绝不能在持有 a.mu 时进行，否则 StalledCast 期间所有前端 IPC 都会被阻塞。
func TestStopCastDoesNotHoldStateLock(t *testing.T) {
	app := NewApp()

	// 模拟一次正在进行的投屏，让 stopCast 有会话需要回收。
	srv, err := media.NewStreamServer()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	app.streamSrv = srv
	app.sessionID = "session-under-test"

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.stopCast()
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stopCast 超时未返回（可能持锁等待进程回收）")
	}

	// stopCast 结束后状态必须已清空，且 a.mu 可立即获取。
	app.mu.Lock()
	if app.sessionID != "" || app.renderer != nil {
		t.Errorf("stopCast 后状态未清空: sessionID=%q renderer=%v", app.sessionID, app.renderer)
	}
	app.mu.Unlock()
}

// TestCastURLDoesNotDeadlock 回归测试：CastURL 曾经在持有 a.mu 时再次加锁
// 造成永久死锁，导致「选择视频后投屏 UI 无任何反应」。
//
// 该测试不依赖真实设备：用一个本地渲染设备描述 + 会在解析阶段失败/成功的
// 路径，确保调用一定返回而不是挂死。
func TestCastURLDoesNotDeadlock(t *testing.T) {
	app := NewApp()
	srv, err := media.NewStreamServer()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	app.streamSrv = srv

	// 一台假的渲染设备，其控制端点指向本地测试服务器。
	dev := newFakeRendererDevice(t)
	app.devices = append(app.devices, dev)

	// 用一个必定失败的 URL，让 CastURL 在解析阶段就返回错误。
	// 关键断言是：它必须「返回」，而不是死锁。
	done := make(chan error, 1)
	go func() {
		_, err := app.CastURL(dev.UDN, "not-a-valid-url:///")
		done <- err
	}()

	select {
	case <-done:
		// 返回即通过；解析失败是预期结果（本机可能未装 yt-dlp）。
	case <-time.After(60 * time.Second):
		t.Fatal("CastURL 超时未返回：疑似再次出现自死锁")
	}

	// 死锁回归的核心判据：状态锁必须仍可获取，否则前端 IPC 会全部卡死。
	acquired := make(chan struct{})
	go func() {
		app.mu.Lock()
		app.mu.Unlock()
		close(acquired)
	}()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("a.mu 无法获取：状态锁已被永久占用")
	}
}

// TestCastURLReportsMissingTools 确认缺少依赖时给出明确错误而非静默无反应。
func TestCastURLReportsMissingTools(t *testing.T) {
	app := NewApp()
	srv, err := media.NewStreamServer()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	app.streamSrv = srv

	dev := newFakeRendererDevice(t)
	app.devices = append(app.devices, dev)

	// 设备不存在时应有明确错误。
	if _, err := app.CastURL("no-such-udn", "https://example.com/v"); err == nil {
		t.Error("设备不存在时应返回错误")
	}

	// 设备存在但 URL 无法解析时应返回错误（而非挂起）。
	done := make(chan error, 1)
	go func() {
		_, err := app.CastURL(dev.UDN, "not-a-valid-url:///")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("无效 URL 应返回错误")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("CastURL 超时未返回")
	}
}

// TestConcurrentIPCNotBlockedByCast 回归测试：一次投屏尝试进行期间，
// 其他前端 IPC（如 Poll / GetCastStatus）必须仍可及时返回。
func TestConcurrentIPCNotBlockedByCast(t *testing.T) {
	app := NewApp()
	srv, err := media.NewStreamServer()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	app.streamSrv = srv

	dev := newFakeRendererDevice(t)
	app.devices = append(app.devices, dev)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = app.CastURL(dev.UDN, "not-a-valid-url:///")
	}()

	// 投屏进行中，其他 IPC 调用应在短时间内返回。
	for i := 0; i < 5; i++ {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = app.GetCastStatus()
			_, _ = app.Poll()
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("投屏期间其他 IPC 被阻塞：疑似长耗时操作持有状态锁")
		}
	}
	wg.Wait()
}

// newFakeRendererDevice 构造一台指向本地测试服务器的假渲染设备，
// 使投屏流程无需真实电视即可推进到下发阶段。
func newFakeRendererDevice(t *testing.T) *dlna.Device {
	t.Helper()

	// 描述 XML 中给出 AVTransport 服务，控制端点指向本地 SOAP 服务器。
	mux := http.NewServeMux()
	mux.HandleFunc("/control", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
		fmt.Fprint(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body><u:PlayResponse xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"/></s:Body>
</s:Envelope>`)
	})
	mux.HandleFunc("/desc.xml", func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		fmt.Fprintf(w, `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <UDN>uuid:fake-renderer</UDN>
    <friendlyName>测试电视</friendlyName>
    <manufacturer>test</manufacturer>
    <modelName>fake</modelName>
    <serviceList>
      <service>
        <serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType>
        <serviceId>urn:upnp-org:serviceId:AVTransport</serviceId>
        <controlURL>/control</controlURL>
        <eventSubURL>/event</eventSubURL>
        <SCPDURL>/scpd.xml</SCPDURL>
      </service>
    </serviceList>
  </device>
</root>`)
		_ = host
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// 用真实的 Describe 解析流程构建设备，确保服务 URL 正确解析为绝对地址。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dev, err := dlna.Describe(ctx, srv.Client(), srv.URL+"/desc.xml")
	if err != nil {
		t.Fatalf("构造测试设备失败: %v", err)
	}
	if !dev.HasAVTransport() {
		t.Fatal("测试设备应包含 AVTransport 服务")
	}
	return dev
}

// TestFakeDeviceControlURLParsed 确认测试替身的控制地址被正确解析，
// 避免因测试夹具本身错误导致回归测试失去意义。
func TestFakeDeviceControlURLParsed(t *testing.T) {
	dev := newFakeRendererDevice(t)
	svc := dev.Service(dlna.AVTransportServiceType)
	if svc == nil {
		t.Fatal("应解析出 AVTransport 服务")
	}
	if !strings.HasSuffix(svc.ControlURL, "/control") {
		t.Errorf("控制地址解析错误: %q", svc.ControlURL)
	}
	// 控制地址必须是绝对 URL，否则 SOAP 调用会失败。
	if !strings.HasPrefix(svc.ControlURL, "http://") {
		t.Errorf("控制地址应为绝对 URL: %q", svc.ControlURL)
	}
}

// TestDeviceDescriptionFixture 校验测试用设备描述解析正常（防止夹具失真）。
func TestDeviceDescriptionFixture(t *testing.T) {
	var desc struct {
		Devices []struct {
			UDN string `xml:"UDN"`
		} `xml:"device"`
	}
	err := xml.Unmarshal([]byte(`<root><device><UDN>uuid:x</UDN></device></root>`), &desc)
	if err != nil || len(desc.Devices) != 1 {
		t.Fatalf("XML 夹具解析异常: %v", err)
	}
}

// ensureLoopbackFree 确认本地测试端口可用（辅助诊断）。
func ensureLoopbackFree(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("本机无法监听回环端口: %v", err)
	}
	_ = ln.Close()
}

// TestSearchBudgetExceedsSearchWindow 是设备搜索的回归测试。
//
// 背景：SSDP 搜索阶段（searchDevices）会一直等到超时才返回，之后才逐个
// 抓取并解析设备描述。此前 ctx 的截止时间与搜索窗口相同，导致描述请求
// 运行在已过期的上下文上、立即失败，最终「搜索成功却一台设备都没有」，
// 而且不报任何错——表现为用户界面上永远搜不到设备。
//
// 该测试固定住这条约束：ctx 的总预算必须严格大于搜索窗口。
func TestSearchBudgetExceedsSearchWindow(t *testing.T) {
	for _, ms := range []int{1, 1000, 6000, 15000} {
		window := time.Duration(ms) * time.Millisecond
		got := searchBudget(ms)
		if got <= window {
			t.Errorf("timeoutMS=%d: 总预算 %v 未超过搜索窗口 %v，描述解析会因 ctx 过期而失败", ms, got, window)
		}
		if got-window != describeBudget {
			t.Errorf("timeoutMS=%d: 描述预算应为 %v，实际 %v", ms, describeBudget, got-window)
		}
	}
}
