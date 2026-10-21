//go:build !windows

package main

// ensureWebView2Runtime 在非 Windows 平台是空实现：
// macOS 用系统自带的 WKWebView，Linux 由 webkit2gtk 提供，都不需要额外检测。
func ensureWebView2Runtime() {}
