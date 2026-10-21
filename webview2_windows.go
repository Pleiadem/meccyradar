//go:build windows

package main

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	// EdgeUpdate 里 WebView2 运行时的固定客户端 GUID。
	webView2ClientGUID  = "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
	webView2DownloadURL = "https://developer.microsoft.com/microsoft-edge/webview2/"
)

var (
	user32Proc      = windows.NewLazySystemDLL("user32.dll")
	messageBoxWProc = user32Proc.NewProc("MessageBoxW")
)

// ensureWebView2Runtime 在 Windows 上确认 WebView2 运行时可用，缺失时给出
// 可操作的中文提示并退出。
//
// 为什么需要：Wails 靠 WebView2 渲染界面，而 Wails 自己不做前置检测。
// 缺少运行时时表现为"双击没反应"或弹一条英文技术错误 —— 用户完全不知道
// 该怎么办。Windows 10/11 精简版（以及部分 LTSC/服务器版）常常没预装。
//
// 为什么用"明确提示"而不是内嵌 bootstrapper / 固定版本运行时：
// 后两者会让单个 exe 膨胀几十到上百 MB；而 WebView2 在新版 Win10/11 上是
// 系统组件，绝大多数机器都有。为少数机器付这个体积不划算，而且内嵌
// bootstrapper 在离线环境下仍然装不上，提示 + 下载地址反而更可靠。
func ensureWebView2Runtime() {
	if webView2Installed() {
		return
	}
	showWebView2MissingDialog()
	os.Exit(1)
}

// webView2Installed 依次检查按机器安装、按用户安装、以及 32 位视图下的注册项。
func webView2Installed() bool {
	candidates := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + webView2ClientGUID},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + webView2ClientGUID},
		{registry.CURRENT_USER, `Software\Microsoft\EdgeUpdate\Clients\` + webView2ClientGUID},
	}
	for _, candidate := range candidates {
		key, err := registry.OpenKey(candidate.root, candidate.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		version, _, err := key.GetStringValue("pv")
		key.Close()
		if err == nil && webView2VersionUsable(version) {
			return true
		}
	}
	return false
}

func showWebView2MissingDialog() {
	const (
		mbIconError     = 0x00000010
		mbSetForeground = 0x00010000
		mbTopmost       = 0x00040000
	)
	text := "检测到系统未安装 Microsoft Edge WebView2 运行时。\n\n" +
		"MeccyRadar 依赖它来显示界面，请先安装后重新打开程序：\n" +
		webView2DownloadURL + "\n\n" +
		"Windows 10/11 精简版、LTSC 与部分服务器版常常没有预装此组件。"
	textPtr, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	captionPtr, err := windows.UTF16PtrFromString("MeccyRadar 无法启动")
	if err != nil {
		return
	}
	_, _, _ = messageBoxWProc.Call(
		0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(captionPtr)),
		uintptr(mbIconError|mbSetForeground|mbTopmost),
	)
}
