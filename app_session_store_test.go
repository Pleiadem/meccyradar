package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
)

type blockingSessionStore struct {
	inner    *config.MemoryStore
	mu       sync.Mutex
	blockKey string
	started  chan struct{}
	release  chan struct{}
	blocked  bool
}

func newBlockingSessionStore() *blockingSessionStore {
	return &blockingSessionStore{inner: config.NewMemoryStore()}
}

func (s *blockingSessionStore) blockNextSaveFor(key string) (<-chan struct{}, chan<- struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockKey, s.started, s.release, s.blocked = key, make(chan struct{}), make(chan struct{}), false
	return s.started, s.release
}

func (s *blockingSessionStore) Save(session config.Session) error {
	s.mu.Lock()
	block := session.CardKey == s.blockKey && !s.blocked
	if block {
		s.blocked = true
		close(s.started)
		release := s.release
		s.mu.Unlock()
		<-release
		return s.inner.Save(session)
	}
	s.mu.Unlock()
	return s.inner.Save(session)
}

func (s *blockingSessionStore) Load() (config.Session, error) { return s.inner.Load() }
func (s *blockingSessionStore) Clear() error                  { return s.inner.Clear() }

func testLinkAuthData(cardKey string) auth.AuthData {
	return auth.AuthData{
		AppName: "test", ShareBaseURL: "https://radar.example", ExpireAtUnix: time.Now().Add(time.Hour).Unix(),
		Session: auth.Session{LeaseID: "lease-" + cardKey},
		Server:  auth.ServerLinkState{LinkVersion: 2, CopyPending: true},
		FRPConfig: auth.FRPConfig{
			ServerAddr: "radar.example", ServerPort: 7000, Token: "synthetic-token",
			Proxies: []auth.Proxy{{Name: "radar_tcp", Type: "tcp", LocalPort: 13133, RemotePort: 8000}},
		},
	}
}

func sessionLifecycleServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/client/login":
			var request struct {
				CardKey string `json:"cardKey"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode login request: %v", err)
			}
			data := testLinkAuthData(request.CardKey)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": data})
		case "/api/client/logout":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestLogoutClearLinearizesAfterBlockedOldSaveAndNewLogin(t *testing.T) {
	server := sessionLifecycleServer(t)
	defer server.Close()
	client, err := auth.NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	store := newBlockingSessionStore()
	app := newApp(appDependencies{client: client, store: store})
	app.mu.Lock()
	app.cardKey = "CARD-A"
	app.sessionGeneration = 1
	app.authData = &auth.AuthData{Session: auth.Session{LeaseID: "lease-CARD-A"}}
	app.mu.Unlock()

	started, release := store.blockNextSaveFor("CARD-A")
	saveDone := make(chan error, 1)
	go func() {
		saveDone <- app.saveSessionForCurrent(1, "CARD-A")
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("old-generation save did not reach blocking store")
	}

	logoutDone := make(chan error, 1)
	go func() { logoutDone <- app.Logout() }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		app.mu.Lock()
		invalidated := app.sessionGeneration > 1
		app.mu.Unlock()
		if invalidated {
			break
		}
		time.Sleep(time.Millisecond)
	}
	app.mu.Lock()
	invalidated := app.sessionGeneration > 1
	app.mu.Unlock()
	if !invalidated {
		t.Fatal("Logout did not invalidate the old generation before waiting for storage")
	}
	close(release)
	if err := <-saveDone; err != nil {
		t.Fatalf("the already-started save should finish before Logout's Clear: %v", err)
	}
	select {
	case err := <-logoutDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Logout did not finish after the blocked Save was released")
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Logout Clear must win over an old save")
	}

	if _, err := app.LoginByCard("CARD-B"); err != nil {
		t.Fatal(err)
	}
	if err := app.saveSessionForCurrent(1, "CARD-A"); err == nil {
		t.Fatal("stale generation must not overwrite the new card")
	}
	session, err := store.Load()
	if err != nil || session.CardKey != "CARD-B" {
		t.Fatalf("new card credential was overwritten: card=%q err=%v", session.CardKey, err)
	}
}
