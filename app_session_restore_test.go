package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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

// 同一进程的自动恢复已持有租约时，重复点击登录不得签发第二个租约：
// RentForge 登录会 takeover 旧租约，而现有 monitor 仍会用旧 leaseId 心跳。
func TestManualLoginAfterAutoRestoreDoesNotTakeOverOwnLease(t *testing.T) {
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
	if _, err := app.LoginByCard("CARD-BOTH"); err == nil || !strings.Contains(err.Error(), "当前已登录") {
		t.Fatalf("已有会话时应明确拒绝重复登录，实际 %v", err)
	}
	login, _, _ := server.Counts()
	if login != 1 {
		t.Fatalf("不应为同进程现有会话签发新租约，登录请求数=%d", login)
	}
}

// 并发双击登录不能并发签发两份租约；其中一次成功，另一次明确看到已登录。
func TestConcurrentManualLoginDoesNotIssueDuplicateLease(t *testing.T) {
	server := mockserver.New("CARD-CONCURRENT")
	defer server.Close()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: config.NewMemoryStore()})

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, loginErr := app.LoginByCard("CARD-CONCURRENT")
			errs <- loginErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if !strings.Contains(err.Error(), "当前已登录") {
			t.Fatalf("unexpected concurrent login error: %v", err)
		}
	}
	login, _, _ := server.Counts()
	if login != 1 || server.Takeovers() != 0 || successes < 1 {
		t.Fatalf("expected one local login and no takeover: login=%d takeovers=%d successes=%d", login, server.Takeovers(), successes)
	}
}

// A late caller passes LoginByCard's outer authData check, then is paused until
// the first caller commits and clears loginInFlight. loginOnce must recheck and
// reserve atomically rather than issuing a second server login.
func TestLateCallerRechecksSessionInsideLoginCoordinator(t *testing.T) {
	server := mockserver.New("CARD-LATE-CALLER")
	defer server.Close()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: config.NewMemoryStore()})
	var joinCalls atomic.Int32
	latePrechecked, releaseLate := make(chan struct{}), make(chan struct{})
	app.loginBeforeJoin = func(string) {
		if joinCalls.Add(1) == 2 {
			close(latePrechecked)
			<-releaseLate
		}
	}
	responseArrived, releaseCommit := make(chan struct{}), make(chan struct{})
	app.loginBeforeCommit = func() { close(responseArrived); <-releaseCommit }

	firstDone := make(chan error, 1)
	go func() { _, loginErr := app.LoginByCard("CARD-LATE-CALLER"); firstDone <- loginErr }()
	<-responseArrived
	lateDone := make(chan error, 1)
	go func() { _, loginErr := app.LoginByCard("CARD-LATE-CALLER"); lateDone <- loginErr }()
	<-latePrechecked // The second caller has already passed its outer authData check.
	close(releaseCommit)
	if err := <-firstDone; err != nil {
		t.Fatalf("first login failed: %v", err)
	}
	close(releaseLate)
	if err := <-lateDone; err == nil || !strings.Contains(err.Error(), "当前已登录") {
		t.Fatalf("late caller should observe the committed session, got %v", err)
	}
	login, _, _ := server.Counts()
	if login != 1 || server.Takeovers() != 0 {
		t.Fatalf("late caller issued duplicate login: login=%d takeover=%d", login, server.Takeovers())
	}
}

func waitForLoginWaiter(t *testing.T, app *App) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		app.loginMu.Lock()
		ready := app.loginInFlight != nil && app.loginInFlight.waiters > 0
		app.loginMu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("manual login did not join the in-flight restore request")
}

// The network response has arrived, but commit is deliberately paused. A manual
// login in this gap must join the restore instead of signing a second lease.
func TestRestoreManualLoginCoalesceThroughCommit(t *testing.T) {
	server := mockserver.New("CARD-RESTORE-MANUAL")
	defer server.Close()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	store := config.NewMemoryStore()
	if err := store.Save(config.Session{CardKey: "CARD-RESTORE-MANUAL", RadarPassword: "saved-password"}); err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: store})
	arrived, release := make(chan struct{}), make(chan struct{})
	app.loginBeforeCommit = func() { close(arrived); <-release }

	restoreDone := make(chan struct{})
	go func() { app.restoreCachedSession(); close(restoreDone) }()
	<-arrived
	manualDone := make(chan error, 1)
	go func() { _, loginErr := app.LoginByCard("CARD-RESTORE-MANUAL"); manualDone <- loginErr }()
	waitForLoginWaiter(t, app)
	login, _, _ := server.Counts()
	if login != 1 {
		t.Fatalf("second login escaped in response/commit gap: %d requests", login)
	}
	close(release)
	<-restoreDone
	if err := <-manualDone; err != nil {
		t.Fatalf("coalesced manual login failed: %v", err)
	}
	defer app.stopTunnel()
	if !app.GetInitState().Authenticated || app.RadarPassword() != "saved-password" {
		t.Fatal("restore and manual login did not share the single committed session")
	}
	login, _, _ = server.Counts()
	if login != 1 {
		t.Fatalf("expected one committed login, got %d", login)
	}
}

// If restore has loaded credentials but manual login wins before restore reserves
// its attempt, the expected already-authenticated result must not become a red
// tunnel error event/status. This was the startup/manual completion regression.
func TestRestoreLosingToManualCommitDoesNotEmitErrorStatus(t *testing.T) {
	server := mockserver.New("CARD-MANUAL-WINS")
	defer server.Close()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	store := config.NewMemoryStore()
	if err := store.Save(config.Session{CardKey: "CARD-MANUAL-WINS"}); err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: store})
	defer app.stopTunnel()

	var joins atomic.Int32
	restoreAtJoin, releaseRestore := make(chan struct{}), make(chan struct{})
	app.loginBeforeJoin = func(string) {
		if joins.Add(1) == 1 {
			close(restoreAtJoin)
			<-releaseRestore
		}
	}
	restoreDone := make(chan struct{})
	go func() { app.restoreCachedSession(); close(restoreDone) }()
	<-restoreAtJoin

	if _, err := app.LoginByCard("CARD-MANUAL-WINS"); err != nil {
		t.Fatalf("manual login should win while restore is paused: %v", err)
	}
	close(releaseRestore)
	<-restoreDone

	if state := app.GetInitState(); !state.Authenticated || state.TunnelStatus == "error" {
		t.Fatalf("restore race corrupted authenticated UI state: %+v", state)
	}
	login, _, _ := server.Counts()
	if login != 1 || server.Takeovers() != 0 {
		t.Fatalf("expected one lease and no takeover: login=%d takeovers=%d", login, server.Takeovers())
	}
}

// The reverse ordering (manual login owns the request, restore joins it) also
// commits once and leaves tunnel startup to restoration only.
func TestManualRestoreCoalesceThroughCommit(t *testing.T) {
	server := mockserver.New("CARD-MANUAL-RESTORE")
	defer server.Close()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	store := config.NewMemoryStore()
	if err := store.Save(config.Session{CardKey: "CARD-MANUAL-RESTORE"}); err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: store})
	arrived, release := make(chan struct{}), make(chan struct{})
	app.loginBeforeCommit = func() { close(arrived); <-release }
	manualDone := make(chan error, 1)
	go func() { _, loginErr := app.LoginByCard("CARD-MANUAL-RESTORE"); manualDone <- loginErr }()
	<-arrived
	restoreDone := make(chan struct{})
	go func() { app.restoreCachedSession(); close(restoreDone) }()
	waitForLoginWaiter(t, app)
	login, _, _ := server.Counts()
	if login != 1 {
		t.Fatalf("restore escaped manual login's uncommitted attempt: %d requests", login)
	}
	close(release)
	if err := <-manualDone; err != nil {
		t.Fatalf("manual login owner failed: %v", err)
	}
	<-restoreDone
	defer app.stopTunnel()
	login, _, _ = server.Counts()
	if login != 1 || !app.GetInitState().Authenticated {
		t.Fatalf("expected one committed restore/manual lease: login=%d", login)
	}
}

// Logout invalidates an unresolved login generation; its late response must not
// repopulate authData or the credential store after Logout has completed.
func TestLogoutDuringLoginPreventsLateSessionCommit(t *testing.T) {
	server := mockserver.New("CARD-LOGOUT-RACE")
	defer server.Close()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	store := config.NewMemoryStore()
	if err := store.Save(config.Session{CardKey: "CARD-LOGOUT-RACE"}); err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: store})
	arrived, release := make(chan struct{}), make(chan struct{})
	app.loginBeforeCommit = func() { close(arrived); <-release }
	restoreDone := make(chan struct{})
	go func() { app.restoreCachedSession(); close(restoreDone) }()
	<-arrived
	if err := app.Logout(); err != nil {
		t.Fatalf("logout during unresolved login: %v", err)
	}
	close(release)
	<-restoreDone
	if app.GetInitState().Authenticated {
		t.Fatal("late login response resurrected the logged-out session")
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("late login response repopulated the credential store")
	}
	login, _, logout := server.Counts()
	if login != 1 || logout != 1 {
		t.Fatalf("late issued lease should be released once: login=%d logout=%d", login, logout)
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
	if state := app.GetInitState(); state.TunnelStatus != "error" {
		t.Fatalf("真实恢复错误必须保留为可见错误状态，实际 %+v", state)
	}
	if got := app.SavedCardKey(); got != "CARD-WRONG" {
		t.Fatalf("登录失败后仍应能读到卡密用于预填，实际 %q", got)
	}
	_ = context.Background()
}
