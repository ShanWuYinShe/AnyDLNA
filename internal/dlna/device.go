// Package dlna 实现一个精简的 UPnP/DLNA 控制点：
// 通过 SSDP 发现局域网内的媒体渲染设备（MediaRenderer），
// 并通过 AVTransport / RenderingControl / ConnectionManager 的 SOAP 动作
// 控制播放并协商设备支持的媒体格式。
package dlna

const (
	// AVTransportServiceType 是 DLNA 媒体渲染设备必备的服务类型。
	AVTransportServiceType = "urn:schemas-upnp-org:service:AVTransport:1"
	// RenderingControlServiceType 提供音量等渲染控制，为可选服务。
	RenderingControlServiceType = "urn:schemas-upnp-org:service:RenderingControl:1"
	// ConnectionManagerServiceType 提供设备能力协商（协议信息查询），为可选服务。
	// 通过它的 GetProtocolInfo 动作可拿到设备声明支持的媒体格式。
	ConnectionManagerServiceType = "urn:schemas-upnp-org:service:ConnectionManager:1"
)

// knownServiceTypes 是需要从设备描述中保留的服务类型。
// 描述里还可能有 ContentDirectory 等与本应用无关的服务，一律不收集。
var knownServiceTypes = []string{
	AVTransportServiceType,
	RenderingControlServiceType,
	ConnectionManagerServiceType,
}

// Service 是设备描述中的一个 UPnP 服务端点。
type Service struct {
	Type        string // 例如 urn:schemas-upnp-org:service:AVTransport:1
	ID          string // 例如 urn:upnp-org:serviceId:AVTransport
	ControlURL  string // 已解析为绝对 URL
	EventSubURL string // 已解析为绝对 URL，可能为空
}

// Device 表示局域网内发现的一台 DLNA 渲染设备（电视、盒子等）。
type Device struct {
	UDN          string // 设备唯一标识
	FriendlyName string // 展示名称
	Manufacturer string
	ModelName    string
	Location     string // 设备描述 XML 的 LOCATION URL
	Services     []Service
}

// Service 返回指定类型的第一个服务；不存在时返回 nil。
func (d *Device) Service(serviceType string) *Service {
	for i := range d.Services {
		if d.Services[i].Type == serviceType {
			return &d.Services[i]
		}
	}
	return nil
}

// HasAVTransport 表示该设备是否具备 AVTransport 服务（即可被投屏控制）。
func (d *Device) HasAVTransport() bool {
	return d.Service(AVTransportServiceType) != nil
}

// HasConnectionManager 表示该设备是否支持格式协商。
func (d *Device) HasConnectionManager() bool {
	return d.Service(ConnectionManagerServiceType) != nil
}
