package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
)

func TestBuildShareLinkEncodesColonAndPassword(t *testing.T) {
	link, err := buildShareLink("http://radar.example/", "node.example", 8000, "a b&c")
	if err != nil {
		t.Fatal(err)
	}
	want := "http://radar.example/?a=node.example%3A8000&k=a+b%26c"
	if link != want {
		t.Fatalf("unexpected link: %q", link)
	}
}

func TestCopyShareLinkUsesOnlyRadarTCPProxy(t *testing.T) {
	var copied string
	app := newApp(appDependencies{
		store:     config.NewMemoryStore(),
		clipboard: func(_ context.Context, text string) error { copied = text; return nil },
	})
	app.authData = &auth.AuthData{
		ShareBaseURL: "http://radar.example/",
		FRPConfig: auth.FRPConfig{
			ServerAddr: "node.example",
			Proxies: []auth.Proxy{
				{Name: "other", Type: "tcp", RemotePort: 9000},
				{Name: "radar_tcp", Type: "tcp", RemotePort: 8000},
			},
		},
	}
	if err := app.CopyShareLink("pwd"); err != nil {
		t.Fatal(err)
	}
	// 域名固定为公网雷达域名（不再取服务端下发的 shareBaseUrl）
	if copied != "http://radar.fastcheat.top/?a=node.example%3A8000&k=pwd" {
		t.Fatalf("unexpected copied link: %q", copied)
	}
}

func TestCopyShareLinkDoesNotFallbackToOtherProxy(t *testing.T) {
	app := newApp(appDependencies{clipboard: func(context.Context, string) error { return nil }})
	app.authData = &auth.AuthData{
		ShareBaseURL: "http://radar.example/",
		FRPConfig:    auth.FRPConfig{ServerAddr: "node.example", Proxies: []auth.Proxy{{Name: "other", Type: "tcp", RemotePort: 9000}}},
	}
	if err := app.CopyShareLink(""); !errors.Is(err, errRadarProxyMissing) {
		t.Fatalf("expected radar proxy error, got %v", err)
	}
}

func TestLoginDTODoesNotExposeSensitiveFRPConfig(t *testing.T) {
	dto := AuthResultDTO{AppName: "MeccyRadar", ExpireAtStr: "future", ExpireAtUnix: 1, TunnelStatus: "idle"}
	text := strings.Join([]string{dto.AppName, dto.ExpireAtStr, dto.TunnelStatus}, " ")
	if strings.Contains(text, "node.example") || strings.Contains(text, "[REDACTED]") || strings.Contains(text, "8000") {
		t.Fatalf("sensitive data leaked into DTO representation: %q", text)
	}
}

// ---------------------------------------------------------------- 服务端地址默认值

func TestResolveManagementURLDefaultsToRealService(t *testing.T) {
	// 默认必须指向真实 RentForge，而不是内置 mock：
	// 连到 mock 时任何真卡都会被判无效，用户只会看到"卡密无效"这种误导性错误。
	t.Setenv(managementURL, "")
	if got := resolveManagementURL(); got != "http://43.138.163.188:10003/" {
		t.Fatalf("默认服务端地址应为真实服务，实际 %q", got)
	}
}

func TestResolveManagementURLHonoursEnv(t *testing.T) {
	t.Setenv(managementURL, " http://127.0.0.1:18080 ")
	if got := resolveManagementURL(); got != "http://127.0.0.1:18080" {
		t.Fatalf("显式设置应优先，实际 %q", got)
	}
}
