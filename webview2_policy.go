package main

import "strings"

// webView2VersionUsable 判断注册表里读到的 WebView2 运行时版本号是否可用。
//
// 抽成平台无关的纯函数，这样判定逻辑能在非 Windows 机器上被测到 ——
// 真正的注册表读取只能跑在 Windows 上，而 CI 不是 Windows。
//
// EdgeUpdate 会把已卸载的运行时留成 "0.0.0.0"，所以只看"键存在"不够，
// 必须校验版本号本身。
func webView2VersionUsable(raw string) bool {
	version := strings.TrimSpace(raw)
	if version == "" {
		return false
	}
	if version == "0.0.0.0" {
		return false
	}
	// 必须形如 x.y.z.w，且主版本号大于 0
	head, _, ok := strings.Cut(version, ".")
	if !ok {
		return false
	}
	for _, r := range head {
		if r < '0' || r > '9' {
			return false
		}
	}
	return head != "0" && head != ""
}
