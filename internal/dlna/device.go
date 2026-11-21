// Package dlna 实现一个精简的 UPnP/DLNA 控制点：
// 通过 SSDP 发现局域网内的媒体渲染设备（MediaRenderer），
// 并通过 AVTransport / RenderingControl / ConnectionManager 的 SOAP 动作
// 控制播放并协商设备支持的媒体格式。
package dlna

import "strings"

const (
	// AVTransportServiceType 是 DLNA 媒体渲染设备必备的服务类型（标准 v1）。
	AVTransportServiceType = "urn:schemas-upnp-org:service:AVTransport:1"
	// RenderingControlServiceType 提供音量等渲染控制（标准 v1）。
	RenderingControlServiceType = "urn:schemas-upnp-org:service:RenderingControl:1"
	// ConnectionManagerServiceType 提供设备能力协商（标准 v1）。
	ConnectionManagerServiceType = "urn:schemas-upnp-org:service:ConnectionManager:1"

	// 服务类型前缀，用于向下兼容 v2、v3 等更高版本服务。
	AVTransportPrefix       = "urn:schemas-upnp-org:service:AVTransport:"
	RenderingControlPrefix  = "urn:schemas-upnp-org:service:RenderingControl:"
	ConnectionManagerPrefix = "urn:schemas-upnp-org:service:ConnectionManager:"
)

// knownServicePrefixes 是需要从设备描述中保留的服务前缀列表。
var knownServicePrefixes = []string{
	AVTransportPrefix,
	RenderingControlPrefix,
	ConnectionManagerPrefix,
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

// Service 返回指定类型的服务：优先精确匹配；若无则尝试前缀相同的高版本（如 v2/v3）；不存在时返回 nil。
func (d *Device) Service(serviceType string) *Service {
	// 1. 精确匹配
	for i := range d.Services {
		if d.Services[i].Type == serviceType {
			return &d.Services[i]
		}
	}
	// 2. 前缀兼容匹配（截取版本号前缀，如 urn:schemas-upnp-org:service:AVTransport:）
	if idx := strings.LastIndex(serviceType, ":"); idx != -1 {
		prefix := serviceType[:idx+1]
		for i := range d.Services {
			if strings.HasPrefix(d.Services[i].Type, prefix) {
				return &d.Services[i]
			}
		}
	}
	return nil
}

// HasAVTransport 表示该设备是否具备 AVTransport 服务（即可被投屏控制，兼容 v1/v2/v3）。
func (d *Device) HasAVTransport() bool {
	return d.Service(AVTransportServiceType) != nil
}

// HasConnectionManager 表示该设备是否支持格式协商（兼容 v1/v2/v3）。
func (d *Device) HasConnectionManager() bool {
	return d.Service(ConnectionManagerServiceType) != nil
}
