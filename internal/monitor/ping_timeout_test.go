package monitor

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

func blockingMonitor(t *testing.T, round time.Duration) (*Monitor, *[]Event, *atomic.Int32) {
	t.Helper()
	var events []Event
	var dials atomic.Int32
	mon, err := New(Config{
		ServerAddr:        "node.example",
		ServerPort:        7000,
		HeartbeatInterval: time.Hour,
		LeaseExpireAtUnix: time.Now().Add(time.Hour).Unix(),
		ExpireAtUnix:      time.Now().Add(24 * time.Hour).Unix(),
		PingTimeout:       round,
		Now:               time.Now,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dials.Add(1)
			// 模拟"卡住不返回"的连接：只有 ctx 结束才放弃。
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Hooks: Hooks{Emit: func(event Event) { events = append(events, event) }},
	})
	if err != nil {
		t.Fatal(err)
	}
	return mon, &events, &dials
}

// 整轮超时必须强制结束，并上报超时事件让界面恢复可点击。
func TestPingRoundTimeoutEndsAndReports(t *testing.T) {
	mon, events, dials := blockingMonitor(t, 80*time.Millisecond)

	done := make(chan struct{})
	go func() {
		_ = mon.measurePing(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("测速卡死了：整轮超时没有生效")
	}

	var timedOut *Event
	for i := range *events {
		if (*events)[i].Timeout {
			timedOut = &(*events)[i]
		}
	}
	if timedOut == nil {
		t.Fatalf("必须上报超时事件，实际事件 %+v", *events)
	}
	if timedOut.Status != StatusError {
		t.Fatalf("超时事件应为 error 状态，实际 %s", timedOut.Status)
	}
	if timedOut.Message == "" {
		t.Fatal("超时事件必须带可展示的消息")
	}
	// 卡住的 dial 只应该发起一次：超时后不再继续发新的连接
	if dials.Load() != 1 {
		t.Fatalf("超时后不应继续发起连接，实际发起 %d 次", dials.Load())
	}
}

// 超时结束后必须能再次测速（标志位被释放）。
func TestPingRoundTimeoutReleasesGuard(t *testing.T) {
	mon, _, _ := blockingMonitor(t, 50*time.Millisecond)

	_ = mon.measurePing(context.Background())
	if err := mon.acquirePing(); err != nil {
		t.Fatalf("超时后标志位应已释放，实际 %v", err)
	}
	mon.pinging.Store(false)
}

// 并发重测必须被拒绝，不能堆积协程。
func TestConcurrentRemeasureIsRejected(t *testing.T) {
	mon, _, _ := blockingMonitor(t, 400*time.Millisecond)

	if err := mon.RemeasurePingAsync(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 第一轮还在跑（dial 被阻塞），第二次必须立即被拒
	err := mon.RemeasurePingAsync(context.Background())
	if !errors.Is(err, ErrPingInProgress) {
		t.Fatalf("应返回 ErrPingInProgress，实际 %v", err)
	}
	if err.Error() != "测速进行中" {
		t.Fatalf("错误文案应为『测速进行中』，实际 %q", err.Error())
	}
}

// 隧道停止（ctx 取消）时测速必须快速退出。
func TestPingExitsOnContextCancel(t *testing.T) {
	mon, _, _ := blockingMonitor(t, 30*time.Second) // 整轮超时故意设得很长

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = mon.measurePing(ctx)
		close(done)
	}()

	time.Sleep(30 * time.Millisecond) // 让 dial 进入阻塞
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ctx 取消后测速没有退出")
	}
	// 取消不是超时，不应该报"测速超时"
	if err := mon.acquirePing(); err != nil {
		t.Fatalf("取消后标志位应已释放，实际 %v", err)
	}
	mon.pinging.Store(false)
}
