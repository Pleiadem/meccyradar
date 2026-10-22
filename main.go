package main

import (
	"embed"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

// singleInstanceID 用于跨进程互斥：重复启动时新进程通知既有实例后自行退出。
const singleInstanceID = "MeccyRadar.SingleInstance"

// hasAutostartFlag 判断是否由开机自启拉起。
func hasAutostartFlag(args []string) bool {
	for _, arg := range args {
		if arg == autostartFlag {
			return true
		}
	}
	return false
}

func main() {
	// Windows 精简版可能没有 WebView2 运行时。必须在 wails.Run 之前检查：
	// 缺了它 Wails 只会抛一条英文技术错误，用户看到的是"双击没反应"。
	ensureWebView2Runtime()

	// Create an instance of the app structure
	app := NewApp()
	// 开机自启拉起时不弹窗口：直接进托盘并自动登录连隧道。
	// 用 StartHidden 而不是启动后再隐藏——后者会先闪一下窗口。
	app.autostartMode = hasAutostartFlag(os.Args[1:])
	if app.autostartMode {
		// Wails StartHidden 隐藏窗口，但浏览器进程仍可能被创建；
		// startup goroutine 读这个标记后执行自动登录与连接。
		app.autostartMode = true
	}

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
		// 开机自启时不显示窗口，直接进托盘。
		StartHidden: app.autostartMode,
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
