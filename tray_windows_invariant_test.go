package main

import (
	"os"
	"strings"
	"testing"
)

// Windows 托盘能不能点，取决于托盘 goroutine 有没有锁定 OS 线程。
//
// 这个 bug 无法用交叉编译发现，本机也跑不了 Windows 二进制，
// 所以只能把这条不变量钉在源码层面：systray.Run 必须在
// runtime.LockOSThread() 之后调用。
//
// 背景：Windows 消息队列是按线程分的，托盘窗口由该 goroutine 创建，
// 消息也只能由同一线程 GetMessage 取走。库里那句 LockOSThread 在
// init() 里，锁的是主 goroutine，对新起的 goroutine 无效。
func TestTrayGoroutineLocksOSThread(t *testing.T) {
	source, err := os.ReadFile("tray_windows.go")
	if err != nil {
		t.Fatalf("读取 tray_windows.go 失败：%v", err)
	}
	text := string(source)

	lockIndex := strings.Index(text, "runtime.LockOSThread()")
	if lockIndex < 0 {
		t.Fatal("tray_windows.go 必须调用 runtime.LockOSThread()，否则 Windows 托盘菜单点不动")
	}
	runIndex := strings.Index(text, "systray.Run(")
	if runIndex < 0 {
		t.Fatal("tray_windows.go 应调用 systray.Run")
	}
	if lockIndex > runIndex {
		t.Fatal("runtime.LockOSThread() 必须在 systray.Run() 之前调用")
	}
}

// 托盘必须真的把「退出」菜单项接上回调，否则用户点了没反应。
func TestTrayWiresQuitMenuItem(t *testing.T) {
	source, err := os.ReadFile("tray_windows.go")
	if err != nil {
		t.Fatalf("读取 tray_windows.go 失败：%v", err)
	}
	text := string(source)
	for _, want := range []string{`AddMenuItem("退出"`, "quit.ClickedCh", "systray.Quit()"} {
		if !strings.Contains(text, want) {
			t.Fatalf("托盘缺少退出链路：%s", want)
		}
	}
}
