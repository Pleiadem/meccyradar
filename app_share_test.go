package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
	"meccyradar/internal/monitor"
)

// RentForge 下发的代理名是 `card_<card_id>_tcp`，不是 `radar_tcp`。
// 只认 radar_tcp 会让真实卡密永远报"未配置 radar_tcp 代理"。
func TestFindRadarProxyAcceptsRentForgeNaming(t *testing.T) {
	proxies := []auth.Proxy{
		{Name: "card_c479a707780113b1a9e0550d_tcp", Type: "tcp", RemotePort: 13000},
	}
	proxy, ok := findRadarProxy(proxies)
	if !ok {
		t.Fatal("RentForge 命名（card_<id>_tcp）必须被识别为雷达代理")
	}
	if proxy.RemotePort != 13000 {
		t.Fatalf("远程端口错误：%d", proxy.RemotePort)
	}
}

func TestFindRadarProxyStillAcceptsMockNaming(t *testing.T) {
	proxies := []auth.Proxy{{Name: "radar_tcp", Type: "tcp", RemotePort: 8000}}
	if _, ok := findRadarProxy(proxies); !ok {
		t.Fatal("mock 的 radar_tcp 仍须可用")
	}
}

func TestFindRadarProxyRejectsUnrelatedProxy(t *testing.T) {
	proxies := []auth.Proxy{{Name: "other", Type: "tcp", RemotePort: 9000}}
	if _, ok := findRadarProxy(proxies); ok {
		t.Fatal("无关代理不得被当作雷达代理")
	}
}

func TestFindRadarProxySkipsNonTCP(t *testing.T) {
	proxies := []auth.Proxy{
		{Name: "card_abc_http", Type: "http", RemotePort: 9000},
		{Name: "card_abc_tcp", Type: "tcp", RemotePort: 13001},
	}
	proxy, ok := findRadarProxy(proxies)
	if !ok || proxy.RemotePort != 13001 {
		t.Fatalf("应选中 tcp 代理，得到 %+v ok=%v", proxy, ok)
	}
}

// 生产链路的完整形状：RentForge 的真实字段。
func TestCopyShareLinkWithRentForgePayload(t *testing.T) {
	var copied string
	app := newApp(appDependencies{
		store:     config.NewMemoryStore(),
		clipboard: func(_ context.Context, text string) error { copied = text; return nil },
	})
	app.authData = &auth.AuthData{
		ShareBaseURL: "http://radar.fastcheat.top/",
		FRPConfig: auth.FRPConfig{
			ServerAddr: "68l.bh17.meccy.fun",
			ServerPort: 7000,
			Proxies: []auth.Proxy{
				{Name: "card_c479a707780113b1a9e0550d_tcp", Type: "tcp", RemotePort: 1017},
			},
		},
	}
	if err := app.CopyShareLink("s3cret"); err != nil {
		t.Fatal(err)
	}
	want := "http://radar.fastcheat.top/?a=68l.bh17.meccy.fun%3A1017&k=s3cret"
	if copied != want {
		t.Fatalf("链接不符：\n得到 %q\n期望 %q", copied, want)
	}
}

// 密码为空时不得产出 `k=` 这种缺参数的链接，必须明确报错。
func TestCopyShareLinkRejectsEmptyPassword(t *testing.T) {
	var copied string
	app := newApp(appDependencies{
		store:     config.NewMemoryStore(),
		clipboard: func(_ context.Context, text string) error { copied = text; return nil },
	})
	app.authData = &auth.AuthData{
		ShareBaseURL: "http://radar.example/",
		FRPConfig: auth.FRPConfig{
			ServerAddr: "node.example",
			Proxies:    []auth.Proxy{{Name: "card_abc_tcp", Type: "tcp", RemotePort: 8000}},
		},
	}
	err := app.CopyShareLink("")
	if err == nil {
		t.Fatal("空密码必须报错，不能生成 k= 的空链接")
	}
	if !strings.Contains(err.Error(), "密码") {
		t.Fatalf("错误信息应提示密码，实际 %q", err.Error())
	}
	if copied != "" {
		t.Fatalf("失败时不应写入剪贴板，实际 %q", copied)
	}
}

// 密码必须被记住：重启后仍能直接复制出带密码的链接。
func TestRadarPasswordIsPersisted(t *testing.T) {
	store := config.NewMemoryStore()
	app := newApp(appDependencies{store: store, clipboard: func(context.Context, string) error { return nil }})
	app.cardKey = "FAST-AAAA-BBBB-CCCC"
	app.authData = &auth.AuthData{
		ShareBaseURL: "http://radar.example/",
		FRPConfig: auth.FRPConfig{
			ServerAddr: "node.example",
			Proxies:    []auth.Proxy{{Name: "card_abc_tcp", Type: "tcp", RemotePort: 8000}},
		},
	}

	if err := app.SaveRadarPassword("hunter2"); err != nil {
		t.Fatal(err)
	}

	// 模拟重启：新实例从同一个 store 读
	restarted := newApp(appDependencies{store: store, clipboard: func(context.Context, string) error { return nil }})
	restarted.cardKey = "FAST-AAAA-BBBB-CCCC"
	if err := restarted.loadSession(); err != nil {
		t.Fatal(err)
	}
	if got := restarted.RadarPassword(); got != "hunter2" {
		t.Fatalf("重启后密码应保留，得到 %q", got)
	}
}

// 已保存的密码应能在不传参时自动补进链接。
func TestCopyShareLinkUsesStoredPasswordWhenArgumentEmpty(t *testing.T) {
	store := config.NewMemoryStore()
	app := newApp(appDependencies{store: store, clipboard: func(context.Context, string) error { return nil }})
	app.cardKey = "FAST-AAAA-BBBB-CCCC"
	app.authData = &auth.AuthData{
		ShareBaseURL: "http://radar.example/",
		FRPConfig: auth.FRPConfig{
			ServerAddr: "node.example",
			Proxies:    []auth.Proxy{{Name: "card_abc_tcp", Type: "tcp", RemotePort: 8000}},
		},
	}
	if err := app.SaveRadarPassword("stored-pw"); err != nil {
		t.Fatal(err)
	}

	var copied string
	app.deps.clipboard = func(_ context.Context, text string) error { copied = text; return nil }
	if err := app.CopyShareLink(""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(copied, "k=stored-pw") {
		t.Fatalf("应使用已保存的密码，实际 %q", copied)
	}
	_ = time.Now
}

// 隧道未运行时点击重测必须给出明确错误，而不是静默无反应。
func TestRemeasurePingWithoutTunnelFails(t *testing.T) {
	app := newApp(appDependencies{store: config.NewMemoryStore()})
	err := app.RemeasurePing()
	if err == nil {
		t.Fatal("隧道未运行时应返回错误")
	}
	if !strings.Contains(err.Error(), "隧道未运行") {
		t.Fatalf("错误应说明隧道未运行，实际 %q", err.Error())
	}
}

// 隧道运行时点击重测必须真的触发一轮测速。
func TestRemeasurePingTriggersMeasurement(t *testing.T) {
	app := newApp(appDependencies{store: config.NewMemoryStore()})

	measured := make(chan int64, 32)
	mon, err := monitor.New(monitor.Config{
		ServerAddr:        "node.example",
		ServerPort:        7000,
		LocalAddr:         "127.0.0.1:13133",
		HeartbeatInterval: time.Hour,
		LeaseExpireAtUnix: time.Now().Add(time.Hour).Unix(),
		ExpireAtUnix:      time.Now().Add(24 * time.Hour).Unix(),
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			conn, peer := net.Pipe()
			_ = peer.Close()
			return conn, nil
		},
		Hooks: monitor.Hooks{
			Emit: func(event monitor.Event) {
				if event.Kind == "tunnel:status" && event.Status == "connected" {
					select {
					case measured <- event.PingMS:
					default:
					}
				}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.monitor = mon

	if err := app.RemeasurePing(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-measured:
		// 收到测速结果即证明重测被触发
	case <-time.After(3 * time.Second):
		t.Fatal("点击重测未触发测速")
	}
}
