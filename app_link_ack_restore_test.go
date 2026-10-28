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

func TestRestoredPendingAckClearsStateBeforeOrAfterInitSnapshot(t *testing.T) {
	for _, holdAck := range []bool{false, true} {
		name := "ack-before-init"
		if holdAck {
			name = "ack-after-init"
		}
		t.Run(name, func(t *testing.T) {
			ackStarted := make(chan struct{})
			releaseAck := make(chan struct{})
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
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": testLinkAuthData(request.CardKey)})
				case "/api/client/link-ack":
					close(ackStarted)
					if holdAck {
						<-releaseAck
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"code": 200, "msg": "ok", "data": map[string]any{"accepted": true},
					})
				case "/api/client/logout":
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": map[string]any{}})
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
			if err := store.Save(config.Session{
				CardKey: "CARD-A", PendingLinkAckVersion: 2, PendingLinkAckLeaseID: "lease-old",
			}); err != nil {
				t.Fatal(err)
			}
			app := newApp(appDependencies{client: client, store: store})
			events := make(chan LinkAckEventDTO, 1)
			app.linkAckEmitter = func(event LinkAckEventDTO) { events <- event }
			if _, err := app.LoginByCard("CARD-A"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-ackStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("restored ack did not start")
			}
			if holdAck {
				if state := app.GetInitState(); !state.CopyPending {
					t.Fatal("controlled in-flight ack should leave the initialized prompt pending")
				}
				close(releaseAck)
			}
			select {
			case event := <-events:
				if !event.Accepted || event.LeaseID != "lease-CARD-A" || event.LinkVersion != 2 {
					t.Fatalf("unexpected ack event: %+v", event)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("successful restored ack did not publish UI synchronization event")
			}
			if state := app.GetInitState(); state.CopyPending {
				t.Fatal("accepted matching ack must clear the current prompt before/after UI initialization")
			}
		})
	}
}
