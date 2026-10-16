package mockserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"meccyradar/internal/auth"
)

type Server struct {
	httpServer *httptest.Server

	mu              sync.Mutex
	cardKey         string
	leaseID         string
	online          bool
	heartbeatErr    bool
	expireAtUnix    int64
	leaseExpireUnix int64
	loginCount      int
	heartbeatCount  int
	logoutCount     int
}

func New(cardKey string) *Server {
	s := &Server{cardKey: cardKey, leaseID: "mock-lease", expireAtUnix: time.Now().Add(time.Hour).Unix(), leaseExpireUnix: time.Now().Add(30 * time.Second).Unix()}
	s.httpServer = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *Server) URL() string { return s.httpServer.URL }
func (s *Server) Close()      { s.httpServer.Close() }

func (s *Server) SetHeartbeatFailure(failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeatErr = failed
}

func (s *Server) SetExpiry(expireAtUnix, leaseExpireUnix int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireAtUnix, s.leaseExpireUnix = expireAtUnix, leaseExpireUnix
}

func (s *Server) Counts() (login, heartbeat, logout int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loginCount, s.heartbeatCount, s.logoutCount
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/client/login":
		s.login(w, r)
	case "/api/client/heartbeat":
		s.heartbeat(w, r)
	case "/api/client/logout":
		s.logout(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var request struct {
		CardKey string `json:"cardKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.CardKey != s.cardKey {
		writeEnvelope(w, http.StatusUnauthorized, "invalid card", nil)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loginCount++
	if s.online {
		writeEnvelope(w, http.StatusConflict, "card already online", nil)
		return
	}
	s.online = true
	writeEnvelope(w, http.StatusOK, "ok", auth.AuthData{
		AppName:      "MeccyRadar Mock",
		ShareBaseURL: "http://radar.example/",
		ExpireAtUnix: s.expireAtUnix,
		ExpireAtStr:  time.Unix(s.expireAtUnix, 0).Format("2006-01-02 15:04"),
		Session:      auth.Session{LeaseID: s.leaseID, HeartbeatInterval: 1, LeaseExpireAtUnix: s.leaseExpireUnix},
		FRPConfig:    auth.FRPConfig{ServerAddr: "127.0.0.1", ServerPort: 7000, User: "mock-user", Token: "mock-token", Proxies: []auth.Proxy{{Name: "radar_tcp", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: 13133, RemotePort: 8000}, {Name: "radar_http", Type: "http", LocalIP: "127.0.0.1", LocalPort: 13133, CustomDomains: []string{"radar.example"}}}},
	})
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeatCount++
	if s.heartbeatErr || !s.online {
		writeEnvelope(w, http.StatusUnauthorized, "lease invalid", nil)
		return
	}
	writeEnvelope(w, http.StatusOK, "ok", map[string]string{})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logoutCount++
	s.online = false
	writeEnvelope(w, http.StatusOK, "ok", map[string]string{})
}

func writeEnvelope(w http.ResponseWriter, status int, message string, data any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": status, "msg": message, "data": data})
}
