package dlna

import (
	"context"
	"net"
	"testing"
	"time"
)

// TestListenAlive 在回环接口上自发自收一条 NOTIFY alive，验证常驻监听链路。
// 真实网络的广播可能同时到达，循环过滤只认测试包；
// 组播回环在部分环境不可用，届时跳过。
func TestListenAlive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := ListenAlive(ctx)
	if err != nil {
		t.Skipf("1900 端口不可用（可能被其他应用占用）: %v", err)
	}

	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Skipf("回环 socket 不可用: %v", err)
	}
	defer sender.Close()
	setMulticastIf(sender, net.ParseIP("127.0.0.1"))

	testLoc := "http://127.0.0.1:59999/anydlna-test-dd.xml"
	notify := "NOTIFY * HTTP/1.1\r\n" +
		"HOST: 239.255.255.250:1900\r\n" +
		"NT: urn:schemas-upnp-org:device:MediaRenderer:1\r\n" +
		"NTS: ssdp:alive\r\n" +
		"USN: uuid:anydlna-test-renderer\r\n" +
		"LOCATION: " + testLoc + "\r\n\r\n"
	send := func() {
		_, _ = sender.WriteToUDP([]byte(notify), &net.UDPAddr{IP: net.ParseIP(ssdpIP), Port: ssdpPort})
	}

	// 等待测试包出现（忽略真实网络的广播）。
	recvTest := func(timeout time.Duration) bool {
		deadline := time.After(timeout)
		for {
			select {
			case clue := <-ch:
				if clue.Location == testLoc {
					return true
				}
				continue // 真实网络设备，忽略
			case <-deadline:
				return false
			}
		}
	}
	send()
	if !recvTest(3 * time.Second) {
		t.Skip("回环组播未到达（环境限制），跳过")
	}

	// 同一 LOCATION 的重复广播应被去重：只可能收到真实设备包，不应再收到测试包。
	send()
	if recvTest(1 * time.Second) {
		t.Fatal("重复 LOCATION 未去重")
	}
}

// TestPruneLastDescLocked 覆盖描述地址去重表的清理：
// 过期条目直接删除；仍超限时保留最新的 limit 条、淘汰最旧的。
func TestPruneLastDescLocked(t *testing.T) {
	now := time.Now()
	m := map[string]time.Time{
		"old":     now.Add(-2 * descDedupTTL), // 过期：直接删
		"mid":     now.Add(-time.Minute),
		"fresh":   now,
		"mid-old": now.Add(-2 * time.Minute),
	}
	pruneLastDescLocked(m, now, 2)
	if _, ok := m["old"]; ok {
		t.Error("过期条目应被删除")
	}
	if len(m) != 2 {
		t.Fatalf("超限后应只保留最新的 2 条，实际 %d: %v", len(m), m)
	}
	if _, ok := m["fresh"]; !ok {
		t.Error("最新条目不应被淘汰")
	}
	if _, ok := m["mid"]; !ok {
		t.Error("次新条目不应被淘汰")
	}
	if _, ok := m["mid-old"]; ok {
		t.Error("最旧条目应被淘汰")
	}

	// 未超限时不淘汰：只有过期清理生效。
	m2 := map[string]time.Time{"a": now, "b": now}
	pruneLastDescLocked(m2, now, 2)
	if len(m2) != 2 {
		t.Errorf("未超限不应淘汰: %v", m2)
	}
}
