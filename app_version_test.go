package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/buildinfo"
	"meccyradar/internal/config"
	"meccyradar/internal/frpengine"
)

type testCredentialStore struct {
	session config.Session
	loads   atomic.Int32
}

func (s *testCredentialStore) Save(v config.Session) error   { s.session = v; return nil }
func (s *testCredentialStore) Load() (config.Session, error) { s.loads.Add(1); return s.session, nil }
func (s *testCredentialStore) Clear() error                  { s.session = config.Session{}; return nil }

func releaseResponse(version string, force bool, configured bool) map[string]any {
	url, notes := "", ""
	if configured {
		url, notes = "https://pan.lanzou.com/share", "release notes"
	}
	return map[string]any{"code": 200, "msg": "ok", "data": auth.VersionInfo{Configured: configured, Version: version, DownloadURL: url, Notes: notes, Force: force}}
}
func versionServer(t *testing.T, handler func(http.ResponseWriter, *http.Request)) (*httptest.Server, *auth.Client) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(handler))
	client, err := auth.NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return server, client
}
func writeRelease(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}
func newVersionTestApp(client *auth.Client, path string) *App {
	app := newApp(appDependencies{client: client})
	app.versionStateFile = path
	return app
}

func TestForcedVersionPersistsAndOfflineRestartBlocks(t *testing.T) {
	buildinfo.Version = "1.0.0"
	path := filepath.Join(t.TempDir(), "state", "version-gate.json")
	server, client := versionServer(t, func(w http.ResponseWriter, _ *http.Request) { writeRelease(w, releaseResponse("1.2.0", true, true)) })
	app := newVersionTestApp(client, path)
	if err := app.CheckVersion(); err == nil {
		t.Fatal("newer forced release should block")
	}
	restores := 0
	app.restoreAfterVersionCheck(func() { restores++ })
	if restores != 0 {
		t.Fatal("startup restored cached session despite authoritative forced version")
	}
	server.Close()
	newClient, err := auth.NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	restarted := newVersionTestApp(newClient, path)
	if err := restarted.CheckVersion(); err == nil || !strings.Contains(err.Error(), "离线") {
		t.Fatalf("expected offline gate, got %v", err)
	}
	state := restarted.VersionState()
	if !state.Blocked || state.RequiredVersion != "1.2.0" || state.DownloadURL != "https://pan.lanzou.com/share" {
		t.Fatalf("persisted force gate not restored: %#v", state)
	}
	if _, err := restarted.LoginByCard("synthetic-card"); err == nil {
		t.Fatal("manual login bypassed force gate")
	}
	if err := restarted.StartTunnel(); err == nil {
		t.Fatal("manual tunnel start bypassed force gate")
	}
	store := &testCredentialStore{session: config.Session{CardKey: "synthetic-card"}}
	restarted.deps.store = store
	restarted.restoreCachedSession()
	if store.loads.Load() != 0 {
		t.Fatal("cached restore accessed credentials while gated")
	}
}

func TestUnknownOfflineAllowsButInvalidResponseFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		blocked bool
	}{
		{"offline", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		}, false},
		{"invalid", func(w http.ResponseWriter, _ *http.Request) {
			writeRelease(w, map[string]any{"code": 200, "msg": "ok", "data": map[string]any{"configured": true, "version": "oops", "downloadUrl": "https://evil.example/x", "notes": "", "force": false}})
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, client := versionServer(t, tc.handler)
			defer server.Close()
			app := newVersionTestApp(client, filepath.Join(t.TempDir(), "state.json"))
			if err := app.CheckVersion(); err == nil {
				t.Fatal("failure or invalid payload must be surfaced")
			}
			state := app.VersionState()
			if state.Blocked != tc.blocked || state.CheckError == "" {
				t.Fatalf("unexpected fail-closed state: %#v", state)
			}
			_, loginErr := app.LoginByCard("synthetic-card")
			if tc.blocked && (loginErr == nil || !strings.Contains(loginErr.Error(), "停止使用")) {
				t.Fatalf("invalid response must block login: %v", loginErr)
			}
			if tc.blocked {
				persisted, readErr := readVersionStateAt(app.versionStateFile)
				if readErr != nil || !persisted.gate.FailClosed {
					t.Fatalf("invalid response quarantine wasn't persisted: %#v %v", persisted, readErr)
				}
				closed := httptest.NewServer(http.NotFoundHandler())
				endpoint := closed.URL
				closed.Close()
				offlineClient, clientErr := auth.NewClient(endpoint, nil)
				if clientErr != nil {
					t.Fatal(clientErr)
				}
				restarted := newVersionTestApp(offlineClient, app.versionStateFile)
				if err := restarted.CheckVersion(); err == nil || !restarted.VersionState().Blocked {
					t.Fatalf("invalid response quarantine did not survive offline restart: %v %#v", err, restarted.VersionState())
				}
				validServer, validClient := versionServer(t, func(w http.ResponseWriter, _ *http.Request) { writeRelease(w, releaseResponse("", false, false)) })
				defer validServer.Close()
				restarted.deps.client = validClient
				if err := restarted.CheckVersion(); err != nil || restarted.VersionState().Blocked {
					t.Fatalf("valid authority could not clear quarantined state: %v %#v", err, restarted.VersionState())
				}
			}
			if !tc.blocked && (loginErr == nil || strings.Contains(loginErr.Error(), "停止使用")) {
				t.Fatalf("unknown network failure should permit login attempt past version gate: %v", loginErr)
			}
			if !tc.blocked {
				restores := 0
				app.restoreAfterVersionCheck(func() { restores++ })
				if restores != 1 {
					t.Fatal("unknown network failure should preserve automatic restore under offline policy A")
				}
			}
		})
	}
}

func TestInvalidDataDoesNotEraseKnownForcedVersionOrDownloadURL(t *testing.T) {
	var invalid atomic.Bool
	server, client := versionServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if invalid.Load() {
			writeRelease(w, map[string]any{"code": 200, "msg": "ok", "data": map[string]any{"configured": true, "version": "bad", "downloadUrl": "https://evil.example/", "force": true}})
			return
		}
		writeRelease(w, releaseResponse("1.8.0", true, true))
	})
	path := filepath.Join(t.TempDir(), "gate.json")
	app := newVersionTestApp(client, path)
	if err := app.CheckVersion(); err == nil {
		t.Fatal("known newer force should block")
	}
	invalid.Store(true)
	if err := app.CheckVersion(); err == nil {
		t.Fatal("invalid response should fail closed")
	}
	state := app.VersionState()
	if !state.Blocked || state.Checking || state.RequiredVersion != "1.8.0" || state.DownloadURL != "https://pan.lanzou.com/share" {
		t.Fatalf("invalid data erased known forced release: %#v", state)
	}
	persisted, err := readVersionStateAt(path)
	if err != nil || persisted.gate.RequiredVersion != "1.8.0" || persisted.gate.DownloadURL != state.DownloadURL || !persisted.gate.FailClosed {
		t.Fatalf("known force and URL weren't persisted: %#v %v", persisted, err)
	}
	server.Close()
	offlineClient, err := auth.NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	restarted := newVersionTestApp(offlineClient, path)
	if err := restarted.CheckVersion(); err == nil {
		t.Fatal("offline restarted app should remain gated")
	}
	restartedState := restarted.VersionState()
	if !restartedState.Blocked || restartedState.RequiredVersion != "1.8.0" || restartedState.DownloadURL != state.DownloadURL {
		t.Fatalf("offline restart lost known forced link: %#v", restartedState)
	}
}

func TestCorruptStateFailsClosedAndAuthoritativeOnlineResponseCanClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	client, err := auth.NewClient(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	app := newVersionTestApp(client, path)
	if err := app.CheckVersion(); err == nil {
		t.Fatal("corrupt persisted state must fail closed")
	}
	if !app.VersionState().Blocked {
		t.Fatal("corrupt persisted state allowed access")
	}
	server, client := versionServer(t, func(w http.ResponseWriter, _ *http.Request) { writeRelease(w, releaseResponse("", false, false)) })
	defer server.Close()
	app.deps.client = client
	if err := app.CheckVersion(); err != nil {
		t.Fatalf("authoritative valid unconfigured response should clear gate: %v", err)
	}
	if app.VersionState().Blocked {
		t.Fatal("valid authoritative response did not clear gate")
	}
	state, err := readVersionStateAt(path)
	if err != nil || state.gate.RequiredVersion != "" {
		t.Fatalf("cleared gate wasn't persisted: %#v %v", state, err)
	}
}

func TestPersistenceFailureBlocksForcedRelease(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	server, client := versionServer(t, func(w http.ResponseWriter, _ *http.Request) { writeRelease(w, releaseResponse("1.5.0", true, true)) })
	defer server.Close()
	app := newVersionTestApp(client, filepath.Join(parent, "gate.json"))
	if err := app.CheckVersion(); err == nil {
		t.Fatal("persistence failure should fail closed")
	}
	if !app.VersionState().Blocked || app.VersionState().RequiredVersion != "1.5.0" {
		t.Fatalf("force requirement was not retained in memory: %#v", app.VersionState())
	}
}

func TestConcurrentChecksSerializeAuthoritativeResults(t *testing.T) {
	var requests atomic.Int32
	entered := make(chan struct{}, 2)
	releaseFirst := make(chan struct{})
	server, client := versionServer(t, func(w http.ResponseWriter, _ *http.Request) {
		n := requests.Add(1)
		entered <- struct{}{}
		if n == 1 {
			<-releaseFirst
			writeRelease(w, releaseResponse("1.8.0", true, true))
			return
		}
		writeRelease(w, releaseResponse("", false, false))
	})
	defer server.Close()
	app := newVersionTestApp(client, filepath.Join(t.TempDir(), "state.json"))
	first := make(chan error, 1)
	go func() { first <- app.CheckVersion() }()
	<-entered
	second := make(chan error, 1)
	go func() { second <- app.CheckVersion() }()
	time.Sleep(20 * time.Millisecond)
	close(releaseFirst)
	if err := <-first; err == nil {
		t.Fatal("first forced configuration should gate")
	}
	if err := <-second; err != nil {
		t.Fatalf("second valid authority should clear gate: %v", err)
	}
	if app.VersionState().Blocked {
		t.Fatal("stale first result overwrote the later authoritative result")
	}
}

func TestStorageReadIOFailureFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory-is-not-a-file")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	app := newVersionTestApp(nil, path)
	if err := app.CheckVersion(); err == nil {
		t.Fatal("read I/O error should be surfaced")
	}
	if !app.VersionState().Blocked {
		t.Fatal("read I/O error must fail closed")
	}
}

func TestBuildVersionInjectedValue(t *testing.T) {
	want := os.Getenv("MECCYRADAR_TEST_EXPECT_APP_VERSION")
	if want == "" {
		want = "1.0.0"
	}
	if buildinfo.Version != want {
		t.Fatalf("build version got %q, want %q", buildinfo.Version, want)
	}
}

func TestSlowVersionCheckDoesNotDelayLogin(t *testing.T) {
	versionRelease := make(chan struct{})
	versionEntered := make(chan struct{}, 1)
	server, client := versionServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/client/login" {
			writeRelease(w, map[string]any{"code": 200, "msg": "ok", "data": versionTestAuthData()})
			return
		}
		versionEntered <- struct{}{}
		<-versionRelease
		writeRelease(w, releaseResponse("", false, false))
	})
	defer server.Close()
	app := newApp(appDependencies{client: client, store: config.NewMemoryStore()})
	app.versionStateFile = filepath.Join(t.TempDir(), "gate.json")
	check := make(chan error, 1)
	go func() { check <- app.CheckVersion() }()
	<-versionEntered
	login := make(chan error, 1)
	go func() { _, err := app.LoginByCard("synthetic-card"); login <- err }()
	select {
	case err := <-login:
		if err != nil {
			t.Fatalf("login failed while only the background check was slow: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("login waited for the slow remote version check")
	}
	close(versionRelease)
	if err := <-check; err != nil {
		t.Fatalf("valid optional response should allow use: %v", err)
	}
}

func TestLoginDoesNotWaitForVersionCheckAndLateResultCannotBypassNewGate(t *testing.T) {
	versionEntered := make(chan struct{}, 1)
	versionRelease := make(chan struct{})
	loginEntered := make(chan struct{}, 1)
	loginRelease := make(chan struct{})
	var loginRequests atomic.Int32
	server, client := versionServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/client/login":
			loginRequests.Add(1)
			loginEntered <- struct{}{}
			<-loginRelease
			writeRelease(w, map[string]any{"code": 200, "msg": "ok", "data": versionTestAuthData()})
		case "/api/client/logout":
			writeRelease(w, map[string]any{"code": 200, "msg": "ok", "data": map[string]any{}})
		default:
			versionEntered <- struct{}{}
			<-versionRelease
			writeRelease(w, releaseResponse("1.9.0", true, true))
		}
	})
	defer server.Close()
	app := newApp(appDependencies{client: client, store: config.NewMemoryStore()})
	app.versionStateFile = filepath.Join(t.TempDir(), "gate.json")
	engine, err := frpengine.New(auth.FRPConfig{ServerAddr: "127.0.0.1", ServerPort: 1, Token: "synthetic-token", Proxies: []auth.Proxy{{Name: "radar_tcp", Type: "tcp", LocalPort: 13133, RemotePort: 8000}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	app.engine = engine
	app.mu.Unlock()
	t.Cleanup(func() {
		if engine.Running() {
			_ = engine.Stop()
		}
	})
	check := make(chan error, 1)
	go func() { check <- app.CheckVersion() }()
	<-versionEntered
	login := make(chan error, 1)
	go func() { _, err := app.LoginByCard("synthetic-card"); login <- err }()
	select {
	case <-loginEntered:
		// Login progressed while the remote version request is still pending.
	case <-time.After(time.Second):
		t.Fatal("login waited on the remote version check")
	}
	close(versionRelease)
	deadline := time.Now().Add(time.Second)
	for !app.IsVersionBlocked() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !app.IsVersionBlocked() {
		t.Fatal("forced gate was not published while login was in flight")
	}
	if engine.Running() {
		t.Fatal("active tunnel was not stopped as soon as the forced result arrived")
	}
	close(loginRelease)
	if err := <-check; err == nil {
		t.Fatal("new forced version should block")
	}
	if err := <-login; err == nil || !strings.Contains(err.Error(), "停止使用") {
		t.Fatalf("late login result bypassed the forced update: %v", err)
	}
	if loginRequests.Load() != 1 {
		t.Fatalf("expected one login request, got %d", loginRequests.Load())
	}
	if app.GetInitState().Authenticated {
		t.Fatal("late login response was committed after the forced gate")
	}
}

func versionTestAuthData() map[string]any {
	return map[string]any{
		"appName": "MeccyRadar", "shareBaseUrl": "http://radar.example/", "expireAtUnix": 1794736800, "expireAtStr": "2026-11-15 18:00",
		"session":   map[string]any{"leaseId": "lease-race", "heartbeatInterval": 30, "leaseExpireAtUnix": 1794736830},
		"frpConfig": map[string]any{"serverAddr": "node.example", "serverPort": 7000, "user": "user", "token": "synthetic-token", "proxies": []any{map[string]any{"name": "radar_tcp", "type": "tcp", "localIP": "127.0.0.1", "localPort": 13133, "remotePort": 8000}}},
	}
}

func TestVersionCheckPendingDoesNotBlockManualLogin(t *testing.T) {
	server, client := versionServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/client/login" {
			t.Fatalf("unexpected request while version check is only pending: %s", r.URL.Path)
		}
		writeRelease(w, map[string]any{"code": 200, "msg": "ok", "data": versionTestAuthData()})
	})
	defer server.Close()
	app := newApp(appDependencies{client: client, store: config.NewMemoryStore()})
	app.mu.Lock()
	app.versionCheckPending = true
	app.mu.Unlock()
	if _, err := app.LoginByCard("synthetic-card"); err != nil {
		t.Fatalf("pending remote check must not gate login: %v", err)
	}
}

func TestLocalForcedGateLoadsSynchronouslyWithoutNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version-gate.json")
	want := versionGateState{RequiredVersion: "1.2.0", DownloadURL: "https://downloads.example/release#install"}
	if err := writeVersionStateAt(path, want); err != nil {
		t.Fatal(err)
	}
	app := newVersionTestApp(nil, path)
	app.loadLocalVersionGate()
	if state := app.VersionState(); !state.Blocked || state.RequiredVersion != want.RequiredVersion {
		t.Fatalf("local gate was not synchronously restored: %#v", state)
	}
	if _, err := app.LoginByCard("synthetic-card"); err == nil {
		t.Fatal("known local forced gate allowed login while offline")
	}
}

func TestAtomicVersionStateReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version-gate.json")
	first := versionGateState{RequiredVersion: "1.2.0", DownloadURL: "https://pan.lanzou.com/one"}
	second := versionGateState{RequiredVersion: "1.3.0", DownloadURL: "https://pan.lanzou.com/two"}
	if err := writeVersionStateAt(path, first); err != nil {
		t.Fatal(err)
	}
	if err := writeVersionStateAt(path, second); err != nil {
		t.Fatalf("replace existing state: %v", err)
	}
	got, err := readVersionStateAt(path)
	if err != nil || got.gate != second {
		t.Fatalf("atomic replacement did not persist new complete state: %#v %v", got, err)
	}
}

func TestInvalidBuildVersionFailsClosed(t *testing.T) {
	previous := buildinfo.Version
	buildinfo.Version = "not-a-semver"
	defer func() { buildinfo.Version = previous }()
	app := newVersionTestApp(nil, filepath.Join(t.TempDir(), "state.json"))
	if err := app.CheckVersion(); err == nil || !strings.Contains(err.Error(), "构建版本无效") {
		t.Fatalf("invalid build version not rejected: %v", err)
	}
	if !app.VersionState().Blocked {
		t.Fatal("invalid build version did not fail closed")
	}
}

func TestStructurallyInvalidVersionStateIsCorruption(t *testing.T) {
	for _, raw := range []string{"{}", "null"} {
		path := filepath.Join(t.TempDir(), "gate.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readVersionStateAt(path); err == nil {
			t.Fatalf("damaged semantic state accepted: %q", raw)
		}
	}
}

func TestMissingVersionStateIsNotCorruption(t *testing.T) {
	state, err := readVersionStateAt(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || state.found {
		t.Fatalf("missing state must be distinguished from damaged state: %#v %v", state, err)
	}
}

func TestOpenVersionDownloadUsesCurrentOptionalOrPersistedForceURL(t *testing.T) {
	previousVersion := buildinfo.Version
	buildinfo.Version = "1.0.0"
	defer func() { buildinfo.Version = previousVersion }()

	openAndCapture := func(t *testing.T, app *App) (string, error) {
		t.Helper()
		app.ctx = context.Background()
		var openedURL string
		err := app.openVersionDownload(func(_ context.Context, url string) { openedURL = url })
		return openedURL, err
	}

	t.Run("optional online release opens info URL", func(t *testing.T) {
		server, client := versionServer(t, func(w http.ResponseWriter, _ *http.Request) { writeRelease(w, releaseResponse("1.2.0", false, true)) })
		defer server.Close()
		app := newVersionTestApp(client, filepath.Join(t.TempDir(), "gate.json"))
		if err := app.CheckVersion(); err != nil {
			t.Fatalf("optional update should not block: %v", err)
		}
		state := app.VersionState()
		if !auth.ValidDownloadURL(state.DownloadURL) {
			t.Fatal("optional update URL missing from snapshot")
		}
		openedURL, err := openAndCapture(t, app)
		if err != nil || openedURL != state.DownloadURL {
			t.Fatalf("optional online download did not use the current info URL: err=%v", err)
		}
	})

	t.Run("forced offline release opens persisted URL", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "gate.json")
		server, client := versionServer(t, func(w http.ResponseWriter, _ *http.Request) { writeRelease(w, releaseResponse("1.2.0", true, true)) })
		app := newVersionTestApp(client, path)
		if err := app.CheckVersion(); err == nil {
			t.Fatal("newer forced release should block")
		}
		server.Close()
		offlineClient, err := auth.NewClient(server.URL, server.Client())
		if err != nil {
			t.Fatal(err)
		}
		restarted := newVersionTestApp(offlineClient, path)
		if err := restarted.CheckVersion(); err == nil {
			t.Fatal("offline restart should retain the force gate")
		}
		if !auth.ValidDownloadURL(restarted.VersionState().DownloadURL) {
			t.Fatal("persisted forced URL missing after offline restart")
		}
		openedURL, err := openAndCapture(t, restarted)
		if err != nil || openedURL != restarted.VersionState().DownloadURL {
			t.Fatalf("persisted forced download did not use the saved URL: err=%v", err)
		}
	})

	t.Run("authoritative unconfigured response clears prior URL", func(t *testing.T) {
		var configured atomic.Bool
		configured.Store(true)
		server, client := versionServer(t, func(w http.ResponseWriter, _ *http.Request) {
			if configured.Load() {
				writeRelease(w, releaseResponse("1.2.0", true, true))
				return
			}
			writeRelease(w, releaseResponse("", false, false))
		})
		defer server.Close()
		app := newVersionTestApp(client, filepath.Join(t.TempDir(), "gate.json"))
		if err := app.CheckVersion(); err == nil {
			t.Fatal("initial forced release should block")
		}
		configured.Store(false)
		if err := app.CheckVersion(); err != nil {
			t.Fatalf("valid unconfigured authority should clear gate: %v", err)
		}
		state := app.VersionState()
		if state.DownloadURL != "" || state.RequiredVersion != "" {
			t.Fatal("unconfigured authority retained a stale download URL")
		}
		openedURL, err := openAndCapture(t, app)
		if err == nil || openedURL != "" {
			t.Fatal("stale download URL was opened after authoritative clear")
		}
	})
}

func TestRequiredVersionBlocksLoginAndTunnel(t *testing.T) {
	app := newApp(appDependencies{})
	app.mu.Lock()
	app.versionGateState = versionGateState{RequiredVersion: "1.1.0", DownloadURL: "https://pan.lanzou.com/share"}
	app.applyVersionGateLocked(app.versionGateState)
	app.mu.Unlock()
	if _, err := app.LoginByCard("synthetic-card"); err == nil || !strings.Contains(err.Error(), "停止使用") {
		t.Fatalf("login must be gated, got %v", err)
	}
	if err := app.StartTunnel(); err == nil || !strings.Contains(err.Error(), "停止使用") {
		t.Fatalf("tunnel must be gated, got %v", err)
	}
}
