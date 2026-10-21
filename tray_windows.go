//go:build windows

package main

import (
	"runtime"

	"github.com/getlantern/systray"
)

// startTray 启动 Windows 系统托盘。
//
// Windows 上关闭窗口只是隐藏（HideWindowOnClose），隐藏后没有 Dock 之类的
// 常驻入口，托盘就是唯一能重新打开窗口的地方，所以必须在启动时拉起。
func (a *App) startTray() {
	go func() {
		// 必须锁定 OS 线程，否则托盘菜单点不动。
		//
		// Windows 的消息队列是按线程分的：托盘窗口由本 goroutine 在
		// systray.Run → initInstance 里创建，它的消息就只能由同一个线程
		// 用 GetMessage 取走。库里那句 runtime.LockOSThread() 写在 init() 里，
		// 锁的是主 goroutine，对这里新起的 goroutine 没有任何作用。
		// 不自己锁的话，Go 调度器可能把本 goroutine 迁到别的线程，
		// GetMessage 就永远取不到消息 —— 表现为图标能正常显示
		// （Shell_NotifyIcon 只是一次性调用），但右键菜单不弹出、
		// 点「退出」也没反应。
		runtime.LockOSThread()
		systray.Run(a.onTrayReady, func() {})
	}()
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
