package main

import (
	"net/http"
	"testing"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
	"meccyradar/internal/mockserver"
)

// 托盘「退出」走的路径：先 disconnect 释放租约，再退出进程。
// 验收标准是「退出后立刻重新登录不报『已在其他设备运行』」。
func TestQuitReleasesLeaseSoImmediateReloginSucceeds(t *testing.T) {
	server := mockserver.New("CARD-QUIT")
	defer server.Close()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	store := config.NewMemoryStore()
	app := newApp(appDependencies{client: client, store: store})

	if _, err := app.LoginByCard("CARD-QUIT"); err != nil {
		t.Fatal(err)
	}

	// 模拟托盘点「退出」：ctx 为空时只做断开，不触发 runtime.Quit
	app.Quit()

	_, _, logouts := server.Counts()
	if logouts != 1 {
		t.Fatalf("退出时必须释放租约，实际 logout 调用 %d 次", logouts)
	}

	// 立刻重新登录：必须成功
	if _, err := app.LoginByCard("CARD-QUIT"); err != nil {
		t.Fatalf("退出后立刻重新登录应成功，实际 %v", err)
	}
}

// disconnect 是托盘退出与 OnShutdown 的共同路径，必须同步等到
// logout 请求完成再返回，否则进程可能在请求发出前就退出了。
func TestDisconnectWaitsForLeaseRelease(t *testing.T) {
	server := mockserver.New("CARD-SYNC")
	defer server.Close()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	store := config.NewMemoryStore()
	app := newApp(appDependencies{client: client, store: store})

	if _, err := app.LoginByCard("CARD-SYNC"); err != nil {
		t.Fatal(err)
	}
	if err := app.disconnect(); err != nil {
		t.Fatal(err)
	}
	// disconnect 返回的那一刻，服务端就必须已经收到 logout
	_, _, logouts := server.Counts()
	if logouts != 1 {
		t.Fatalf("disconnect 返回时应已完成租约释放，实际 logout %d 次", logouts)
	}
	// 立刻重新登录必须成功
	if _, err := app.LoginByCard("CARD-SYNC"); err != nil {
		t.Fatalf("重新登录应成功，实际 %v", err)
	}
}

// 未登录时退出不应产生任何 logout 请求（没有租约可释放）。
func TestQuitWithoutSessionDoesNotCallLogout(t *testing.T) {
	server := mockserver.New("CARD-NONE")
	defer server.Close()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: config.NewMemoryStore()})

	app.Quit()

	if _, _, logouts := server.Counts(); logouts != 0 {
		t.Fatalf("未登录不应调用 logout，实际 %d 次", logouts)
	}
}

// 登录后点关闭按钮只能隐藏窗口：隧道要继续跑，租约不能被释放。
func TestCloseWindowKeepsSessionWhenLoggedIn(t *testing.T) {
	server := mockserver.New("CARD-CLOSE")
	defer server.Close()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: config.NewMemoryStore()})
	if _, err := app.LoginByCard("CARD-CLOSE"); err != nil {
		t.Fatal(err)
	}

	if app.shouldQuitOnClose() {
		t.Fatal("登录后关窗应只隐藏，不应退出")
	}
	app.CloseWindow()

	if _, _, logouts := server.Counts(); logouts != 0 {
		t.Fatalf("登录后关窗不得释放租约，实际 logout %d 次", logouts)
	}
	if app.GetInitState().TunnelStatus == "idle" && app.authData == nil {
		t.Fatal("登录后关窗不应清掉会话")
	}
}

// 未登录时点关闭按钮必须真正退出，否则进程会挂在托盘里，
// 下次启动被单实例挡回去，用户看到的是"双击没反应"。
func TestCloseWindowQuitsWhenNotLoggedIn(t *testing.T) {
	server := mockserver.New("CARD-EMPTY")
	defer server.Close()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: config.NewMemoryStore()})

	if !app.shouldQuitOnClose() {
		t.Fatal("未登录时关窗应真正退出")
	}
	app.CloseWindow() // ctx 为空，不会真的调用 runtime.Quit

	if _, _, logouts := server.Counts(); logouts != 0 {
		t.Fatalf("未登录退出不应产生 logout，实际 %d 次", logouts)
	}
}
