package main

import (
	"regexp"
	"strings"
	"time"
)

// 日志时间戳去重。
//
// 线上看到的一行是这样的：
//
//	2026-10-22T10:55:55+08:002026-10-22 10:55:55.822 [I] [3dd5d268e800b46d] [card_xxx_tcp] start proxy success
//
// 两段"时间"挤在一起，中间没有分隔：前面那段是界面自己加的 `at`（RFC3339），
// 后面那段是 FRPC 写进日志行的自带时间。用户根本没法读。
//
// 处理原则：时间只由界面提供一次（HH:MM:SS），FRPC 行自带的日期时间和
// run id 在进入界面之前就剥掉。`[I]/[W]/[E]` 保留——它表示级别，有用。

// frpcTimestamp 匹配 FRPC 写在行首的时间戳：2026-10-22 10:55:55.822
var frpcTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}(?:\.\d+)?\s*`)

// frpcRunID 匹配 FRPC 的 run id：正好 16 位小写十六进制。
//
// 必须严格限定 16 位：代理名 [card_xxx_tcp] 里含有非十六进制字符，
// 放宽匹配会把它也当成 run id 剥掉，日志就丢了最关键的那部分。
var frpcRunID = regexp.MustCompile(`^\[[0-9a-f]{16}\]\s*`)

// frpcLevel 匹配级别标记 [T]/[D]/[I]/[W]/[E]。
//
// 级别要保留（它表示这条是信息还是错误），但它是 run id 的前缀，
// 所以剥 run id 之前必须先跨过它，否则 ^ 锚点对不上。
var frpcLevel = regexp.MustCompile(`^\[[TDIWE]\]\s*`)

// logClockFormat 是界面显示的时间格式。只到秒：日志区一行里出现毫秒
// 除了增加噪音没有别的用处。
const logClockFormat = "15:04:05"

// sanitizeEngineLog 去掉 FRPC 行自带的日期时间与 run id，保留级别标记。
func sanitizeEngineLog(line string) string {
	rest := strings.TrimSpace(line)
	rest = frpcTimestamp.ReplaceAllString(rest, "")

	level := ""
	if match := frpcLevel.FindString(rest); match != "" {
		level = strings.TrimSpace(match)
		rest = rest[len(match):]
	}
	rest = frpcRunID.ReplaceAllString(rest, "")
	rest = strings.TrimSpace(rest)

	if level == "" {
		return rest
	}
	if rest == "" {
		return level
	}
	return level + " " + rest
}

// logClock 返回界面展示用的时间戳。
func logClock(now time.Time) string {
	return now.Format(logClockFormat)
}
