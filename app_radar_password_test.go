package main

import (
	"net/http"
	"testing"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
	"meccyradar/internal/mockserver"
)

func radarTestApp(t *testing.T, server *mockserver.Server, store config.CredentialStore) *App {
	t.Helper()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return newApp(appDependencies{client: client, store: store})
}

// 登录时必须把内存里已有的雷达密码一起写盘。
//
// 原先 LoginByCard 只写 CardKey，等于每次登录都把已保存的雷达密码抹掉，
// 用户下次打开还得重新填。
func TestLoginPreservesSavedRadarPassword(t *testing.T) {
	server := mockserver.New("CARD-RP")
	defer server.Close()
	store := config.NewMemoryStore()
	app := radarTestApp(t, server, store)

	// 用户在登录前就先填了雷达密码
	if err := app.SaveRadarPassword("12345678"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.LoginByCard("CARD-RP"); err != nil {
		t.Fatal(err)
	}

	session, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if session.RadarPassword != "12345678" {
		t.Fatalf("登录后应保留雷达密码，实际 %q", session.RadarPassword)
	}
	if session.CardKey != "CARD-RP" {
		t.Fatalf("卡密应同时写入，实际 %q", session.CardKey)
	}
}

// 登录后再填雷达密码，同样要落盘。
func TestSaveRadarPasswordAfterLoginPersists(t *testing.T) {
	server := mockserver.New("CARD-RP2")
	defer server.Close()
	store := config.NewMemoryStore()
	app := radarTestApp(t, server, store)

	if _, err := app.LoginByCard("CARD-RP2"); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveRadarPassword("abcdefgh"); err != nil {
		t.Fatal(err)
	}

	session, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if session.RadarPassword != "abcdefgh" {
		t.Fatalf("应保存雷达密码，实际 %q", session.RadarPassword)
	}
}

// 自动登录（restoreCachedSession）之后，内存里的雷达密码必须被回填，
// 否则界面上是空的、复制出的链接也会缺 k=。
func TestRestoreCachedSessionLoadsRadarPassword(t *testing.T) {
	server := mockserver.New("CARD-RP3")
	defer server.Close()
	store := config.NewMemoryStore()
	if err := store.Save(config.Session{CardKey: "CARD-RP3", RadarPassword: "87654321"}); err != nil {
		t.Fatal(err)
	}
	app := radarTestApp(t, server, store)

	app.restoreCachedSession()
	defer app.stopTunnel()

	if !app.GetInitState().Authenticated {
		t.Fatal("应完成自动登录")
	}
	if got := app.RadarPassword(); got != "87654321" {
		t.Fatalf("自动登录后应回填雷达密码，实际 %q", got)
	}
}

// 雷达密码为空不影响保存与恢复。
func TestEmptyRadarPasswordRoundTrips(t *testing.T) {
	server := mockserver.New("CARD-RP4")
	defer server.Close()
	store := config.NewMemoryStore()
	app := radarTestApp(t, server, store)

	if _, err := app.LoginByCard("CARD-RP4"); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveRadarPassword(""); err != nil {
		t.Fatal(err)
	}
	session, err := store.Load()
	if err != nil {
		t.Fatalf("空雷达密码不应影响保存，实际 %v", err)
	}
	if session.RadarPassword != "" {
		t.Fatalf("应为空，实际 %q", session.RadarPassword)
	}
	if session.CardKey != "CARD-RP4" {
		t.Fatalf("卡密应仍然保存，实际 %q", session.CardKey)
	}

	// 再恢复一次也要成功
	app2 := radarTestApp(t, server, store)
	app2.restoreCachedSession()
	defer app2.stopTunnel()
	if got := app2.RadarPassword(); got != "" {
		t.Fatalf("应回填空串，实际 %q", got)
	}
}

// 模拟"关掉软件再打开"：新实例从磁盘恢复，卡密与雷达密码都要回来。
func TestRestartRestoresBothFields(t *testing.T) {
	server := mockserver.New("CARD-RP5")
	defer server.Close()
	store := config.NewMemoryStore()

	first := radarTestApp(t, server, store)
	if err := first.SaveRadarPassword("pass1234"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.LoginByCard("CARD-RP5"); err != nil {
		t.Fatal(err)
	}

	// 重启：同一份存储，新的 App 实例
	second := radarTestApp(t, server, store)
	second.restoreCachedSession()
	defer second.stopTunnel()

	if got := second.SavedCardKey(); got != "CARD-RP5" {
		t.Fatalf("卡密未恢复，实际 %q", got)
	}
	if got := second.RadarPassword(); got != "pass1234" {
		t.Fatalf("雷达密码未恢复，实际 %q", got)
	}
}
