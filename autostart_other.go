//go:build !windows

package main

// 非 Windows 平台不提供开机自启。
//
// 返回 nil 而不是报错：App 层据此把开关显示成"当前系统不支持"，
// 而不是在启动时抛一个用户无法处理的错误。

func newPlatformAutostart() (autostartManager, error) {
	return nil, nil
}
