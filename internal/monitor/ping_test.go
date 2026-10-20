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
