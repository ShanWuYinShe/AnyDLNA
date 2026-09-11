package dlna

import (
	"net/http"
	"net/http/httptest"
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

func TestParseSSDPResponse(t *testing.T) {
	raw := []byte("HTTP/1.1 200 OK\r\n" +
		"CACHE-CONTROL: max-age=1800\r\n" +
		"LOCATION: http://192.168.1.5:49152/desc.xml\r\n" +
		"SERVER: Linux UPnP/1.0\r\n" +
		"ST: urn:schemas-upnp-org:service:AVTransport:1\r\n" +
		"USN: uuid:abc-123::urn:schemas-upnp-org:service:AVTransport:1\r\n\r\n")
	dev, ok := parseSSDPResponse(raw)
	if !ok {
		t.Fatal("期望解析成功")
	}
	if dev.Location != "http://192.168.1.5:49152/desc.xml" {
		t.Errorf("Location = %q", dev.Location)
	}
	if dev.USN != "uuid:abc-123::urn:schemas-upnp-org:service:AVTransport:1" {
		t.Errorf("USN = %q", dev.USN)
	}
	if _, ok := parseSSDPResponse([]byte("not http")); ok {
		t.Error("非 HTTP 文本不应解析成功")
	}
}
