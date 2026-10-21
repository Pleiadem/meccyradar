package main

import (
	"context"
	"strings"
	"testing"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
)

func rentForgePayload() *auth.AuthData {
	return &auth.AuthData{
		// 生产实测：RentForge 默认把管理端地址当成分享域名下发。
		ShareBaseURL: "http://127.0.0.1:8766/",
		FRPConfig: auth.FRPConfig{
			ServerAddr: "43.138.163.188",
			ServerPort: 40099,
			Proxies: []auth.Proxy{
				{Name: "card_abc_tcp", Type: "tcp", RemotePort: 13001},
			},
		},
	}
}

// 分享域名必须固定为公网雷达域名，不能被服务端下发的管理端地址带偏。
func TestShareLinkIgnoresServerProvidedManagementAddress(t *testing.T) {
	var copied string
	app := newApp(appDependencies{
		store:     config.NewMemoryStore(),
		clipboard: func(_ context.Context, text string) error { copied = text; return nil },
	})
	app.authData = rentForgePayload()

	if err := app.CopyShareLink("22223333"); err != nil {
		t.Fatal(err)
	}
	want := "http://radar.fastcheat.top/?a=43.138.163.188%3A13001&k=22223333"
	if copied != want {
		t.Fatalf("链接不符：\n得到 %q\n期望 %q", copied, want)
	}
}

func TestShareLinkNeverUsesLoopback(t *testing.T) {
	var copied string
	app := newApp(appDependencies{
		store:     config.NewMemoryStore(),
		clipboard: func(_ context.Context, text string) error { copied = text; return nil },
	})
	app.authData = rentForgePayload()

	if err := app.CopyShareLink("pw"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"127.0.0.1", "localhost", ":8766"} {
		if strings.Contains(copied, bad) {
			t.Fatalf("链接不得包含 %q：%q", bad, copied)
		}
	}
}

// 服务端没给域名时同样要产出可用的公网链接。
func TestShareLinkWorksWithoutServerBaseURL(t *testing.T) {
	var copied string
	app := newApp(appDependencies{
		store:     config.NewMemoryStore(),
		clipboard: func(_ context.Context, text string) error { copied = text; return nil },
	})
	data := rentForgePayload()
	data.ShareBaseURL = ""
	app.authData = data

	if err := app.CopyShareLink("pw"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(copied, "http://radar.fastcheat.top/?") {
		t.Fatalf("应使用固定雷达域名，实际 %q", copied)
	}
}

// 开发/自测允许用环境变量覆盖域名（mock 场景），但默认必须是公网域名。
func TestRadarBaseURLOverride(t *testing.T) {
	t.Setenv(radarBaseURLEnv, "http://radar.example/")
	if got := radarBaseURL(); got != "http://radar.example/" {
		t.Fatalf("环境变量应可覆盖，得到 %q", got)
	}
	t.Setenv(radarBaseURLEnv, "")
	if got := radarBaseURL(); got != defaultRadarBaseURL {
		t.Fatalf("默认应为 %q，得到 %q", defaultRadarBaseURL, got)
	}
}

// 环境变量给了非法值（如管理端地址）时必须回退到固定公网域名。
func TestRadarBaseURLRejectsLoopbackOverride(t *testing.T) {
	t.Setenv(radarBaseURLEnv, "http://127.0.0.1:8766/")
	if got := radarBaseURL(); got != defaultRadarBaseURL {
		t.Fatalf("回环地址不得作为分享域名，得到 %q", got)
	}
}

// 空密码也要能生成链接（k= 留空），由用户自己决定填不填。
// 界面只在密码不足 8 位时把按钮置灰提示，但不再阻止复制。
func TestShareLinkAllowsEmptyPassword(t *testing.T) {
	var copied string
	app := newApp(appDependencies{
		store:     config.NewMemoryStore(),
		clipboard: func(_ context.Context, text string) error { copied = text; return nil },
	})
	app.authData = rentForgePayload()

	if err := app.CopyShareLink(""); err != nil {
		t.Fatalf("空密码不应报错，实际 %v", err)
	}
	want := "http://radar.fastcheat.top/?a=43.138.163.188%3A13001&k="
	if copied != want {
		t.Fatalf("链接不符：\n得到 %q\n期望 %q", copied, want)
	}
}

// 只有空白的密码等同于空密码。
func TestShareLinkAllowsWhitespacePassword(t *testing.T) {
	var copied string
	app := newApp(appDependencies{
		store:     config.NewMemoryStore(),
		clipboard: func(_ context.Context, text string) error { copied = text; return nil },
	})
	app.authData = rentForgePayload()

	if err := app.CopyShareLink("   "); err != nil {
		t.Fatalf("纯空白密码不应报错，实际 %v", err)
	}
	if !strings.Contains(copied, "&k=") {
		t.Fatalf("应生成带 k= 的链接，实际 %q", copied)
	}
}
