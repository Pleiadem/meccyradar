package main

import (
	"strings"
	"testing"

	"meccyradar/internal/auth"
)

// 默认管理端地址必须是新地址，且必须真的能建出客户端。
//
// 这条测试的价值在于：地址写错（比如漏了端口、协议写错）在编译期
// 完全看不出来，但用户开箱就会连不上。这里把"默认值"和
// "auth.NewClient 是否接受它"绑在一起验证。
func TestDefaultManagementURLIsUsable(t *testing.T) {
	t.Setenv(managementURL, "")

	const want = "http://43.138.163.188:10003/"
	if got := resolveManagementURL(); got != want {
		t.Fatalf("默认管理端地址应为 %q，实际 %q", want, got)
	}

	client, err := auth.NewClient(resolveManagementURL(), nil)
	if err != nil {
		t.Fatalf("默认管理端地址无法建立客户端：%v", err)
	}
	if client == nil {
		t.Fatal("默认管理端地址应能建立客户端")
	}
}

// 环境变量仍然要能覆盖默认值（自测/mock 用）。
func TestManagementURLStillOverridable(t *testing.T) {
	t.Setenv(managementURL, " http://127.0.0.1:18080 ")
	if got := resolveManagementURL(); got != "http://127.0.0.1:18080" {
		t.Fatalf("环境变量覆盖失败，实际 %q", got)
	}
}

// 明文 HTTP 白名单必须窄：只放行自建管理端，其余公网地址仍强制 HTTPS。
func TestPlainHTTPAllowlistIsNarrow(t *testing.T) {
	if !strings.Contains(defaultManagementURL, "43.138.163.188:10003") {
		t.Fatalf("默认地址应指向自建管理端，实际 %q", defaultManagementURL)
	}
	// 白名单里的主机必须允许明文
	if _, err := auth.NewClient(defaultManagementURL, nil); err != nil {
		t.Fatalf("自建管理端应允许明文 HTTP：%v", err)
	}
	// 其他公网地址仍然必须 HTTPS
	if _, err := auth.NewClient("http://example.com", nil); err == nil {
		t.Fatal("公网地址仍应强制 HTTPS")
	}
}
