package dlna

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Describe 拉取并解析设备描述 XML，构造 Device。
// location 为 SSDP 响应中的 LOCATION URL。
func Describe(ctx context.Context, client *http.Client, location string) (*Device, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("获取设备描述失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("获取设备描述失败: HTTP %d", resp.StatusCode)
	}

	// 部分厂商的描述 XML 缺少编码声明甚至声明错误，优先信任 HTTP 头。
	base, err := url.Parse(location)
	if err != nil {
		return nil, err
	}

	var desc struct {
		Devices []descDevice `xml:"device"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&desc); err != nil {
		return nil, fmt.Errorf("解析设备描述失败: %w", err)
	}
	if len(desc.Devices) == 0 {
		return nil, fmt.Errorf("设备描述中没有根设备")
	}

	dev := &Device{Location: location}
	collectDevice(&desc.Devices[0], base, dev)
	if !dev.HasAVTransport() {
		return dev, nil // 不含 AVTransport 的设备（路由器等），由调用方过滤。
	}
	return dev, nil
}

// descDevice 递归描述设备节点；encoding/xml 按元素局部名匹配，可兼容各厂商命名空间。
type descDevice struct {
	UDN          string `xml:"UDN"`
	FriendlyName string `xml:"friendlyName"`
	Manufacturer string `xml:"manufacturer"`
	ModelName    string `xml:"modelName"`
	Services     []struct {
		Type        string `xml:"serviceType"`
		ID          string `xml:"serviceId"`
		ControlURL  string `xml:"controlURL"`
		EventSubURL string `xml:"eventSubURL"`
	} `xml:"serviceList>service"`
	Children []descDevice `xml:"deviceList>device"`
}

// collectDevice 汇总设备自身及全部内嵌设备的信息与服务端点。
func collectDevice(node *descDevice, base *url.URL, dev *Device) {
	if dev.UDN == "" && node.UDN != "" {
		dev.UDN = strings.TrimSpace(node.UDN)
	}
	if dev.FriendlyName == "" && node.FriendlyName != "" {
		dev.FriendlyName = strings.TrimSpace(node.FriendlyName)
	}
	if dev.Manufacturer == "" && node.Manufacturer != "" {
		dev.Manufacturer = strings.TrimSpace(node.Manufacturer)
	}
	if dev.ModelName == "" && node.ModelName != "" {
		dev.ModelName = strings.TrimSpace(node.ModelName)
	}
	for _, svc := range node.Services {
		if svc.Type != AVTransportServiceType && svc.Type != RenderingControlServiceType {
			continue
		}
		if dev.Service(svc.Type) != nil {
			continue
		}
		dev.Services = append(dev.Services, Service{
			Type:        svc.Type,
			ID:          svc.ID,
			ControlURL:  resolveReference(base, svc.ControlURL),
			EventSubURL: resolveReference(base, svc.EventSubURL),
		})
	}
	for i := range node.Children {
		collectDevice(&node.Children[i], base, dev)
	}
}

// DescribeByHost 在 SSDP 失效时手动发现设备：并发尝试常见 UPnP 描述地址，
// 返回第一台具备 AVTransport 的设备。host 可以带端口（如 192.168.1.100:49152）。
func DescribeByHost(ctx context.Context, host string) (*Device, error) {
	client := defaultHTTPClient()
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(strings.TrimPrefix(host, "http://"), "https://")
	host = strings.TrimSuffix(host, "/")

	var candidates []string
	if !strings.Contains(host, ":") {
		// 未指定端口：轮询常见 UPnP 端口。
		for _, port := range []string{"49152", "49153", "49154", "8080", "7676", "36669", "55000", "51423"} {
			for _, path := range []string{"/dd.xml", "/description.xml", "/"} {
				candidates = append(candidates, "http://"+host+":"+port+path)
			}
		}
	} else {
		for _, path := range []string{"/dd.xml", "/description.xml", "/device.xml", "/"} {
			candidates = append(candidates, "http://"+host+path)
		}
	}

	type result struct {
		dev *Device
		err error
	}
	ch := make(chan result, len(candidates))
	for _, raw := range candidates {
		go func(u string) {
			dev, err := Describe(ctx, client, u)
			if err == nil && dev.HasAVTransport() {
				ch <- result{dev: dev}
				return
			}
			ch <- result{err: fmt.Errorf("%s: %v", u, err)}
		}(raw)
	}
	var lastErr error
	for range candidates {
		r := <-ch
		if r.dev != nil {
			return r.dev, nil
		}
		lastErr = r.err
	}
	return nil, fmt.Errorf("在 %s 上未发现 DLNA 渲染设备（请确认地址与端口）: %v", host, lastErr)
}

// resolveReference 将描述中的相对 URL 基于 LOCATION 解析为绝对 URL。
//
// 需要兼容两类不规范的设备写法：
//   - 部分设备把 controlURL 写成 "_urn:schemas-upnp-org:service:AVTransport_control"。
//     按 RFC 3986，相对引用的首个路径段不能含冒号，url.Parse 会直接报错
//     （"first path segment in URL cannot contain colon"）。
//   - 部分设备写成 "urn:schemas-upnp-org:service:AVTransport" 这类伪 scheme
//     形式，url.Parse 会成功但得到的引用没有主机，无法用于 HTTP 请求。
//
// 两种情况的处理方式一致：补前导 "/" 使其成为合法的相对根路径引用。
// 正常的相对路径（含以 "/" 开头的）保持原有语义不变，不做多余改写。
func resolveReference(base *url.URL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	ref, err := url.Parse(raw)
	switch {
	case err == nil && ref.Host != "" && ref.Scheme != "":
		// 带协议的绝对 URL，直接采用。
		return ref.String()
	case err == nil && ref.Host != "":
		// 协议相对形式（//host/path）：补上与 LOCATION 相同的协议。
		ref.Scheme = base.Scheme
		return ref.String()
	case err == nil && ref.Scheme == "" && ref.Opaque == "":
		// 常规相对路径：交由标准库按 RFC 3986 解析（保留目录相对语义）。
		return base.ResolveReference(ref).String()
	}

	// 解析失败，或形如 "urn:..." 的无主机伪 scheme：按相对根路径处理。
	path := raw
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base.ResolveReference(&url.URL{Path: path}).String()
}

// defaultHTTPClient 供控制点复用；DLNA 设备都在局域网，超时从严，
// 且显式禁用代理——即使环境变量配置了 HTTP_PROXY 也不影响局域网控制。
func defaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
}
