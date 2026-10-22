package main

import (
	"strings"
	"testing"
	"time"
)

// 线上看到的一行（两段时间挤在一起，没有分隔）：
//
//	2026-10-22T10:55:55+08:002026-10-22 10:55:55.822 [I] [3dd5d268e800b46d] [card_xxx_tcp] start proxy success
//
// 前面那段是界面加的 at，后面那段是 FRPC 自带的时间戳。
// 修法：时间只由界面给一次（HH:MM:SS），FRPC 行自带的日期时间和
// run id 在进入界面前就剥掉，`[I]` 保留。

func TestSanitizeEngineLogStripsTimestampAndRunID(t *testing.T) {
	raw := "2026-10-22 10:55:55.822 [I] [3dd5d268e800b46d] [card_xxx_tcp] start proxy success"
	want := "[I] [card_xxx_tcp] start proxy success"
	if got := sanitizeEngineLog(raw); got != want {
		t.Fatalf("期望 %q，实际 %q", want, got)
	}
}

func TestSanitizeEngineLogKeepsLevelMarker(t *testing.T) {
	got := sanitizeEngineLog("2026-10-22 10:55:56.001 [E] [3dd5d268e800b46d] login to server failed")
	if !strings.HasPrefix(got, "[E]") {
		t.Fatalf("级别标记要保留，实际 %q", got)
	}
	if strings.Contains(got, "2026-10-22") {
		t.Fatalf("日期时间必须被剥掉，实际 %q", got)
	}
	if strings.Contains(got, "3dd5d268e800b46d") {
		t.Fatalf("run id 必须被剥掉，实际 %q", got)
	}
}

// 代理名不能被当成 run id 误剥：它是排查问题的关键信息。
func TestSanitizeEngineLogKeepsProxyName(t *testing.T) {
	got := sanitizeEngineLog("2026-10-22 10:55:55.822 [I] [3dd5d268e800b46d] [card_ed15d227_tcp] start proxy success")
	if !strings.Contains(got, "[card_ed15d227_tcp]") {
		t.Fatalf("代理名不能丢，实际 %q", got)
	}
}

// 不是 FRPC 格式的行（例如客户端自己的中文提示）必须原样保留。
func TestSanitizeEngineLogLeavesPlainMessagesAlone(t *testing.T) {
	for _, line := range []string{
		"登录状态已超时，正在自动重新登录",
		"[I] [card_xxx_tcp] start proxy success",
		"隧道连接成功，等待客户端请求",
	} {
		if got := sanitizeEngineLog(line); got != line {
			t.Fatalf("不该改动 %q，实际变成 %q", line, got)
		}
	}
}

// 带 T 分隔符和毫秒变体也要能剥。
func TestSanitizeEngineLogHandlesVariants(t *testing.T) {
	cases := map[string]string{
		"2026-10-22T10:55:55.822 [I] [3dd5d268e800b46d] x": "[I] x",
		"2026-10-22 10:55:55 [W] [3dd5d268e800b46d] y":     "[W] y",
		"2026-10-22 10:55:55.8 [E] z":                      "[E] z",
	}
	for raw, want := range cases {
		if got := sanitizeEngineLog(raw); got != want {
			t.Fatalf("%q → 期望 %q，实际 %q", raw, want, got)
		}
	}
}

// 界面只显示 HH:MM:SS，不再显示完整 RFC3339。
func TestLogClockIsHourMinuteSecond(t *testing.T) {
	at := time.Date(2026, 10, 22, 10, 55, 55, 822_000_000, time.FixedZone("CST", 8*3600))
	got := logClock(at)
	if got != "10:55:55" {
		t.Fatalf("期望 10:55:55，实际 %q", got)
	}
	if len(got) != 8 || strings.Contains(got, "T") || strings.Contains(got, "+") {
		t.Fatalf("时间戳格式不对：%q", got)
	}
}

// 端到端：handleEngineLog 推给界面的 at 是 HH:MM:SS，message 里没有重复时间。
func TestHandleEngineLogEmitsSingleTimestamp(t *testing.T) {
	app := newApp(appDependencies{})
	var captured LogEventDTO
	app.logEmitter = func(event LogEventDTO) { captured = event }

	app.handleEngineLog("2026-10-22 10:55:55.822 [I] [3dd5d268e800b46d] [card_xxx_tcp] start proxy success")

	if len(captured.At) != 8 {
		t.Fatalf("at 必须是 HH:MM:SS，实际 %q", captured.At)
	}
	if strings.Contains(captured.Message, "2026-10-22") {
		t.Fatalf("message 里不该再有日期，实际 %q", captured.Message)
	}
	if !strings.Contains(captured.Message, "start proxy success") {
		t.Fatalf("正文不能丢，实际 %q", captured.Message)
	}
}
