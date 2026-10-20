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

func TestMonitorMeasuresPingsAndProbesPort(t *testing.T) {
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
	// pingSamples 次测速 + 1 次本地端口探测
	if want := pingSamples + 1; len(dialCalls) != want {
		t.Fatalf("expected %d dials (%d pings + 1 port probe), got %d (%v)", want, pingSamples, len(dialCalls), dialCalls)
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

func TestMonitorStopsTunnelWhenClientExpiryReachesZero(t *testing.T) {
	expired := make(chan Event, 1)
	stopped := make(chan error, 1)
	monitor, err := New(Config{
		ServerAddr:   "node.example",
		ServerPort:   7000,
		ExpireAtUnix: time.Now().Add(-time.Second).Unix(),
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("not reached")
		},
		Hooks: Hooks{
			Emit: func(event Event) {
				if event.Status == StatusExpired {
					expired <- event
				}
			},
			StopTunnel: func(err error) { stopped <- err },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := monitor.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-expired:
		if event.ExpiresIn != 0 {
			t.Fatalf("expected zero remaining time, got %d", event.ExpiresIn)
		}
	case <-time.After(time.Second):
		t.Fatal("expiry event was not emitted")
	}
	select {
	case err := <-stopped:
		if err == nil {
			t.Fatal("expected expiry stop error")
		}
	case <-time.After(time.Second):
		t.Fatal("expiry did not request tunnel stop")
	}
}

func TestMonitorRejectsMissingExpiry(t *testing.T) {
	if _, err := New(Config{ServerAddr: "node.example", ServerPort: 7000}); err == nil {
		t.Fatal("expected missing expiry error")
	}
}

// 租约 TTL（90 秒）是"心跳存活窗口"，不是授权期限。
// 心跳成功即意味着租约被服务端续期，因此授权倒计时不应该拿旧租约到期时间
// 作为上限，否则用户会在登录 90 秒后被误判"授权已到期"。
func TestHeartbeatKeepsAuthorizationAliveBeyondLeaseWindow(t *testing.T) {
	base := time.Unix(1_000, 0)
	const cardExpiry = int64(30 * 24 * 3600) // 卡密 30 天

	var mu sync.Mutex
	var events []Event
	var nowMu sync.Mutex
	now := base
	setNow := func(v time.Time) { nowMu.Lock(); now = v; nowMu.Unlock() }
	clock := func() time.Time { nowMu.Lock(); defer nowMu.Unlock(); return now }

	monitor, err := New(Config{
		ServerAddr:        "node.example",
		ServerPort:        7000,
		LocalAddr:         "127.0.0.1:13133",
		LeaseID:           "lease-1",
		HeartbeatInterval: 10 * time.Millisecond,
		// 登录响应里的租约到期时间：只有 90 秒
		LeaseExpireAtUnix: base.Add(90 * time.Second).Unix(),
		ExpireAtUnix:      base.Unix() + cardExpiry,
		Heartbeater:       &fakeHeartbeat{},
		Now:               clock,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			conn, peer := net.Pipe()
			_ = peer.Close()
			return conn, nil
		},
		Hooks: Hooks{Emit: func(e Event) { mu.Lock(); events = append(events, e); mu.Unlock() }},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := monitor.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = monitor.Stop() }()

	// 心跳一直在成功，把时钟推过旧的租约到期点（90s）但远未到卡密到期
	setNow(base.Add(5 * time.Minute))
	time.Sleep(120 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	for _, event := range events {
		if event.Status == StatusExpired {
			t.Fatalf("心跳正常时不应判定授权到期，但仍收到 expired 事件：%+v", event)
		}
	}
	if !monitor.Running() {
		t.Fatal("心跳正常时监控应继续运行，不应被停止")
	}
}

// 心跳失败说明会话真的失效，此时必须停隧道——这是"不做租约上限"之后
// 防止无限续期的唯一兜底，所以必须直接断言 StopTunnel 被调用，
// 而不是只看有没有发出事件。
func TestHeartbeatFailureStillStopsTunnel(t *testing.T) {
	base := time.Unix(1_000, 0)
	hb := &fakeHeartbeat{err: errors.New("租约已过期")}
	stopped := make(chan error, 1)

	monitor, err := New(Config{
		ServerAddr:        "node.example",
		ServerPort:        7000,
		LocalAddr:         "127.0.0.1:13133",
		LeaseID:           "lease-1",
		HeartbeatInterval: 10 * time.Millisecond,
		// 租约时间给足一年，确保停止只可能来自心跳失败，而不是租约到期
		LeaseExpireAtUnix: base.Add(365 * 24 * time.Hour).Unix(),
		ExpireAtUnix:      base.Add(730 * 24 * time.Hour).Unix(),
		Heartbeater:       hb,
		Now:               func() time.Time { return base },
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
	case stopErr := <-stopped:
		if stopErr == nil {
			t.Fatal("停止隧道时应带上传入的错误")
		}
	case <-time.After(time.Second):
		t.Fatal("心跳失败必须调用 StopTunnel（否则租约失效也不会断开，等于无限续期）")
	}

	// 监控自身也应退出
	deadline := time.Now().Add(time.Second)
	for monitor.Running() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if monitor.Running() {
		t.Fatal("心跳失败后监控应停止运行")
	}
}
