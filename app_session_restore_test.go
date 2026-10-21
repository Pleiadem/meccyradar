package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
	"meccyradar/internal/mockserver"
)

// 登录页要能拿到上次保存的卡密做预填，否则自动登录失败时用户只能重打一遍。
func TestSavedCardKeyReturnsStoredKey(t *testing.T) {
	store := config.NewMemoryStore()
	if err := store.Save(config.Session{CardKey: "CARD-SAVED"}); err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{store: store})

	if got := app.SavedCardKey(); got != "CARD-SAVED" {
		t.Fatalf("应返回已保存的卡密，实际 %q", got)
	}
}

func TestSavedCardKeyEmptyWhenNothingStored(t *testing.T) {
	app := newApp(appDependencies{store: config.NewMemoryStore()})
	if got := app.SavedCardKey(); got != "" {
		t.Fatalf("没有保存过时应返回空串，实际 %q", got)
	}
}

// 没有凭据存储时不能崩，返回空串即可。
func TestSavedCardKeyWithoutStore(t *testing.T) {
	app := newApp(appDependencies{})
	if got := app.SavedCardKey(); got != "" {
		t.Fatalf("缺少存储时应返回空串，实际 %q", got)
	}
}

// 端到端：自动登录成功之后，用户在界面上再点一次登录也必须成功。
//
// 这正是线上"每次打开都要重打卡密、还进不去主界面"的完整链路：
// 自动登录已经占了租约，用户重输卡密那一次在旧服务端上必然撞 409。
// 服务端改为 takeover 之后这条链路才通。
func TestManualLoginAfterAutoRestoreSucceeds(t *testing.T) {
	server := mockserver.New("CARD-BOTH")
	defer server.Close()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	store := config.NewMemoryStore()
	if err := store.Save(config.Session{CardKey: "CARD-BOTH"}); err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: store})

	app.restoreCachedSession()
	defer app.stopTunnel()

	if !app.GetInitState().Authenticated {
		t.Fatal("自动登录后后端应处于已登录状态")
	}
	// 界面上再点一次登录（旧版本用户就是这么做的）
	if _, err := app.LoginByCard("CARD-BOTH"); err != nil {
		t.Fatalf("自动登录后再手动登录应成功，实际 %v", err)
	}
}

// 自动登录失败时，界面回到登录页，但卡密必须还能读到（用于预填）。
func TestSavedCardKeySurvivesFailedRestore(t *testing.T) {
	server := mockserver.New("CARD-OTHER")
	defer server.Close()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	store := config.NewMemoryStore()
	// 存了一张服务端不认的卡密：自动登录必然失败
	if err := store.Save(config.Session{CardKey: "CARD-WRONG"}); err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: store})

	app.restoreCachedSession()
	defer app.stopTunnel()

	if app.GetInitState().Authenticated {
		t.Fatal("卡密无效时不应进入已登录状态")
	}
	if got := app.SavedCardKey(); got != "CARD-WRONG" {
		t.Fatalf("登录失败后仍应能读到卡密用于预填，实际 %q", got)
	}
	_ = context.Background()
}
