// Package diagnosis 把隧道启动/运行期的原始错误，翻译成用户能看懂、
// 且知道下一步该做什么的提示。
//
// 为什么需要它：FRPC 与 FRPS 抛出来的都是英文协议文本（"port already in
// use"、"login to server failed: unauthorized"），直接丢给用户等于没提示。
// 但也不能反过来——把所有失败都笼统说成"端口冲突"，那会把人带偏，
// 让用户去查一个跟问题无关的地方。所以这里的规则必须窄，认不出来就
// 老老实实展示原文。
package diagnosis

import "strings"

// Level 决定界面用哪种高亮：warning 用琥珀色，error 用红色。
const (
	LevelWarning = "warning"
	LevelError   = "error"
)

// Diagnosis 是一条可直接渲染的提示。
type Diagnosis struct {
	Level string
	Title string
	Hint  string
	// Raw 保留原始错误，认不出类型时它就是唯一有信息量的内容。
	Raw string
}

type rule struct {
	// keywords 任一命中即算匹配。
	keywords []string
	level    string
	title    string
	hint     string
}

// rules 按顺序匹配，先命中先用。
//
// 顺序很重要：端口占用与卡密占用都会出现 "already in use" 这类词，
// 更具体的规则必须排在前面。
var rules = []rule{
	{
		// 端口占用：FRPC 绑定远端端口失败。
		keywords: []string{
			"port already in use",
			"port is already used",
			"address already in use",
			"bind: address already in use",
			"端口已被占用",
			"port unavailable",
		},
		level: LevelWarning,
		title: "该端口已被占用",
		hint:  "请退出其他设备上的连接后重试",
	},
	{
		// 卡密占用：这是 RentForge 的 409（MSG_CARD_IN_USE），
		// 与端口冲突完全不是一回事，不能混。
		keywords: []string{"已在其他设备", "card already online", "已在其他设备上运行"},
		level:    LevelWarning,
		title:    "卡密已在其他设备运行",
		hint:     "请先退出原设备；若原设备已关闭，等待约 90 秒后重试",
	},
	{
		keywords: []string{"403", "forbidden", "已被禁用", "已被撤销"},
		level:    LevelError,
		title:    "卡密已被禁用",
		hint:     "请联系管理员",
	},
	{
		keywords: []string{"expired", "过期", "已到期"},
		level:    LevelError,
		title:    "授权已到期",
		hint:     "请续费后重试",
	},
	{
		keywords: []string{
			"unauthorized",
			"authorization failed",
			"login to server failed",
			"authentication failed",
			"401 ",
			" 401",
		},
		level: LevelError,
		title: "授权失败",
		hint:  "卡密可能已过期或被禁用，请检查",
	},
	{
		keywords: []string{
			"i/o timeout",
			"connection refused",
			"no such host",
			"network is unreachable",
			"connection reset",
			"dial tcp",
			"网络",
			"超时",
		},
		level: LevelError,
		title: "网络连接失败",
		hint:  "请检查网络后重试",
	},
}

// match 返回第一条命中的规则。
func match(message string) (rule, bool) {
	lowered := strings.ToLower(strings.TrimSpace(message))
	for _, candidate := range rules {
		for _, keyword := range candidate.keywords {
			if strings.Contains(lowered, strings.ToLower(keyword)) {
				return candidate, true
			}
		}
	}
	return rule{}, false
}

// IsRecognized 判断这条消息是否命中已知失败特征。
//
// 日志流用它做闸门：只在真的认出来时才推结构化提示，
// 否则每条普通日志都会弹一个框，反而盖住有用信息。
func IsRecognized(message string) bool {
	_, ok := match(message)
	return ok
}

// Diagnose 把一条原始错误翻译成提示。
//
// 认不出来时返回通用标题 + 原始信息：宁可信息少，也不能给错方向。
func Diagnose(message string) Diagnosis {
	raw := strings.TrimSpace(message)
	if candidate, ok := match(raw); ok {
		return Diagnosis{Level: candidate.level, Title: candidate.title, Hint: candidate.hint, Raw: raw}
	}
	return Diagnosis{Level: LevelError, Title: "连接失败", Hint: "", Raw: raw}
}
