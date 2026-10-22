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
//
// 方括号可选：FRPC 自己不加，但有些日志包装层会写成 [2026-10-22 10:55:55]，
// 两种都得能剥掉，否则后面"英文错误换中文"的规则会因为前缀没清干净而漏匹配。
var frpcTimestamp = regexp.MustCompile(`^\[?\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}(?:\.\d+)?\]?\s*`)

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

// 连接失败的中文业务文案。
//
// FRPC 在登录被拒时只吐英文：
//
//	[E] login to the server failed: <nil>. With loginFailExit enabled,
//	    no additional retries will be attempted
//
// 用户看到的是"连接失败"四个字加一串英文和 <nil>，既不知道是自己卡密
// 到期还是网络问题，也没法据此做任何事。这里统一换成一句能行动的提示。
const msgEngineLoginFailed = "连接服务器失败，请检查授权状态或联系客服"

// engineLogOverrides 把 FRPC 的技术性错误翻成用户能看懂的文案。
//
// 返回 (替换文本, 是否命中)。命中后原行整条丢弃，不再走后面的级别拼接：
// 这些行里没有任何对用户有价值的信息（run id、loginFailExit 之类的
// 内部开关名只会增加困惑）。
var engineLogOverrides = []struct {
	needles  []string
	replacer string
}{
	// 顺序敏感：登录失败那行同时包含 "loginFailExit enabled"，
	// 必须先匹配它，否则会被后面的"纯技术信息丢弃"规则吞掉。
	{
		needles:  []string{"login to the server failed", "login to server failed"},
		replacer: msgEngineLoginFailed,
	},
	{
		// loginFailExit 是我们自己写进配置的开关，属于纯技术细节。
		needles:  []string{"loginfailexit enabled", "no additional retries will be attempted"},
		replacer: "",
	},
}

// engineLogTranslations 把常见的 FRPC 状态行翻成中文。
//
// 与 engineLogOverrides 的区别：这里**保留**原行的其他内容（代理名、错误
// 细节），只把认出来的英文片段替换掉。用户仍能看到是哪个代理、什么原因。
//
// 顺序敏感：长句必须排在短句前面，否则 "try to reconnect to server" 会
// 先被 "reconnect to server" 命中，留下一个孤零零的 "try to 正在重连服务器"。
var engineLogTranslations = []struct {
	needle      string
	replacement string
}{
	{"try to reconnect to server", "正在重连服务器"},
	{"reconnect to server", "重连服务器"},
	{"server response timeout", "服务器响应超时"},
	{"login to server success", "已连接服务器"},
	{"start proxy success", "隧道已建立"},
	{"proxy added", "代理已注册"},
	{"proxy start error", "代理启动失败"},
	{"heartbeat timeout", "心跳超时"},
	{"client closed", "连接已关闭"},
	{"connection closed", "连接已关闭"},
	{"start error", "启动失败"},
	{"connect to server error", "连接服务器出错"},
}

// translateEngineLog 替换认出来的英文片段，返回替换后的文本。
func translateEngineLog(text string) (string, bool) {
	lowered := strings.ToLower(text)
	for _, rule := range engineLogTranslations {
		index := strings.Index(lowered, rule.needle)
		if index < 0 {
			continue
		}
		translated := text[:index] + rule.replacement + text[index+len(rule.needle):]
		return strings.TrimSpace(translated), true
	}
	return "", false
}

// sanitizeEngineLog 去掉 FRPC 行自带的日期时间与 run id，保留级别标记。
//
// 返回值是空字符串表示这一行不该显示给用户（纯技术噪音）。
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

	// 用剥干净的正文做匹配：原始行可能带时间戳和 run id，
	// 直接匹配子串虽然也能命中，但用正文更不容易误伤。
	if replaced, matched := engineLogReplacement(rest); matched {
		return replaced
	}
	// 认出来的状态行翻成中文，其余内容保留。
	if translated, ok := translateEngineLog(rest); ok {
		rest = translated
	}

	if level == "" {
		return rest
	}
	if rest == "" {
		return level
	}
	return level + " " + rest
}

// engineLogReplacement 判断这一行是否要替换成业务文案（或整条丢弃）。
func engineLogReplacement(text string) (string, bool) {
	lowered := strings.ToLower(text)
	for _, rule := range engineLogOverrides {
		for _, needle := range rule.needles {
			if strings.Contains(lowered, needle) {
				return rule.replacer, true
			}
		}
	}
	return "", false
}

// logClock 返回界面展示用的时间戳。
func logClock(now time.Time) string {
	return now.Format(logClockFormat)
}
