package desktop

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// 局域网关闭时对外地址必须等于回环地址，行为与改造前完全一致。
func TestPublicURLsDefaultToLoopback(t *testing.T) {
	s := DefaultSettings()
	s.ControlPort = 8443
	s.GatewayPort = 8080

	if got, want := s.PublicGatewayURL(), "http://127.0.0.1:8080"; got != want {
		t.Fatalf("PublicGatewayURL = %q，期望 %q", got, want)
	}
	if got, want := s.PublicControlURL(), "http://127.0.0.1:8443"; got != want {
		t.Fatalf("PublicControlURL = %q，期望 %q", got, want)
	}
	if got := s.ListenHostGateway(); got != "127.0.0.1" {
		t.Fatalf("网关监听主机 = %q，期望 127.0.0.1", got)
	}
	if got := s.ListenHostControl(); got != "127.0.0.1" {
		t.Fatalf("控制台监听主机 = %q，期望 127.0.0.1", got)
	}
}

// 局域网开启且配置了地址时，对外地址用配置值；监听主机变 0.0.0.0。
func TestPublicURLsUseConfiguredLANAddress(t *testing.T) {
	s := DefaultSettings()
	s.LanEnabled = true
	s.GatewayPort = 8080
	s.ControlPort = 8443
	s.AdvertisedGatewayURL = "http://10.0.0.5:8080/"

	if got, want := s.PublicGatewayURL(), "http://10.0.0.5:8080"; got != want {
		t.Fatalf("PublicGatewayURL = %q，期望 %q（应去掉尾斜杠）", got, want)
	}
	// 控制台沿用同一个主机，但端口必须是控制台端口。
	if got, want := s.PublicControlURL(), "http://10.0.0.5:8443"; got != want {
		t.Fatalf("PublicControlURL = %q，期望 %q", got, want)
	}
	if got := s.ListenHostGateway(); got != "0.0.0.0" {
		t.Fatalf("网关监听主机 = %q，期望 0.0.0.0", got)
	}
	if got := s.ListenHostControl(); got != "0.0.0.0" {
		t.Fatalf("控制台监听主机 = %q，期望 0.0.0.0", got)
	}
}

// 局域网开启但没配地址时，现算一个可用的局域网 IPv4；算不出来就退回回环。
func TestPublicGatewayWithoutConfiguredAddress(t *testing.T) {
	s := DefaultSettings()
	s.LanEnabled = true
	s.GatewayPort = 8080

	got := s.PublicGatewayURL()
	ip := detectLANIPv4()
	if ip == "" {
		if want := "http://127.0.0.1:8080"; got != want {
			t.Fatalf("无局域网地址时应退回回环，得到 %q", got)
		}
		return
	}
	want := "http://" + ip + ":8080"
	if got != want {
		t.Fatalf("PublicGatewayURL = %q，期望 %q", got, want)
	}
}

// 内部地址（探针、托盘打开控制台用）必须始终是回环，不能跟着局域网开关变。
func TestInternalURLsStayLoopback(t *testing.T) {
	s := DefaultSettings()
	s.LanEnabled = true
	s.AdvertisedGatewayURL = "http://10.0.0.5:8080"

	if got, want := s.ControlURL(), "http://127.0.0.1:8443"; got != want {
		t.Fatalf("ControlURL = %q，期望 %q", got, want)
	}
	if got, want := s.GatewayURL(), "http://127.0.0.1:8080"; got != want {
		t.Fatalf("GatewayURL = %q，期望 %q", got, want)
	}
}

// 虚拟网卡不能当作局域网地址播出去（Docker/WSL 的网段员工连不上）。
func TestVirtualInterfaceDetection(t *testing.T) {
	virtual := []string{
		"docker0", "veth1a2b", "br-abcdef", "virbr0",
		"VMware Network Adapter VMnet8", "VirtualBox Host-Only Network",
		"vEthernet (Default Switch)", "Hyper-V Virtual Ethernet Adapter",
		"utun3", "tailscale0", "ZeroTier One [abc]",
	}
	for _, name := range virtual {
		if !isVirtualInterface(name) {
			t.Errorf("%q 应被判定为虚拟网卡", name)
		}
	}
	// 物理网卡名（含中文 Windows 默认名与 Linux 常见名）不能误判。
	physical := []string{"以太网", "以太网 2", "Ethernet", "Ethernet 2", "WLAN", "Wi-Fi", "eth0", "enp3s0", "wlan0"}
	for _, name := range physical {
		if isVirtualInterface(name) {
			t.Errorf("%q 不应被判定为虚拟网卡", name)
		}
	}
}

// 退避序列：1s 起翻倍、封顶 60s；跑够一分钟的进程重置为 1s。
func TestBackoffFor(t *testing.T) {
	cases := []struct {
		restarts int
		uptime   time.Duration
		want     time.Duration
	}{
		{0, time.Second, time.Second},
		{1, time.Second, 2 * time.Second},
		{2, time.Second, 4 * time.Second},
		{3, time.Second, 8 * time.Second},
		{10, time.Second, time.Minute},
		{100, time.Second, time.Minute},
		{3, 2 * time.Minute, time.Second},
	}
	for _, c := range cases {
		if got := backoffFor(c.restarts, c.uptime); got != c.want {
			t.Errorf("backoffFor(%d, %s) = %s，期望 %s", c.restarts, c.uptime, got, c.want)
		}
	}
}

// Stop 之后不应再重启子进程：否则关机时会留下孤儿进程。
func TestStopPreventsRestart(t *testing.T) {
	sup := NewSupervisor(&Layout{Root: t.TempDir()}, DefaultSettings())
	sup.Stop()

	if !sup.stopping {
		t.Fatal("Stop 之后 stopping 应为 true")
	}
}

// 进程卡死时健康探针必须判定失败（连不上的地址）。
func TestHTTPHealthyRejectsDeadEndpoint(t *testing.T) {
	// 占一个端口再立刻释放，确保这个端口上没人监听。
	listener, port := occupyPort(t)
	listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/healthz"
	if httpHealthy(ctx, url) {
		t.Fatalf("%s 上没有服务，探针不应返回健康", url)
	}
}
