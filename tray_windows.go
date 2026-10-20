//go:build windows

package main

import "github.com/getlantern/systray"

// startTray 启动 Windows 系统托盘。
//
// Windows 上关闭窗口只是隐藏（HideWindowOnClose），隐藏后没有 Dock 之类的
// 常驻入口，托盘就是唯一能重新打开窗口的地方，所以必须在启动时拉起。
func (a *App) startTray() {
	go systray.Run(a.onTrayReady, func() {})
}

func (a *App) onTrayReady() {
	systray.SetIcon(buildTrayIcon())
	systray.SetTooltip("MeccyRadar · 隧道保持连接中")

	show := systray.AddMenuItem("显示窗口", "打开 MeccyRadar 窗口")
	systray.AddSeparator()
	quit := systray.AddMenuItem("退出", "断开隧道并退出程序")

	go func() {
		for {
			select {
			case <-show.ClickedCh:
				a.restoreWindow()
			case <-quit.ClickedCh:
				// 先收托盘，再走统一退出路径（断开连接 → 退出应用）
				systray.Quit()
				a.Quit()
				return
			}
		}
	}()
}
