package frpengine

import (
	"testing"

	"meccyradar/internal/auth"

	v1 "github.com/fatedier/frp/pkg/config/v1"
)

func TestBuildConfigCreatesTCPAndHTTPProxyConfig(t *testing.T) {
	common, proxies, err := BuildConfigForTest(auth.FRPConfig{
		ServerAddr: "node.example",
		ServerPort: 7000,
		User:       "card-user",
		Token:      "[REDACTED]",
		Proxies: []auth.Proxy{
			{Name: "radar_tcp", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: 13133, RemotePort: 8000},
			{Name: "radar_http", Type: "http", LocalIP: "127.0.0.1", LocalPort: 13133, CustomDomains: []string{"radar.example"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if common.ServerAddr != "node.example" || common.ServerPort != 7000 {
		t.Fatalf("unexpected common config: %+v", common)
	}
	// The panel authenticates via the plugin, which reads metadata["token"].
	// auth.token must stay empty: a per-user token there fails the server's own
	// global token check ("token in login doesn't match token from configuration").
	if common.Auth.Token != "" || common.Auth.Method != "" {
		t.Fatalf("auth token must not be set, got method=%q token set=%v", common.Auth.Method, common.Auth.Token != "")
	}
	if common.Metadatas["token"] != "[REDACTED]" {
		t.Fatalf("metadata token missing, got %+v", common.Metadatas)
	}
	if len(proxies) != 2 {
		t.Fatalf("expected two proxies, got %d", len(proxies))
	}
	tcp, ok := proxies[0].(*v1.TCPProxyConfig)
	if !ok || tcp.RemotePort != 8000 || tcp.LocalIP != "127.0.0.1" {
		t.Fatalf("unexpected TCP config: %#v", proxies[0])
	}
	httpProxy, ok := proxies[1].(*v1.HTTPProxyConfig)
	if !ok || len(httpProxy.CustomDomains) != 1 || httpProxy.CustomDomains[0] != "radar.example" {
		t.Fatalf("unexpected HTTP config: %#v", proxies[1])
	}
}

func TestBuildConfigRejectsInvalidProxyConfiguration(t *testing.T) {
	cases := []auth.FRPConfig{
		{ServerAddr: "node.example", ServerPort: 7000, Token: "x", Proxies: []auth.Proxy{{Name: "x", Type: "tcp", LocalPort: 1}}},
		{ServerAddr: "node.example", ServerPort: 7000, Token: "x", Proxies: []auth.Proxy{{Name: "x", Type: "http", LocalPort: 1}}},
		{ServerAddr: "node.example", ServerPort: 7000, Token: "x", Proxies: []auth.Proxy{{Name: "x", Type: "udp", LocalPort: 1}}},
	}
	for _, config := range cases {
		if _, _, err := BuildConfigForTest(config); err == nil {
			t.Fatalf("expected invalid config error for %#v", config)
		}
	}
}

func TestEngineStartStopRejectsNetworklessServiceHonestly(t *testing.T) {
	engine, err := New(auth.FRPConfig{
		ServerAddr: "127.0.0.1",
		ServerPort: 1,
		Token:      "[REDACTED]",
		Proxies:    []auth.Proxy{{Name: "radar_tcp", Type: "tcp", LocalPort: 13133, RemotePort: 8000}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if engine.Running() {
		t.Fatal("new engine must not be running")
	}
	if err := engine.Stop(); err != ErrNotRunning {
		t.Fatalf("expected ErrNotRunning, got %v", err)
	}
}
