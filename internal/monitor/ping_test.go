package monitor

import (
	"context"
	"net"
	"testing"
	"time"
)

// fakeClock 时间由拨号过程显式推进：成功建立连接时前进对应延迟。
// 这样即使某些样本失败（不会调用第二次 Now），也不会与脚本错位。
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// TestPingReportsRunningMinimumAcrossFiveSamples 需求：
// 共测 5 次，界面始终显示历史最低值。
// 实测 41 36 98 78 52 → 显示 41 36 36 36 36。
func TestPingReportsRunningMinimumAcrossFiveSamples(t *testing.T) {
	latencies := []int64{41, 36, 98, 78, 52}
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	dialed := 0

	var pings []int64
	mon, err := New(Config{
		ServerAddr:        "node.example",
		ServerPort:        7000,
		LocalAddr:         "127.0.0.1:13133",
		HeartbeatInterval: time.Hour,
		LeaseExpireAtUnix: time.Now().Add(time.Hour).Unix(),
		ExpireAtUnix:      time.Now().Add(24 * time.Hour).Unix(),
		Now:               clock.Now,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			clock.advance(time.Duration(latencies[dialed]) * time.Millisecond)
			dialed++
			conn, peer := net.Pipe()
			_ = peer.Close()
			return conn, nil
		},
		Hooks: Hooks{
			Emit: func(event Event) {
				if event.Kind == "tunnel:status" && event.PingMS > 0 {
					pings = append(pings, event.PingMS)
				}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mon.measurePing(context.Background())

	if dialed != len(latencies) {
		t.Fatalf("应测 %d 次，实际 %d 次", len(latencies), dialed)
	}
	want := []int64{41, 36, 36, 36, 36}
	if len(pings) != len(want) {
		t.Fatalf("应上报 %d 次延迟（每次测量一次），实际 %v", len(want), pings)
	}
	for i := range want {
		if pings[i] != want[i] {
			t.Fatalf("第 %d 次应显示 %d，实际 %d（完整序列 %v）", i+1, want[i], pings[i], pings)
		}
	}
}

// TestPingSkipsFailedSamplesButKeepsGoing 单次失败不应中断，其余样本照常上报。
func TestPingSkipsFailedSamplesButKeepsGoing(t *testing.T) {
	latencies := []int64{30, 20, 90, 50, 40}
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}

	calls := 0
	var pings []int64
	mon, err := New(Config{
		ServerAddr:        "node.example",
		ServerPort:        7000,
		LocalAddr:         "127.0.0.1:13133",
		HeartbeatInterval: time.Hour,
		LeaseExpireAtUnix: time.Now().Add(time.Hour).Unix(),
		ExpireAtUnix:      time.Now().Add(24 * time.Hour).Unix(),
		Now:               clock.Now,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			clock.advance(time.Duration(latencies[calls]) * time.Millisecond)
			calls++
			if calls == 2 {
				return nil, context.DeadlineExceeded
			}
			conn, peer := net.Pipe()
			_ = peer.Close()
			return conn, nil
		},
		Hooks: Hooks{
			Emit: func(event Event) {
				if event.Kind == "tunnel:status" && event.PingMS > 0 {
					pings = append(pings, event.PingMS)
				}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mon.measurePing(context.Background())

	// 第 2 次失败被跳过，其余 4 次都上报，且始终是历史最低
	want := []int64{30, 30, 30, 30}
	if len(pings) != len(want) {
		t.Fatalf("期望 %v，实际 %v", want, pings)
	}
	for i := range want {
		if pings[i] != want[i] {
			t.Fatalf("期望 %v，实际 %v", want, pings)
		}
	}
}

// TestPingReportsFailureWhenAllSamplesFail 全部失败时仍要报错。
func TestPingReportsFailureWhenAllSamplesFail(t *testing.T) {
	mon, err := New(Config{
		ServerAddr:        "node.example",
		ServerPort:        7000,
		LocalAddr:         "127.0.0.1:13133",
		HeartbeatInterval: time.Hour,
		LeaseExpireAtUnix: time.Now().Add(time.Hour).Unix(),
		ExpireAtUnix:      time.Now().Add(24 * time.Hour).Unix(),
		Now:               time.Now,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, context.DeadlineExceeded
		},
		Hooks: Hooks{
			Emit: func(event Event) {
				if event.Kind == "tunnel:status" && event.Status == StatusError {
					if event.Message != "节点测速失败" {
						t.Fatalf("意外的错误文案：%q", event.Message)
					}
				}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mon.measurePing(context.Background())
}

// TestPingDoesNotReportFailureWhenSamplesAreZeroMS 复现线上误报：
// 本机有 TUN/代理时握手可能本地完成，毫秒取整为 0（实测 31,0,0,0,0）。
// 有成功样本就绝不能报"节点测速失败"。
func TestPingDoesNotReportFailureWhenSamplesAreZeroMS(t *testing.T) {
	latencies := []int64{31, 0, 0, 0, 0}
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	dialed := 0

	var pings []int64
	var failed bool
	mon, err := New(Config{
		ServerAddr:        "node.example",
		ServerPort:        7000,
		LocalAddr:         "127.0.0.1:13133",
		HeartbeatInterval: time.Hour,
		LeaseExpireAtUnix: time.Now().Add(time.Hour).Unix(),
		ExpireAtUnix:      time.Now().Add(24 * time.Hour).Unix(),
		Now:               clock.Now,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			clock.advance(time.Duration(latencies[dialed]) * time.Millisecond)
			dialed++
			conn, peer := net.Pipe()
			_ = peer.Close()
			return conn, nil
		},
		Hooks: Hooks{
			Emit: func(event Event) {
				if event.Kind != "tunnel:status" {
					return
				}
				if event.Status == StatusError && event.Message == "节点测速失败" {
					failed = true
				}
				if event.PingMS > 0 {
					pings = append(pings, event.PingMS)
				}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mon.measurePing(context.Background())

	if failed {
		t.Fatal("有成功样本时不得报『节点测速失败』")
	}
	if len(pings) != len(latencies) {
		t.Fatalf("每次成功都应上报，实际 %v", pings)
	}
	if pings[0] != 31 {
		t.Fatalf("首个样本应为 31ms，实际 %d", pings[0])
	}
	// 0ms 样本按"成功样本最小 1ms"处理，且始终取历史最低
	for i := 1; i < len(pings); i++ {
		if pings[i] != 1 {
			t.Fatalf("0ms 样本应显示为 1ms 且不回退，实际序列 %v", pings)
		}
	}
}

// TestPingReportsFailureOnlyWhenEverySampleFails 全部失败才报失败。
func TestPingReportsFailureOnlyWhenEverySampleFails(t *testing.T) {
	failures := 0
	mon, err := New(Config{
		ServerAddr:        "node.example",
		ServerPort:        7000,
		LocalAddr:         "127.0.0.1:13133",
		HeartbeatInterval: time.Hour,
		LeaseExpireAtUnix: time.Now().Add(time.Hour).Unix(),
		ExpireAtUnix:      time.Now().Add(24 * time.Hour).Unix(),
		Now:               time.Now,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, context.DeadlineExceeded
		},
		Hooks: Hooks{
			Emit: func(event Event) {
				if event.Kind == "tunnel:status" && event.Status == StatusError {
					failures++
				}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mon.measurePing(context.Background())
	if failures != 1 {
		t.Fatalf("全部失败时应恰好报一次失败，实际 %d", failures)
	}
}

// TestPingDisplaysAfterFirstFailureThenSuccess 首次失败后成功仍要显示。
func TestPingDisplaysAfterFirstFailureThenSuccess(t *testing.T) {
	latencies := []int64{0, 25, 20, 30, 22}
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	calls := 0

	var pings []int64
	mon, err := New(Config{
		ServerAddr:        "node.example",
		ServerPort:        7000,
		LocalAddr:         "127.0.0.1:13133",
		HeartbeatInterval: time.Hour,
		LeaseExpireAtUnix: time.Now().Add(time.Hour).Unix(),
		ExpireAtUnix:      time.Now().Add(24 * time.Hour).Unix(),
		Now:               clock.Now,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			idx := calls
			calls++
			if idx == 0 {
				return nil, context.DeadlineExceeded
			}
			clock.advance(time.Duration(latencies[idx]) * time.Millisecond)
			conn, peer := net.Pipe()
			_ = peer.Close()
			return conn, nil
		},
		Hooks: Hooks{
			Emit: func(event Event) {
				if event.Kind == "tunnel:status" && event.PingMS > 0 {
					pings = append(pings, event.PingMS)
				}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mon.measurePing(context.Background())

	want := []int64{25, 20, 20, 20}
	if len(pings) != len(want) {
		t.Fatalf("期望 %v，实际 %v", want, pings)
	}
	for i := range want {
		if pings[i] != want[i] {
			t.Fatalf("期望 %v，实际 %v", want, pings)
		}
	}
}

// TestPingUsesSeparateLongerDialTimeout ping 用独立超时（2s），
// 端口探测仍是 500ms，两者语义不能混。
func TestPingUsesSeparateLongerDialTimeout(t *testing.T) {
	if pingDialTimeout != 2*time.Second {
		t.Fatalf("ping 超时应为 2s，实际 %v", pingDialTimeout)
	}
	if probeDialTimeout != 500*time.Millisecond {
		t.Fatalf("端口探测超时应保持 500ms，实际 %v", probeDialTimeout)
	}
}
