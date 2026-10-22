package main

import (
	"net"
	"testing"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
)

// 端口探测必须与隧道解耦：隧道停着的时候也要继续刷新。
// 原来的实现只在 monitor 里探，隧道一停就没人探，界面永远停在旧状态。

func authDataWithProxy(localIP string, localPort, remotePort int) *auth.AuthData {
	return &auth.AuthData{
		ExpireAtUnix: time.Now().Add(time.Hour).Unix(),
		Session:      auth.Session{LeaseID: "lease-1", HeartbeatInterval: 30},
		FRPConfig: auth.FRPConfig{
			ServerAddr: "node.example", ServerPort: 7000,
			User: "u", Token: "t",
			Proxies: []auth.Proxy{{
				Name: "radar_tcp", Type: "tcp",
				LocalIP: localIP, LocalPort: localPort, RemotePort: remotePort,
			}},
		},
	}
}

// 探测地址必须取服务端下发的 localIP:localPort，而不是写死 13133。
func TestRadarLocalAddrUsesServerConfig(t *testing.T) {
	data := authDataWithProxy("127.0.0.1", 13134, 13001)
	if got := radarLocalAddr(data); got != "127.0.0.1:13134" {
		t.Fatalf("应使用服务端下发的端口，实际 %q", got)
	}
}

func TestRadarLocalAddrFallsBack(t *testing.T) {
	cases := []struct {
		name string
		data *auth.AuthData
	}{
		{"未登录", nil},
		{"没有代理", &auth.AuthData{FRPConfig: auth.FRPConfig{}}},
		{"端口非法", authDataWithProxy("127.0.0.1", 0, 13001)},
		{"只有 http 代理", &auth.AuthData{FRPConfig: auth.FRPConfig{
			Proxies: []auth.Proxy{{Name: "radar_http", Type: "http", LocalPort: 13133}},
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := radarLocalAddr(tc.data); got != defaultLocalPortAddr {
				t.Fatalf("应退回默认地址，实际 %q", got)
			}
		})
	}
}

// localIP 为空时按 127.0.0.1 处理，不能拼出 ":13133" 这种非法地址。
func TestRadarLocalAddrDefaultsHost(t *testing.T) {
	data := authDataWithProxy("", 13133, 13001)
	if got := radarLocalAddr(data); got != "127.0.0.1:13133" {
		t.Fatalf("空 localIP 应补 127.0.0.1，实际 %q", got)
	}
}

func newProbeApp(t *testing.T, localPort int) *App {
	t.Helper()
	app := newApp(appDependencies{store: config.NewMemoryStore()})
	app.portProbeInterval = 20 * time.Millisecond
	app.authData = authDataWithProxy("127.0.0.1", localPort, 13001)
	return app
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

// 核心：没有隧道（engine/monitor 都是 nil）也要照常探测、照常刷新。
func TestPortProbeRunsWithoutTunnel(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	app := newProbeApp(t, port)
	if app.tunnelActive() {
		t.Fatal("前提：这个用例不该有隧道在跑")
	}

	app.startPortProbe()
	t.Cleanup(app.stopPortProbe)

	waitFor(t, "端口应被探到已监听", func() bool { return app.portProbeState() })

	// 关掉监听 → 状态必须自己翻成未监听，不需要重启隧道
	_ = listener.Close()
	waitFor(t, "监听关闭后应翻成未监听", func() bool { return !app.portProbeState() })

	// 再开一个新监听 → 又要翻回来
	again, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	app.mu.Lock()
	app.authData = authDataWithProxy("127.0.0.1", again.Addr().(*net.TCPAddr).Port, 13001)
	app.mu.Unlock()
	waitFor(t, "新监听应被探到", func() bool { return app.portProbeState() })
}

// 停止探测必须把状态复位成未监听，否则退出登录后界面还挂着"已监听"。
func TestStopPortProbeResetsState(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	app := newProbeApp(t, listener.Addr().(*net.TCPAddr).Port)
	app.startPortProbe()
	waitFor(t, "先要探到已监听", func() bool { return app.portProbeState() })

	app.stopPortProbe()
	if app.portProbeState() {
		t.Fatal("停止探测后必须复位成未监听")
	}
}

// 重复 start 不能起两个循环。
func TestStartPortProbeIsIdempotent(t *testing.T) {
	app := newProbeApp(t, 13133)
	app.startPortProbe()
	app.mu.Lock()
	first := app.portProbeDone
	app.mu.Unlock()

	app.startPortProbe()
	app.mu.Lock()
	second := app.portProbeDone
	app.mu.Unlock()

	if first != second {
		t.Fatal("重复 start 不该换掉正在跑的循环")
	}
	app.stopPortProbe()
}
