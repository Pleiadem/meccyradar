package main

import (
	"strings"
	"testing"
)

// 常见 FRPC 日志中文化。
//
// 只翻译认出来的片段，其余内容（代理名、错误细节）保留：
// 用户既要知道"发生了什么"，也要能看出是哪个代理。

func TestTranslateCommonEngineLogs(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"login to server success, get run id [3dd5d268e800b46d]", "已连接服务器, get run id [3dd5d268e800b46d]"},
		{"[card_abc_tcp] start proxy success", "[card_abc_tcp] 隧道已建立"},
		{"[card_abc_tcp] proxy added", "[card_abc_tcp] 代理已注册"},
		{"heartbeat timeout", "心跳超时"},
		{"client closed", "连接已关闭"},
		{"connection closed", "连接已关闭"},
		{"[card_abc_tcp] start error: connect refused", "[card_abc_tcp] 启动失败: connect refused"},
	}
	for _, tc := range cases {
		got := sanitizeEngineLog(tc.raw)
		if got != tc.want {
			t.Fatalf("%q → 期望 %q，实际 %q", tc.raw, tc.want, got)
		}
	}
}

// 长句必须优先于短句，否则会留下"try to 正在重连服务器"这种残句。
func TestTranslatePrefersLongerMatch(t *testing.T) {
	got := sanitizeEngineLog("try to reconnect to server")
	if got != "正在重连服务器" {
		t.Fatalf("期望 正在重连服务器，实际 %q", got)
	}
	if strings.Contains(got, "try to") {
		t.Fatalf("不该残留英文片段：%q", got)
	}
}

// 翻译要保留级别标记与代理名，不能把整行吞掉。
func TestTranslateKeepsLevelAndProxyName(t *testing.T) {
	got := sanitizeEngineLog("2026-10-22 10:55:55.822 [I] [3dd5d268e800b46d] [card_ed15d227_tcp] start proxy success")
	if !strings.HasPrefix(got, "[I]") {
		t.Fatalf("级别标记要保留，实际 %q", got)
	}
	if !strings.Contains(got, "[card_ed15d227_tcp]") {
		t.Fatalf("代理名要保留，实际 %q", got)
	}
	if !strings.Contains(got, "隧道已建立") {
		t.Fatalf("应翻译成中文，实际 %q", got)
	}
	if strings.Contains(got, "start proxy success") {
		t.Fatalf("英文原文不该残留，实际 %q", got)
	}
}

// 未识别的行必须原样保留 —— 不能因为加了一批翻译就把别的日志也吃掉。
func TestUnrecognizedEngineLogsUnchanged(t *testing.T) {
	unchanged := []string{
		"[card_abc_tcp] tunnel established",
		"some unexpected frpc output",
		"[I] dial tcp 10.0.0.1:7000",
		"登录状态已超时，正在自动重新登录",
	}
	for _, raw := range unchanged {
		if got := sanitizeEngineLog(raw); got != raw {
			t.Fatalf("%q 不该被改动，实际 %q", raw, got)
		}
	}
}
