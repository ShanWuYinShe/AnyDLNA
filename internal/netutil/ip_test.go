package netutil

import (
	"net"
	"testing"
)

// TestLANIP 校验 LANIP 的返回值契约。
//
// LANIP 没有入参：它对 8.8.8.8:80 发起 UDP 拨接（UDP 不会实际发包，离线
// 安全，只需路由表可用），由内核选择默认路由出口地址并返回。因此「正常值 /
// 边界值」体现为对返回值的约束，而非输入用例：
//   - 成功时：非空串、可被 net.ParseIP 解析、是 IPv4 而非 IPv6（拨的是
//     IPv4 字面量，IPv6 即违背函数语义）、不是未指定/回环地址（否则局域网
//     设备不可达，等于返回了无效地址）；
//   - 失败时：地址必须为空串，供调用方据 err 判定，不允许半有效返回值。
func TestLANIP(t *testing.T) {
	got, err := LANIP()
	if err != nil {
		// 失败契约：出错时地址必须为空串。
		if got != "" {
			t.Fatalf("LANIP() error = %v 时返回了非空地址 %q，应为空串", err, got)
		}
		// 完全断网（无任何对外路由）的环境该函数必然失败：跳过而非失败，
		// 保持默认套件离线可运行；有常规网络时以下断言全部真实执行。
		t.Skipf("本机无可用对外路由（UDP 拨接失败: %v），跳过成功路径断言", err)
	}

	ip := net.ParseIP(got)
	tests := []struct {
		name string
		ok   bool
	}{
		{"非空串", got != ""},
		{"是合法 IP 文本", ip != nil},
		{"是 IPv4 而非 IPv6", ip != nil && ip.To4() != nil},
		{"不是未指定地址（0.0.0.0）", ip != nil && !ip.IsUnspecified()},
		{"不是回环地址", ip != nil && !ip.IsLoopback()},
	}
	for _, tt := range tests {
		if !tt.ok {
			t.Errorf("LANIP() = %q，不满足约束：%s", got, tt.name)
		}
	}
}
