package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/monitor"
)

// 网络类心跳失败（超时/断网/DNS）要退避重试 3 次，全失败才停隧道。
//
// 动机：笔记本合盖唤醒、Wi-Fi 切 4G、临时丢包都会让一两次心跳超时。
// 以前第一次超时就把隧道停掉，用户看到的是"莫名其妙掉线"。
// 但业务错误（401/403/409）不能重试：租约失效了再发一百次也是 401，
// 只会把重登时机拖后。

type retryHarness struct {
	monitor *monitor.Monitor
	calls   atomic.Int32
	stopped atomic.Bool
	// mu 保护 logs/messages：它们是 monitor goroutine 写的，测试在读。
	mu       sync.Mutex
	logs     []string
	messages []string
}

func (h *retryHarness) appendLog(message string) {
	h.mu.Lock()
	h.logs = append(h.logs, message)
	h.mu.Unlock()
}

func (h *retryHarness) appendMessage(message string) {
	h.mu.Lock()
	h.messages = append(h.messages, message)
	h.mu.Unlock()
}

func (h *retryHarness) joinedLogs() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.logs, "\n")
}

func newRetryHarness(t *testing.T, beat func(call int32) error) *retryHarness {
	t.Helper()
	// 退避调小：真等 2+4+8 秒会让整包慢十几秒。
	previous := monitor.SetHeartbeatRetryBackoffForTest(time.Millisecond)
	t.Cleanup(func() { monitor.SetHeartbeatRetryBackoffForTest(previous) })

	h := &retryHarness{}
	mon, err := monitor.New(monitor.Config{
		ServerAddr: "node.example", ServerPort: 7000, LocalAddr: "127.0.0.1:1",
		LeaseID: "lease-1", HeartbeatInterval: time.Hour, ExpireAtUnix: 1893456000,
		Heartbeater: heartbeatFunc(func(context.Context, string) (monitor.HeartbeatResult, error) {
			return monitor.HeartbeatResult{}, beat(h.calls.Add(1))
		}),
		Hooks: monitor.Hooks{
			StopTunnel:         func(error) { h.stopped.Store(true) },
			Log:                h.appendLog,
			Emit:               func(e monitor.Event) { h.appendMessage(e.Message) },
			RetryableHeartbeat: isRetryableHeartbeat,
			RecoverLease: func(context.Context, error) (monitor.LeaseRecovery, bool) {
				return monitor.LeaseRecovery{}, false
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.monitor = mon
	return h
}

func (h *retryHarness) start(t *testing.T) {
	t.Helper()
	if err := h.monitor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.monitor.Stop() })
}

func (h *retryHarness) waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s（已调用心跳 %d 次，stopped=%v）", what, h.calls.Load(), h.stopped.Load())
}

func networkErr() error { return &auth.TransportError{Timeout: true, Message: "连接服务端超时，请检查网络后重试"} }

// 心跳超时 → 重试 3 次 → 全失败 → 停止隧道。
func TestHeartbeatTimeoutRetriesThenStops(t *testing.T) {
	h := newRetryHarness(t, func(int32) error { return networkErr() })
	h.start(t)

	h.waitFor(t, func() bool { return h.stopped.Load() }, "三次重试都失败后应停止隧道")

	// 1 次首轮 + N 次重试
	wantCalls := int32(1 + monitor.HeartbeatRetryAttempts())
	if got := h.calls.Load(); got != wantCalls {
		t.Fatalf("应共尝试 %d 次心跳，实际 %d 次", wantCalls, got)
	}
	joined := h.joinedLogs()
	if !strings.Contains(joined, "心跳连续失败") {
		t.Fatalf("停止前应有明确日志，实际日志：\n%s", joined)
	}
}

// 心跳超时 → 重试第 2 次成功 → 隧道继续运行。
func TestHeartbeatTimeoutRecoversOnRetry(t *testing.T) {
	h := newRetryHarness(t, func(call int32) error {
		// 第 1 次（首轮）失败，第 2 次（重试一）失败，第 3 次（重试二）成功
		if call <= 2 {
			return networkErr()
		}
		return nil
	})
	h.start(t)

	h.waitFor(t, func() bool { return h.calls.Load() >= 3 }, "应重试到第 2 次")

	// 给一点时间让"若失败则停止"的路径有机会触发
	time.Sleep(150 * time.Millisecond)
	if h.stopped.Load() {
		t.Fatal("重试成功时不能停止隧道")
	}
	if !strings.Contains(h.joinedLogs(), "心跳已恢复") {
		t.Fatalf("恢复后应有日志，实际：%v", h.joinedLogs())
	}
}

// 心跳 401 + ttl → 不走重试，直接交给重登。
func TestHeartbeatUnauthorizedSkipsRetry(t *testing.T) {
	var recovered atomic.Bool
	previous := monitor.SetHeartbeatRetryBackoffForTest(time.Millisecond)
	t.Cleanup(func() { monitor.SetHeartbeatRetryBackoffForTest(previous) })

	var calls atomic.Int32
	mon, err := monitor.New(monitor.Config{
		ServerAddr: "node.example", ServerPort: 7000, LocalAddr: "127.0.0.1:1",
		LeaseID: "lease-1", HeartbeatInterval: time.Hour, ExpireAtUnix: 1893456000,
		Heartbeater: heartbeatFunc(func(context.Context, string) (monitor.HeartbeatResult, error) {
			calls.Add(1)
			return monitor.HeartbeatResult{}, &auth.StatusError{Status: 401, Reason: auth.LeaseReasonTTL, Message: "登录状态已超时"}
		}),
		Hooks: monitor.Hooks{
			RetryableHeartbeat: isRetryableHeartbeat,
			RecoverLease: func(context.Context, error) (monitor.LeaseRecovery, bool) {
				recovered.Store(true)
				return monitor.LeaseRecovery{LeaseID: "lease-2"}, true
			},
			StopTunnel: func(error) { t.Error("重登成功时不应停止隧道") },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mon.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mon.Stop() }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !recovered.Load() {
		time.Sleep(5 * time.Millisecond)
	}
	if !recovered.Load() {
		t.Fatal("401 应直接走重登，实际没触发")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("业务错误不该重试，应只发 1 次心跳，实际 %d 次", got)
	}
}

// 心跳 401 + takeover → 不走重试，直接停止。
func TestHeartbeatTakeoverSkipsRetry(t *testing.T) {
	previous := monitor.SetHeartbeatRetryBackoffForTest(time.Millisecond)
	t.Cleanup(func() { monitor.SetHeartbeatRetryBackoffForTest(previous) })

	var calls atomic.Int32
	var stopped atomic.Bool
	mon, err := monitor.New(monitor.Config{
		ServerAddr: "node.example", ServerPort: 7000, LocalAddr: "127.0.0.1:1",
		LeaseID: "lease-1", HeartbeatInterval: time.Hour, ExpireAtUnix: 1893456000,
		Heartbeater: heartbeatFunc(func(context.Context, string) (monitor.HeartbeatResult, error) {
			calls.Add(1)
			return monitor.HeartbeatResult{}, &auth.StatusError{Status: 401, Reason: auth.LeaseReasonTakeover, Message: "该卡密已在其他设备登录，本机已停止"}
		}),
		Hooks: monitor.Hooks{
			RetryableHeartbeat: isRetryableHeartbeat,
			RecoverLease: func(context.Context, error) (monitor.LeaseRecovery, bool) {
				return monitor.LeaseRecovery{}, false
			},
			StopTunnel: func(error) { stopped.Store(true) },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mon.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mon.Stop() }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !stopped.Load() {
		time.Sleep(5 * time.Millisecond)
	}
	if !stopped.Load() {
		t.Fatal("被顶掉时必须停止隧道")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("takeover 不该重试，应只发 1 次心跳，实际 %d 次", got)
	}
}

// 重试途中若错误从网络类变成业务类，应立即放弃剩余重试。
func TestRetryAbortsWhenErrorBecomesBusiness(t *testing.T) {
	var calls atomic.Int32
	previous := monitor.SetHeartbeatRetryBackoffForTest(time.Millisecond)
	t.Cleanup(func() { monitor.SetHeartbeatRetryBackoffForTest(previous) })

	mon, err := monitor.New(monitor.Config{
		ServerAddr: "node.example", ServerPort: 7000, LocalAddr: "127.0.0.1:1",
		LeaseID: "lease-1", HeartbeatInterval: time.Hour, ExpireAtUnix: 1893456000,
		Heartbeater: heartbeatFunc(func(context.Context, string) (monitor.HeartbeatResult, error) {
			if calls.Add(1) == 1 {
				return monitor.HeartbeatResult{}, networkErr()
			}
			return monitor.HeartbeatResult{}, &auth.StatusError{Status: 401, Reason: auth.LeaseReasonTTL, Message: "登录状态已超时"}
		}),
		Hooks: monitor.Hooks{
			RetryableHeartbeat: isRetryableHeartbeat,
			RecoverLease: func(context.Context, error) (monitor.LeaseRecovery, bool) {
				return monitor.LeaseRecovery{LeaseID: "lease-2"}, true
			},
			StopTunnel: func(error) { t.Error("重登成功时不应停止隧道") },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mon.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mon.Stop() }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && calls.Load() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if got := calls.Load(); got > 2 {
		t.Fatalf("错误变成业务类后不该继续重试，实际共 %d 次心跳", got)
	}
}

// 请求被主动取消（用户停隧道）不算网络故障，不该重试。
func TestCanceledRequestIsNotRetryable(t *testing.T) {
	if isRetryableHeartbeat(errors.New("请求已取消")) {
		t.Fatal("主动取消不该被当成可重试的网络错误")
	}
	if !isRetryableHeartbeat(networkErr()) {
		t.Fatal("TransportError 应被判定为可重试")
	}
	if isRetryableHeartbeat(&auth.StatusError{Status: 401, Reason: auth.LeaseReasonTTL}) {
		t.Fatal("业务错误不该被判定为可重试")
	}
}
