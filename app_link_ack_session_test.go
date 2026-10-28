package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
)

func TestOldAckResponseCannotClearSameVersionNewCardAndSchedulesNewWorker(t *testing.T) {
	ackAStarted := make(chan struct{})
	releaseA := make(chan struct{})
	ackBSeen := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/client/login":
			var request struct {
				CardKey string `json:"cardKey"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode login: %v", err)
			}
			data := testLinkAuthData(request.CardKey)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": data})
		case "/api/client/logout":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": map[string]any{}})
		case "/api/client/link-ack":
			var request struct {
				LeaseID     string `json:"leaseId"`
				LinkVersion int    `json:"linkVersion"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode ack: %v", err)
			}
			if request.LinkVersion != 2 {
				t.Errorf("unexpected version %d", request.LinkVersion)
			}
			if request.LeaseID == "lease-CARD-A" {
				close(ackAStarted)
				<-releaseA
			} else if request.LeaseID == "lease-CARD-B" {
				close(ackBSeen)
			} else {
				t.Errorf("unexpected lease %q", request.LeaseID)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200, "msg": "ok", "data": map[string]any{"accepted": true},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := auth.NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	store := config.NewMemoryStore()
	app := newApp(appDependencies{client: client, store: store})
	events := make(chan LinkAckEventDTO, 4)
	app.linkAckEmitter = func(event LinkAckEventDTO) { events <- event }
	if _, err := app.LoginByCard("CARD-A"); err != nil {
		t.Fatal(err)
	}
	app.queueLinkAck(2, "lease-CARD-A")
	select {
	case <-ackAStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("card A ack did not enter its controlled network wait")
	}

	if err := app.Logout(); err != nil {
		t.Fatal(err)
	}
	if _, err := app.LoginByCard("CARD-B"); err != nil {
		t.Fatal(err)
	}
	app.queueLinkAck(2, "lease-CARD-B")
	close(releaseA)

	select {
	case <-ackBSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("card B task was not scheduled after the old worker exited")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		app.mu.Lock()
		pending := app.pendingLinkAckVersion
		app.mu.Unlock()
		if pending == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	app.mu.Lock()
	pending, authLease, copyPending := app.pendingLinkAckVersion, app.authData.Session.LeaseID, app.authData.Server.CopyPending
	app.mu.Unlock()
	if pending != 0 || authLease != "lease-CARD-B" || copyPending {
		t.Fatalf("card B ack was lost or stale response altered it: pending=%d lease=%s copyPending=%v", pending, authLease, copyPending)
	}
	session, err := store.Load()
	if err != nil || session.CardKey != "CARD-B" || session.PendingLinkAckVersion != 0 {
		t.Fatalf("stored card B intent is wrong: card=%q version=%d err=%v", session.CardKey, session.PendingLinkAckVersion, err)
	}
	select {
	case event := <-events:
		if !event.Accepted || event.LeaseID != "lease-CARD-B" || event.LinkVersion != 2 {
			t.Fatalf("stale ack event escaped the session fence: %+v", event)
		}
	default:
		t.Fatal("matching card B success must emit an ack event")
	}
}
