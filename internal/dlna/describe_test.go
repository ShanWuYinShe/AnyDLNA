package dlna

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const sampleRendererDesc = `<?xml version="1.0" encoding="UTF-8"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <device>
    <deviceType>urn:schemas-upnp-org:device:MediaRenderer:1</deviceType>
    <friendlyName>客厅电视</friendlyName>
    <manufacturer>Xiaomi</manufacturer>
    <modelName>MiTV-4A</modelName>
    <UDN>uuid:0f5d2b34-9f17-4e5a-b6a1-1234567890ab</UDN>
    <serviceList>
      <service>
        <serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType>
        <serviceId>urn:upnp-org:serviceId:AVTransport</serviceId>
        <SCPDURL>/AVTransport.xml</SCPDURL>
        <controlURL>/dev/0f5d2b34/control/AVTransport</controlURL>
        <eventSubURL>/dev/0f5d2b34/event/AVTransport</eventSubURL>
      </service>
      <service>
        <serviceType>urn:schemas-upnp-org:service:RenderingControl:1</serviceType>
        <serviceId>urn:upnp-org:serviceId:RenderingControl</serviceId>
        <SCPDURL>/RenderingControl.xml</SCPDURL>
        <controlURL>/dev/0f5d2b34/control/RenderingControl</controlURL>
        <eventSubURL>/dev/0f5d2b34/event/RenderingControl</eventSubURL>
      </service>
    </serviceList>
  </device>
</root>`

// 嵌套设备：AVTransport 位于内嵌设备中（部分厂商的实现方式）。
const nestedRendererDesc = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <deviceType>urn:schemas-upnp-org:device:MediaRenderer:1</deviceType>
    <friendlyName>主设备</friendlyName>
    <UDN>uuid:nested-device</UDN>
    <deviceList>
      <device>
        <UDN>uuid:embedded-device</UDN>
        <friendlyName>内嵌渲染器</friendlyName>
        <serviceList>
          <service>
            <serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType>
            <serviceId>urn:upnp-org:serviceId:AVTransport</serviceId>
            <controlURL>/ctrl</controlURL>
          </service>
        </serviceList>
      </device>
    </deviceList>
  </device>
</root>`

func TestDescribeResolvesRelativeURLs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleRendererDesc))
	}))
	defer srv.Close()

	dev, err := Describe(testCtx(), srv.Client(), srv.URL+"/desc.xml")
	if err != nil {
		t.Fatalf("Describe 失败: %v", err)
	}
	if dev.FriendlyName != "客厅电视" || dev.UDN != "uuid:0f5d2b34-9f17-4e5a-b6a1-1234567890ab" {
		t.Fatalf("设备信息解析错误: %+v", dev)
	}
	if !dev.HasAVTransport() {
		t.Fatal("期望具备 AVTransport 服务")
	}
	av := dev.Service(AVTransportServiceType)
	if want := srv.URL + "/dev/0f5d2b34/control/AVTransport"; av.ControlURL != want {
		t.Errorf("ControlURL = %q, 期望 %q", av.ControlURL, want)
	}
	if dev.Service(RenderingControlServiceType) == nil {
		t.Error("缺少 RenderingControl 服务")
	}
}

func TestDescribeCollectsEmbeddedDeviceServices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(nestedRendererDesc))
	}))
	defer srv.Close()

	dev, err := Describe(testCtx(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Describe 失败: %v", err)
	}
	if dev.FriendlyName != "主设备" {
		t.Errorf("应取根设备名称，得到 %q", dev.FriendlyName)
	}
	if !dev.HasAVTransport() {
		t.Fatal("应收集到内嵌设备的 AVTransport 服务")
	}
	if want := srv.URL + "/ctrl"; dev.Service(AVTransportServiceType).ControlURL != want {
		t.Errorf("内嵌服务 ControlURL = %q, 期望 %q", dev.Service(AVTransportServiceType).ControlURL, want)
	}
}

func TestParseSSDPHeaders(t *testing.T) {
	raw := []byte("HTTP/1.1 200 OK\r\n" +
		"CACHE-CONTROL: max-age=1800\r\n" +
		"LOCATION: http://192.168.1.5:49152/desc.xml\r\n" +
		"SERVER: Linux UPnP/1.0\r\n" +
		"ST: urn:schemas-upnp-org:service:AVTransport:1\r\n" +
		"USN: uuid:abc-123::urn:schemas-upnp-org:service:AVTransport:1\r\n\r\n")
	kind, h := parseSSDPHeaders(raw)
	if kind != "search-response" {
		t.Fatalf("kind = %q, 期望 search-response", kind)
	}
	if h["LOCATION"] != "http://192.168.1.5:49152/desc.xml" {
		t.Errorf("LOCATION = %q", h["LOCATION"])
	}
	if h["USN"] != "uuid:abc-123::urn:schemas-upnp-org:service:AVTransport:1" {
		t.Errorf("USN = %q", h["USN"])
	}

	notify := []byte("NOTIFY * HTTP/1.1\r\n" +
		"HOST: 239.255.255.250:1900\r\n" +
		"NT: urn:schemas-upnp-org:device:MediaRenderer:1\r\n" +
		"NTS: ssdp:alive\r\n" +
		"USN: uuid:xyz::urn:schemas-upnp-org:device:MediaRenderer:1\r\n" +
		"LOCATION: http://192.168.1.9:49152/dd.xml\r\n\r\n")
	kind, h = parseSSDPHeaders(notify)
	if kind != "notify" || !isRendererTarget(h["NT"]) || !strings.EqualFold(h["NTS"], "ssdp:alive") {
		t.Fatalf("NOTIFY 解析错误: kind=%q h=%v", kind, h)
	}

	if kind, _ := parseSSDPHeaders([]byte("M-SEARCH * HTTP/1.1\r\nST: ssdp:all\r\n\r\n")); kind != "other" {
		t.Errorf("自己的 M-SEARCH 应被忽略，得到 %q", kind)
	}
}

func TestDescribeByHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleRendererDesc))
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	dev, err := DescribeByHost(testCtx(), host)
	if err != nil {
		t.Fatalf("DescribeByHost 失败: %v", err)
	}
	if !dev.HasAVTransport() || dev.FriendlyName != "客厅电视" {
		t.Fatalf("设备信息错误: %+v", dev)
	}
}

// 真实设备中出现的非常规 controlURL：以 "_" 开头且含冒号。
// 这类写法会让 url.Parse 报 "first path segment in URL cannot contain colon"。
const weirdControlURLDesc = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <deviceType>urn:schemas-upnp-org:device:MediaRenderer:1</deviceType>
    <friendlyName>电视</friendlyName>
    <UDN>uuid:weird-control</UDN>
    <serviceList>
      <service>
        <serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType>
        <serviceId>urn:upnp-org:serviceId:AVTransport</serviceId>
        <controlURL>_urn:schemas-upnp-org:service:AVTransport_control</controlURL>
        <eventSubURL>_urn:schemas-upnp-org:service:AVTransport_event</eventSubURL>
      </service>
    </serviceList>
  </device>
</root>`

// TestResolveReferenceHandlesColonInFirstSegment 回归测试：
// controlURL 形如 "_urn:schemas-upnp-org:service:AVTransport_control" 时，
// 必须解析为可用的绝对 URL，而不是把非法字符串原样交给 HTTP 请求
// （否则投屏会报 "下发播放地址失败: parse ... first path segment in URL cannot contain colon"）。
func TestResolveReferenceHandlesColonInFirstSegment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(weirdControlURLDesc))
	}))
	defer srv.Close()

	dev, err := Describe(testCtx(), srv.Client(), srv.URL+"/desc.xml")
	if err != nil {
		t.Fatalf("Describe 失败: %v", err)
	}
	av := dev.Service(AVTransportServiceType)
	if av == nil {
		t.Fatal("应解析出 AVTransport 服务")
	}

	// 关键断言：结果必须是可被 net/http 接受的绝对 URL。
	req, err := http.NewRequest(http.MethodPost, av.ControlURL, nil)
	if err != nil {
		t.Fatalf("ControlURL %q 无法用于 HTTP 请求: %v", av.ControlURL, err)
	}
	if req.URL.Host == "" {
		t.Errorf("ControlURL 缺少主机: %q", av.ControlURL)
	}
	if want := "/_urn:schemas-upnp-org:service:AVTransport_control"; req.URL.Path != want {
		t.Errorf("路径 = %q, 期望 %q", req.URL.Path, want)
	}
	if !strings.HasPrefix(av.ControlURL, srv.URL) {
		t.Errorf("应基于 LOCATION 拼接主机: %q", av.ControlURL)
	}

	// EventSubURL 同样需要规范化。
	sub, err := http.NewRequest(http.MethodPost, av.EventSubURL, nil)
	if err != nil {
		t.Errorf("EventSubURL %q 无法用于 HTTP 请求: %v", av.EventSubURL, err)
	} else if sub.URL.Path != "/_urn:schemas-upnp-org:service:AVTransport_event" {
		t.Errorf("EventSubURL 路径 = %q", sub.URL.Path)
	}
}

// TestResolveReferenceVariants 覆盖各种 URL 写法，确保兼容性且不破坏标准语义。
func TestResolveReferenceVariants(t *testing.T) {
	base, err := url.Parse("http://192.168.1.100:49152/dev/desc.xml")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"空值", "", ""},
		{"根相对路径", "/ctrl", "http://192.168.1.100:49152/ctrl"},
		{"目录相对路径（RFC 语义不变）", "ctrl", "http://192.168.1.100:49152/dev/ctrl"},
		{"绝对 URL", "http://other:8080/x", "http://other:8080/x"},
		{"协议相对形式", "//192.168.1.100:49152/ctrl", "http://192.168.1.100:49152/ctrl"},
		{"下划线开头含冒号", "_urn:x:y_control", "http://192.168.1.100:49152/_urn:x:y_control"},
		{"伪 scheme 形式", "urn:schemas-upnp-org:service:AVTransport", "http://192.168.1.100:49152/urn:schemas-upnp-org:service:AVTransport"},
		{"裸 host:port 含冒号", "192.168.1.100:8080/ctrl", "http://192.168.1.100:49152/192.168.1.100:8080/ctrl"},
		{"带空格", "  /ctrl  ", "http://192.168.1.100:49152/ctrl"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveReference(base, tc.raw)
			if got != tc.want {
				t.Errorf("resolveReference(%q) = %q, 期望 %q", tc.raw, got, tc.want)
			}
			// 非空结果都必须能用于 HTTP 请求。
			if got != "" {
				if _, err := http.NewRequest(http.MethodPost, got, nil); err != nil {
					t.Errorf("结果 %q 无法用于 HTTP 请求: %v", got, err)
				}
			}
		})
	}
}
