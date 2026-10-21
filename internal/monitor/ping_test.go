package monitor

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
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

// 复现线上误报：本机有 TUN/代理时握手可能本地完成，毫秒取整为 0。
// 只要有过成功样本就绝不能报"节点测速失败"。
func TestPingDoesNotReportFailureWhenSamplesAreZeroMS(t *testing.T) {
	latencies := make([]int64, pingSamples)
	latencies[0] = 31 // 其余全为 0
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	dialed := 0

	var pings []int64
	failed := false
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
				// 只统计中间样本：终态 Done 事件带的是同一个平均值，
				// 算进去会让"每次成功更新一次"的断言多出一次。
				if event.Status == StatusConnected && !event.Done {
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
	if dialed != pingSamples {
		t.Fatalf("应测 %d 次，实际 %d", pingSamples, dialed)
	}
	if len(pings) != pingSamples {
		t.Fatalf("每次成功都应更新显示，实际 %v", pings)
	}
	// 去极值：去掉 31 与一个 0，剩 8 个 0 → 平均 0ms（本地代理环境的真实结果）
	if pings[len(pings)-1] != 0 {
		t.Fatalf("去极值平均应为 0ms，实际 %v", pings)
	}
}

// 首次失败后成功仍要显示。
func TestPingDisplaysAfterFirstFailureThenSuccess(t *testing.T) {
	latencies := []int64{0, 25, 20, 30, 22, 24, 21, 23, 26, 27}
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
				if event.Kind == "tunnel:status" && event.Status == StatusConnected && !event.Done {
					pings = append(pings, event.PingMS)
				}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mon.measurePing(context.Background())

	// 首个样本失败被跳过，其余 9 个成功样本各更新一次
	if len(pings) != pingSamples-1 {
		t.Fatalf("应有 %d 次成功更新，实际 %v", pingSamples-1, pings)
	}
}

// ping 用独立超时（2s），端口探测仍是 500ms，两者语义不能混。
func TestPingUsesSeparateLongerDialTimeout(t *testing.T) {
	if pingDialTimeout != 2*time.Second {
		t.Fatalf("ping 超时应为 2s，实际 %v", pingDialTimeout)
	}
	if probeDialTimeout != 500*time.Millisecond {
		t.Fatalf("端口探测超时应保持 500ms，实际 %v", probeDialTimeout)
	}
}

// RemeasurePingAsync 必须能独立触发一轮测速（供界面点击重测）。
func TestRemeasurePingAsyncTriggersMeasurement(t *testing.T) {
	latencies := make([]int64, pingSamples)
	for i := range latencies {
		latencies[i] = 30
	}
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	var dialed atomic.Int32
	var mu sync.Mutex
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
			index := int(dialed.Add(1)) - 1
			clock.advance(time.Duration(latencies[index]) * time.Millisecond)
			conn, peer := net.Pipe()
			_ = peer.Close()
			return conn, nil
		},
		Hooks: Hooks{
			Emit: func(event Event) {
				if event.Kind == "tunnel:status" && event.Status == StatusConnected && !event.Done {
					mu.Lock()
					pings = append(pings, event.PingMS)
					mu.Unlock()
				}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mon.RemeasurePingAsync(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(pings) == pingSamples
	})
	mu.Lock()
	defer mu.Unlock()

	if dialed.Load() != pingSamples {
		t.Fatalf("重测应重新采样 %d 次，实际 %d", pingSamples, dialed.Load())
	}
	if len(pings) != pingSamples {
		t.Fatalf("重测应产生 %d 次更新，实际 %v", pingSamples, pings)
	}
	if pings[len(pings)-1] != 30 {
		t.Fatalf("样本全为 30ms，平均应为 30，实际 %v", pings)
	}
}
