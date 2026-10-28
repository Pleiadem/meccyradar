package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
)

func TestCopyAttemptClosesOnClipboardFailureAndPersistsSafeAckRetry(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/client/link-ack" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			LeaseID     string `json:"leaseId"`
			LinkVersion int    `json:"linkVersion"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode ack request: %v", err)
		}
		if request.LeaseID != "lease-current" || request.LinkVersion != 3 {
			t.Errorf("ack bound to wrong session/version")
		}
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "msg": "ok", "data": map[string]any{"accepted": true},
		})
	}))
	defer server.Close()
	client, err := auth.NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	store := config.NewMemoryStore()
	var copied string
	logs := make(chan LogEventDTO, 4)
	app := newApp(appDependencies{
		store:  store,
		client: client,
		clipboard: func(_ context.Context, value string) error {
			copied = value
			return errors.New("clipboard failure for: " + value)
		},
	})
	app.logEmitter = func(event LogEventDTO) { logs <- event }
	app.cardKey = "synthetic-card"
	app.radarPassword = "private-link-password"
	app.authData = &auth.AuthData{
		Session: auth.Session{LeaseID: "lease-current"},
		Server:  auth.ServerLinkState{LinkVersion: 3, CopyPending: true},
		FRPConfig: auth.FRPConfig{
			ServerAddr: "radar.example",
			Proxies:    []auth.Proxy{{Name: "radar_tcp", Type: "tcp", RemotePort: 8123}},
		},
	}

	result, err := app.CopyLinkForDelivery(app.radarPassword, 3)
	if err != nil {
		t.Fatal("clipboard failure should be returned as a handled attempt, not an operation failure")
	}
	if !result.Attempted || result.Copied || result.Link != copied || result.Error == "" {
		t.Fatalf("unexpected safe copy result: attempted=%v copied=%v hasLink=%v hasError=%v", result.Attempted, result.Copied, result.Link != "", result.Error != "")
	}
	if strings.Contains(result.Error, copied) || strings.Contains(result.Error, app.radarPassword) {
		t.Fatal("clipboard error exposed sensitive link data")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("asynchronous version-bound acknowledgement was not sent")
	}
	session, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if session.PendingLinkAckVersion != 3 || session.PendingLinkAckLeaseID != "lease-current" {
		t.Fatalf("ack retry intent was not persisted: version=%d", session.PendingLinkAckVersion)
	}
	select {
	case event := <-logs:
		if strings.Contains(event.Message, copied) || strings.Contains(event.Message, app.radarPassword) {
			t.Fatal("copy log contains sensitive link data")
		}
	default:
		t.Fatal("copy attempt must emit a redacted result event")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session, _ = store.Load()
		if session.PendingLinkAckVersion == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("successful async ack did not clear only its matching pending version")
}

func TestCopyAttemptRejectsStaleInstalledVersionBeforeClipboard(t *testing.T) {
	called := false
	app := newApp(appDependencies{clipboard: func(context.Context, string) error { called = true; return nil }})
	app.authData = &auth.AuthData{
		Session:   auth.Session{LeaseID: "lease-current"},
		Server:    auth.ServerLinkState{LinkVersion: 4},
		FRPConfig: auth.FRPConfig{ServerAddr: "radar.example", Proxies: []auth.Proxy{{Name: "radar_tcp", Type: "tcp", RemotePort: 8000}}},
	}
	_, err := app.CopyLinkForDelivery("", 3)
	if err == nil || called {
		t.Fatal("stale link version must fail before clipboard and ack")
	}
}
