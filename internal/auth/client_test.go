package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoginHeartbeatLogoutAndConflict(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/client/login" {
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req["cardKey"] != "CARD-1" {
				t.Errorf("bad login request: %+v", req)
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": validAuthData()})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": map[string]any{}})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	data, err := client.Login(context.Background(), "CARD-1")
	if err != nil {
		t.Fatal(err)
	}
	if data.FRPConfig.ServerAddr != "node.example" || data.Session.LeaseID != "lease-1" {
		t.Fatalf("unexpected data: %+v", data)
	}
	if err := client.Heartbeat(context.Background(), data.Session.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err := client.Logout(context.Background(), data.Session.LeaseID); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("expected 3 requests, got %v", paths)
	}

	conflict := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "conflict", http.StatusConflict) }))
	defer conflict.Close()
	conflictClient, _ := NewClient(conflict.URL, conflict.Client())
	_, err = conflictClient.Login(context.Background(), "CARD-2")
	var conflictErr *ConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestLoginRejectsInvalidResponseAndUnsafeURL(t *testing.T) {
	if _, err := NewClient("http://example.com", nil); err == nil {
		t.Fatal("expected HTTPS requirement")
	}
	if _, err := NewClient("not-a-url", nil); err == nil {
		t.Fatal("expected URL error")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":200,"msg":"ok","data":{"appName":"bad"}}`))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, server.Client())
	if _, err := client.Login(context.Background(), "CARD-1"); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("expected invalid response, got %v", err)
	}
}

func validAuthData() map[string]any {
	return map[string]any{
		"appName": "MeccyRadar", "shareBaseUrl": "http://radar.example/", "expireAtUnix": 1794736800, "expireAtStr": "2026-11-15 18:00",
		"session":   map[string]any{"leaseId": "lease-1", "heartbeatInterval": 30, "leaseExpireAtUnix": 1794736830},
		"frpConfig": map[string]any{"serverAddr": "node.example", "serverPort": 7000, "user": "user", "token": "secret-token", "proxies": []any{map[string]any{"name": "radar_tcp", "type": "tcp", "localIP": "127.0.0.1", "localPort": 13133, "remotePort": 8000}}},
	}
}
