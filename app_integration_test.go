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

func TestMockManagementServerLoginLogoutAndConflict(t *testing.T) {
	server := mockserver.New("CARD-1")
	defer server.Close()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	data, err := client.Login(context.Background(), "CARD-1")
	if err != nil {
		t.Fatal(err)
	}
	if data.Session.LeaseID == "" || data.FRPConfig.Proxies[0].RemotePort != 8000 {
		t.Fatalf("unexpected auth data: %+v", data)
	}
	if _, err := client.Login(context.Background(), "CARD-1"); err == nil {
		t.Fatal("expected second login conflict")
	}
	if err := client.Logout(context.Background(), data.Session.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Login(context.Background(), "CARD-1"); err != nil {
		t.Fatalf("expected login after logout, got %v", err)
	}
	login, _, logout := server.Counts()
	if login != 3 || logout != 1 {
		t.Fatalf("unexpected request counts: login=%d logout=%d", login, logout)
	}
}

func TestAppManualLoginDoesNotStartTunnelAndClearsSessionOnLogout(t *testing.T) {
	server := mockserver.New("CARD-2")
	defer server.Close()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	store := config.NewMemoryStore()
	app := newApp(appDependencies{client: client, store: store})
	result, err := app.LoginByCard("CARD-2")
	if err != nil {
		t.Fatal(err)
	}
	if result.TunnelStatus != "idle" || app.GetInitState().TunnelStatus != "idle" {
		t.Fatalf("manual login must remain idle: %+v", result)
	}
	if err := app.Logout(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("expected session to be cleared")
	}
}

func TestAppRestoresCachedSessionAndStartsTunnel(t *testing.T) {
	server := mockserver.New("CARD-3")
	defer server.Close()
	client, err := auth.NewClient(server.URL(), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	store := config.NewMemoryStore()
	if err := store.Save(config.Session{CardKey: "CARD-3"}); err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: store})
	app.restoreCachedSession()
	defer app.stopTunnel()
	state := app.GetInitState()
	if !state.Authenticated {
		t.Fatal("cached session was not restored")
	}
	if app.engine == nil || app.monitor == nil {
		t.Fatal("cached session did not start tunnel and monitoring")
	}
	login, _, _ := server.Counts()
	if login != 1 {
		t.Fatalf("expected one automatic login, got %d", login)
	}
}
