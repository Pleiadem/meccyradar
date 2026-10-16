package monitor

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type fakeHeartbeat struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeHeartbeat) Heartbeat(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

func TestMonitorMeasuresThreePingsAndProbesPort(t *testing.T) {
	base := time.Unix(1_000, 0)
	var mu sync.Mutex
	var events []Event
	var dialCalls []string
	hb := &fakeHeartbeat{}
	monitor, err := New(Config{
		ServerAddr:        "node.example",
		ServerPort:        7000,
		LocalAddr:         "127.0.0.1:13133",
		LeaseID:           "lease-1",
		HeartbeatInterval: time.Hour,
		LeaseExpireAtUnix: base.Add(time.Hour).Unix(),
		ExpireAtUnix:      base.Add(time.Hour).Unix(),
		Heartbeater:       hb,
		Now:               func() time.Time { return base },
		DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
			mu.Lock()
			dialCalls = append(dialCalls, address)
			mu.Unlock()
			conn, peer := net.Pipe()
			_ = peer.Close()
			return conn, nil
		},
		Hooks: Hooks{Emit: func(event Event) { mu.Lock(); events = append(events, event); mu.Unlock() }},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := monitor.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := monitor.Stop(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialCalls) != 4 {
		t.Fatalf("expected three server dials and one port probe, got %d (%v)", len(dialCalls), dialCalls)
	}
	if hb.calls != 1 {
		t.Fatalf("expected one heartbeat, got %d", hb.calls)
	}
	var sawPing, sawPort bool
	for _, event := range events {
		sawPing = sawPing || (event.Kind == "tunnel:status" && event.PingMS >= 0)
		sawPort = sawPort || event.Kind == "port:status"
	}
	if !sawPing || !sawPort {
		t.Fatalf("missing expected events: %+v", events)
	}
}

func TestMonitorStopsTunnelOnHeartbeatFailure(t *testing.T) {
	hb := &fakeHeartbeat{err: errors.New("offline")}
	stopped := make(chan error, 1)
	monitor, err := New(Config{
		ServerAddr:   "node.example",
		ServerPort:   7000,
		ExpireAtUnix: time.Now().Add(time.Hour).Unix(),
		LeaseID:      "lease-1",
		Heartbeater:  hb,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			conn, peer := net.Pipe()
			_ = peer.Close()
			return conn, nil
		},
		Hooks: Hooks{StopTunnel: func(err error) { stopped <- err }},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := monitor.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stopped:
		if err == nil || err.Error() != "offline" {
			t.Fatalf("unexpected stop error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat failure did not stop the tunnel")
	}
	deadline := time.Now().Add(time.Second)
	for monitor.Running() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if monitor.Running() {
		t.Fatal("monitor did not stop after heartbeat failure")
	}
	if err := monitor.Stop(); err == nil {
		t.Fatal("monitor should already be stopped after heartbeat failure")
	}
}

func TestMonitorRejectsMissingExpiry(t *testing.T) {
	if _, err := New(Config{ServerAddr: "node.example", ServerPort: 7000}); err == nil {
		t.Fatal("expected missing expiry error")
	}
}
