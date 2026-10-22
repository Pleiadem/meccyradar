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
	// 用一行不会被中文翻译规则改写的日志，专门验"剥时间戳/run id"。
	raw := "2026-10-22 10:55:55.822 [I] [3dd5d268e800b46d] [card_xxx_tcp] connection established"
	want := "[I] [card_xxx_tcp] connection established"
	if got := sanitizeEngineLog(raw); got != want {
		t.Fatalf("期望 %q，实际 %q", want, got)
	}
}

func TestSanitizeEngineLogKeepsLevelMarker(t *testing.T) {
	// 用不会被业务文案覆盖的行来验：登录失败那类现在会被翻成中文，
	// 它的"级别保留"已不适用（见 TestSanitizeEngineLogRewritesLoginFailure）。
	got := sanitizeEngineLog("2026-10-22 10:55:56.001 [E] [3dd5d268e800b46d] start error: connect timeout")
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
		"[I] [card_xxx_tcp] connection established",
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

	app.handleEngineLog("2026-10-22 10:55:55.822 [I] [3dd5d268e800b46d] [card_xxx_tcp] connection established")

	if len(captured.At) != 8 {
		t.Fatalf("at 必须是 HH:MM:SS，实际 %q", captured.At)
	}
	if strings.Contains(captured.Message, "2026-10-22") {
		t.Fatalf("message 里不该再有日期，实际 %q", captured.Message)
	}
	if !strings.Contains(captured.Message, "connection established") {
		t.Fatalf("正文不能丢，实际 %q", captured.Message)
	}
}

// 到期后点连接，FRPC 只吐英文，用户完全看不懂。
//
// 原始行（真实格式，带级别与 run id）：
//
//	[E] [3dd5d268e800b46d] login to the server failed: <nil>. With
//	    loginFailExit enabled, no additional retries will be attempted
//
// 这里统一换成一句能行动的提示。

func TestSanitizeEngineLogRewritesLoginFailure(t *testing.T) {
	inputs := []string{
		// 规格里给的格式：方括号包住时间戳
		"[2026-10-22 10:55:55] login to the server failed: <nil>. With loginFailExit enabled, no additional retries will be attempted",
		// FRPC 真实格式：时间戳 + 级别 + run id
		"2026-10-22 10:55:55.822 [E] [3dd5d268e800b46d] login to the server failed: <nil>. With loginFailExit enabled, no additional retries will be attempted",
		// 没有 loginFailExit 尾句的变体
		"2026-10-22 10:55:55.822 [E] [3dd5d268e800b46d] login to the server failed: connection refused",
		// FRPC 另一个措辞：login to server failed
		"[E] login to server failed: dial tcp: i/o timeout",
	}
	for _, input := range inputs {
		if got := sanitizeEngineLog(input); got != msgEngineLoginFailed {
			t.Fatalf("应替换成中文业务文案\n输入: %s\n实际: %q", input, got)
		}
	}
}

// loginFailExit 那行是纯技术细节，不显示给用户。
func TestSanitizeEngineLogDropsLoginFailExitNoise(t *testing.T) {
	inputs := []string{
		"With loginFailExit enabled, no additional retries will be attempted",
		"2026-10-22 10:55:55.822 [I] [3dd5d268e800b46d] With loginFailExit enabled, no additional retries will be attempted",
		"[I] loginFailExit enabled",
	}
	for _, input := range inputs {
		if got := sanitizeEngineLog(input); got != "" {
			t.Fatalf("纯技术信息应被丢弃\n输入: %s\n实际: %q", input, got)
		}
	}
}

// 登录失败那行同时包含 loginFailExit，必须先命中"登录失败"规则，
// 否则用户只会看到一片空白，反而更困惑。
func TestLoginFailureWinsOverLoginFailExitNoise(t *testing.T) {
	line := "login to the server failed: <nil>. With loginFailExit enabled, no additional retries will be attempted"
	if got := sanitizeEngineLog(line); got != msgEngineLoginFailed {
		t.Fatalf("登录失败必须优先于噪音丢弃，实际 %q", got)
	}
}

// 中文业务文案里不该残留英文技术词，也不该带 [E] 前缀。
func TestRewrittenLoginFailureHasNoTechnicalNoise(t *testing.T) {
	got := sanitizeEngineLog("2026-10-22 10:55:55.822 [E] [3dd5d268e800b46d] login to the server failed: <nil>")
	for _, forbidden := range []string{"[E]", "login", "FailExit", "<nil>", "retries", "3dd5d268"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("中文文案里不该出现 %q，实际 %q", forbidden, got)
		}
	}
}

// handleEngineLog 对空结果必须直接丢弃，不能推一条空日志给界面。
func TestHandleEngineLogDropsNoiseLines(t *testing.T) {
	app := newApp(appDependencies{})
	var captured []LogEventDTO
	app.logEmitter = func(event LogEventDTO) { captured = append(captured, event) }

	app.handleEngineLog("With loginFailExit enabled, no additional retries will be attempted")
	if len(captured) != 0 {
		t.Fatalf("技术噪音不该推给界面，实际推了 %d 条：%#v", len(captured), captured)
	}

	app.handleEngineLog("2026-10-22 10:55:55.822 [E] [3dd5d268e800b46d] login to the server failed: <nil>")
	if len(captured) != 1 {
		t.Fatalf("登录失败应推一条，实际 %d 条", len(captured))
	}
	if captured[0].Message != msgEngineLoginFailed {
		t.Fatalf("推给界面的应是中文文案，实际 %q", captured[0].Message)
	}
}
