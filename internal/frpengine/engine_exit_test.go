package frpengine

import (
	"testing"
	"time"

	"meccyradar/internal/auth"
)

func TestEngineNotifiesWhenServiceExits(t *testing.T) {
	exited := make(chan error, 1)
	engine, err := NewWithExit(auth.FRPConfig{
		ServerAddr: "127.0.0.1",
		ServerPort: 1,
		Token:      "token-for-test",
		Proxies:    []auth.Proxy{{Name: "radar_tcp", Type: "tcp", LocalPort: 13133, RemotePort: 8000}},
	}, nil, func(err error) { exited <- err })
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err == nil {
			t.Fatal("expected service exit error")
		}
	case <-time.After(3 * time.Second):
		if engine.Running() {
			_ = engine.Stop()
		}
		t.Fatal("service exit callback was not called")
	}
	if engine.Running() {
		t.Fatal("engine must be stopped after service exit")
	}
}
