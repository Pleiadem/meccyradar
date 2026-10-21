package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

// singleInstanceID 用于跨进程互斥：重复启动时新进程通知既有实例后自行退出。
const singleInstanceID = "MeccyRadar.SingleInstance"

func main() {
	// Windows 精简版可能没有 WebView2 运行时。必须在 wails.Run 之前检查：
	// 缺了它 Wails 只会抛一条英文技术错误，用户看到的是"双击没反应"。
	ensureWebView2Runtime()

	// Create an instance of the app structure
	app := NewApp()

	// Create application with options
	err := wails.Run(&options.App{
		Title:         "MeccyRadar",
		Width:         760,
		Height:        480,
		MinWidth:      760,
		MinHeight:     480,
		DisableResize: true,
		Frameless:     true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		// 关闭窗口只隐藏窗口，隧道保持连接；真正退出走托盘 / Dock 菜单的「退出」。
		// Windows 隐藏后没有可点入口，必须配合系统托盘（tray_windows.go）。
		HideWindowOnClose: true,
		// 单实例交给框架处理：Windows 与 macOS 行为一致。
		// 之前 macOS 侧是空实现，重复启动真的会起第二个进程，于是第二个进程去登录
		// 拿到 409「已在其他设备运行」——用户看到的就是这个假故障。
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: singleInstanceID,
			OnSecondInstanceLaunch: func(_ options.SecondInstanceData) {
				app.restoreWindow()
			},
		},
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
