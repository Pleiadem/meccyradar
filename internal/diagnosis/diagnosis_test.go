package diagnosis

import "testing"

// 端口占用必须是 warning 级，且给出可操作的建议。
func TestPortConflict(t *testing.T) {
	for _, message := range []string{
		"start proxy error: port already in use",
		"listen tcp :13001: bind: address already in use",
		"端口已被占用",
	} {
		got := Diagnose(message)
		if got.Level != LevelWarning {
			t.Fatalf("%q: 端口占用应为 warning，实际 %s", message, got.Level)
		}
		if got.Title != "该端口已被占用" {
			t.Fatalf("%q: 标题应为「该端口已被占用」，实际 %q", message, got.Title)
		}
		if got.Hint == "" {
			t.Fatalf("%q: 必须给出解决建议", message)
		}
	}
}

// 卡密被占用（RentForge 409）与端口占用是两件事，不能混为一谈。
func TestCardInUseIsNotPortConflict(t *testing.T) {
	for _, message := range []string{
		"该卡密已在其他设备上运行，请先退出原设备",
		"card already online",
	} {
		got := Diagnose(message)
		if got.Title == "该端口已被占用" {
			t.Fatalf("%q: 卡密占用被误判为端口冲突", message)
		}
		if got.Title != "卡密已在其他设备运行" {
			t.Fatalf("%q: 期望卡密占用提示，实际 %q", message, got.Title)
		}
	}
}

func TestAuthorizationFailures(t *testing.T) {
	cases := []struct {
		message string
		title   string
	}{
		{"login to server failed: unauthorized", "授权失败"},
		{"401 Unauthorized", "授权失败"},
		{"server rejected: 403 Forbidden", "卡密已被禁用"},
		{"授权已到期，隧道已停止", "授权已到期"},
		{"token expired", "授权已到期"},
	}
	for _, testCase := range cases {
		got := Diagnose(testCase.message)
		if got.Level != LevelError {
			t.Fatalf("%q: 应为 error 级，实际 %s", testCase.message, got.Level)
		}
		if got.Title != testCase.title {
			t.Fatalf("%q: 期望 %q，实际 %q", testCase.message, testCase.title, got.Title)
		}
	}
}

func TestNetworkFailures(t *testing.T) {
	for _, message := range []string{
		"dial tcp 1.2.3.4:7000: i/o timeout",
		"connection refused",
		"no such host",
		"连接服务端超时，请检查网络后重试",
	} {
		got := Diagnose(message)
		if got.Title != "网络连接失败" {
			t.Fatalf("%q: 期望网络失败提示，实际 %q", message, got.Title)
		}
	}
}

// 关键：认不出来的错误必须原样展示，绝不能猜成端口冲突。
func TestUnknownErrorFallsBackToRawMessage(t *testing.T) {
	for _, message := range []string{
		"some completely unrelated failure",
		"代理 \"radar_tcp\" 使用了不支持的类型 \"udp\"",
		"panic: runtime error: index out of range",
	} {
		got := Diagnose(message)
		if got.Title != "连接失败" {
			t.Fatalf("%q: 未知错误应落到通用标题，实际 %q", message, got.Title)
		}
		if got.Raw != message {
			t.Fatalf("%q: 未知错误必须保留原始信息，实际 Raw=%q", message, got.Raw)
		}
		if got.Title == "该端口已被占用" {
			t.Fatalf("%q: 未知错误不得误判为端口冲突", message)
		}
	}
}

// 空消息也要有可展示的内容，不能返回空标题。
func TestEmptyMessage(t *testing.T) {
	got := Diagnose("   ")
	if got.Title == "" {
		t.Fatal("空消息也要给出通用标题")
	}
	if got.Level != LevelError {
		t.Fatalf("空消息应为 error 级，实际 %s", got.Level)
	}
}

// 端口数字里出现 "401" 之类的子串不应被当成授权失败。
func TestPortNumbersDoNotTriggerAuthRule(t *testing.T) {
	got := Diagnose("start proxy error: listen tcp :14019: bind: address already in use")
	if got.Title != "该端口已被占用" {
		t.Fatalf("含 401 子串的端口错误应判为端口占用，实际 %q", got.Title)
	}
}
