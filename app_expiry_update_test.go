package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
	"meccyradar/internal/monitor"
)

// 管理员在后台改卡密到期时间，客户端必须能感知。
//
// 线上问题：客户端只在登录时拿一次 expireAtUnix，之后再也不更新。
// 把期限延长，用户到点照样被踢；把期限改成已过期，用户还能一直用到重启。
//
// 方案：心跳响应带 expires_at（带 +08:00 的 ISO 8601），客户端每次心跳后
// 刷新本地值，每秒一次的到期检查自然就会在下一个 tick 停掉隧道。

func TestParseExpiresAt(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  int64
	}{
		{"北京时间", "2026-12-31T23:59:59+08:00", mustUnix(t, "2026-12-31T15:59:59Z")},
		{"UTC", "2026-12-31T15:59:59+00:00", mustUnix(t, "2026-12-31T15:59:59Z")},
		{"空串", "", 0},
		{"格式错误", "2026/12/31 23:59:59", 0},
		{"垃圾值", "not-a-time", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := auth.ParseExpiresAt(tc.value); got != tc.want {
				t.Fatalf("ParseExpiresAt(%q) = %d，期望 %d", tc.value, got, tc.want)
			}
		})
	}
}

func mustUnix(t *testing.T, value string) int64 {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Unix()
}

// 心跳带回 expires_at → monitor 刷新本地到期时间并回调 UpdateExpiry。
func TestMonitorAppliesHeartbeatExpiry(t *testing.T) {
	future := time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second)
	var got atomic.Int64

	mon := newExpiryMonitor(t, expiryConfig{
		ExpireAtUnix: time.Now().Add(time.Hour).Unix(), // 本地初始值：1 小时后
		Beat: func(int32) (monitor.HeartbeatResult, error) {
			return monitor.HeartbeatResult{ExpiresAt: future.Format(time.RFC3339)}, nil
		},
		UpdateExpiry: func(unix int64) { got.Store(unix) },
	})
	mon.start(t)

	mon.waitFor(t, func() bool { return got.Load() != 0 }, "UpdateExpiry 应被调用")
	if got.Load() != future.Unix() {
		t.Fatalf("UpdateExpiry 收到 %d，期望 %d", got.Load(), future.Unix())
	}
	if remaining := mon.monitor.RemainingSeconds(); remaining < int64(89*24*3600) {
		t.Fatalf("本地到期时间没有跟上心跳，剩余 %d 秒", remaining)
	}
}

// 旧服务端不返回 expires_at → 不更新、不报错。
func TestMonitorIgnoresMissingExpiresAt(t *testing.T) {
	initial := time.Now().Add(time.Hour).Unix()
	var called atomic.Int32

	mon := newExpiryMonitor(t, expiryConfig{
		ExpireAtUnix: initial,
		Beat: func(int32) (monitor.HeartbeatResult, error) {
			return monitor.HeartbeatResult{}, nil // 老服务端：data 是空对象
		},
		UpdateExpiry: func(int64) { called.Add(1) },
	})
	mon.start(t)

	mon.waitFor(t, func() bool { return mon.heartbeats.Load() >= 1 }, "至少要发一次心跳")
	time.Sleep(150 * time.Millisecond)

	if called.Load() != 0 {
		t.Fatal("服务端没下发 expires_at 时不该调用 UpdateExpiry")
	}
	if got := mon.monitor.RemainingSeconds(); got > int64(2*time.Hour/time.Second) || got < int64(30*time.Minute/time.Second) {
		t.Fatalf("本地到期时间不该被改动，实际剩余 %d 秒", got)
	}
}

// expires_at 格式错误 → 不更新，心跳本身照常成功。
func TestMonitorIgnoresMalformedExpiresAt(t *testing.T) {
	var called atomic.Int32
	var stopped atomic.Bool

	mon := newExpiryMonitor(t, expiryConfig{
		ExpireAtUnix: time.Now().Add(time.Hour).Unix(),
		Beat: func(int32) (monitor.HeartbeatResult, error) {
			return monitor.HeartbeatResult{ExpiresAt: "2026/12/31 23:59:59"}, nil
		},
		UpdateExpiry: func(int64) { called.Add(1) },
		StopTunnel:   func(error) { stopped.Store(true) },
	})
	mon.start(t)

	mon.waitFor(t, func() bool { return mon.heartbeats.Load() >= 3 }, "格式错误不该中断心跳")
	if called.Load() != 0 {
		t.Fatal("解析失败时不该更新到期时间")
	}
	if stopped.Load() {
		t.Fatal("解析失败不能把隧道停掉——那会把正常用户误伤下线")
	}
}

// 端到端：服务端先下发未来时间，3 次心跳后改下发已过期时间，
// 客户端应在下一次到期检查（expiryTicker 每秒一次，心跳前也会查）就停止隧道。
func TestMonitorStopsWhenServerExpiryTurnsPast(t *testing.T) {
	future := time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second)
	past := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	var pastHeartbeats atomic.Int32
	var stopped atomic.Bool
	var stoppedWith atomic.Value // error

	mon := newExpiryMonitor(t, expiryConfig{
		ExpireAtUnix: future.Unix(),
		Beat: func(call int32) (monitor.HeartbeatResult, error) {
			// 前 3 次返回未来时间，之后模拟管理员把它改成过去
			if call <= 3 {
				return monitor.HeartbeatResult{ExpiresAt: future.Format(time.RFC3339)}, nil
			}
			pastHeartbeats.Add(1)
			return monitor.HeartbeatResult{ExpiresAt: past.Format(time.RFC3339)}, nil
		},
		StopTunnel: func(err error) {
			stopped.Store(true)
			if err != nil {
				stoppedWith.Store(err.Error())
			}
		},
	})
	mon.start(t)

	mon.waitFor(t, func() bool { return pastHeartbeats.Load() >= 1 }, "应收到改成过期后的心跳")
	// 到期检查是 1 秒一次，给足几个周期
	mon.waitFor(t, func() bool { return stopped.Load() }, "到期时间变成过去后应停止隧道")

	if msg, _ := stoppedWith.Load().(string); msg != "授权已过期" {
		t.Fatalf("停止原因应为授权已过期，实际 %q", msg)
	}
}

// 并发安全：心跳在写 expireAtUnix，倒计时在按秒读，不能 panic 也不能被 -race 抓到。
func TestExpiresInIsConcurrencySafe(t *testing.T) {
	mon, err := monitor.New(monitor.Config{
		ServerAddr: "node.example", ServerPort: 7000, LocalAddr: "127.0.0.1:1",
		LeaseID: "lease-1", HeartbeatInterval: 10 * time.Millisecond,
		ExpireAtUnix: time.Now().Add(time.Hour).Unix(),
		Heartbeater: heartbeatFunc(func(context.Context, string) (monitor.HeartbeatResult, error) {
			return monitor.HeartbeatResult{
				ExpiresAt: time.Now().Add(48 * time.Hour).Format(time.RFC3339),
			}, nil
		}),
		Hooks: monitor.Hooks{UpdateExpiry: func(int64) {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mon.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mon.Stop() }()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = mon.RemainingSeconds()
				}
			}
		}()
	}
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// app 层：心跳带回的到期时间要写进 authData，界面才有新值可显示。
func TestApplyServerExpiryUpdatesAuthData(t *testing.T) {
	app := newApp(appDependencies{store: config.NewMemoryStore()})
	app.authData = &auth.AuthData{ExpireAtUnix: time.Now().Add(time.Hour).Unix(), ExpireAtStr: "旧值"}

	target := time.Date(2026, 12, 31, 23, 59, 0, 0, time.FixedZone("CST", 8*3600))
	app.applyServerExpiry(target.Unix())

	if got := app.authData.ExpireAtUnix; got != target.Unix() {
		t.Fatalf("ExpireAtUnix 未更新：%d，期望 %d", got, target.Unix())
	}
	// 展示串必须是北京时间，不受本机时区影响
	if got := app.authData.ExpireAtStr; got != "2026-12-31 23:59" {
		t.Fatalf("ExpireAtStr 应为北京时间 2026-12-31 23:59，实际 %q", got)
	}
}

// 非法值不能把到期时间改成 0（那等于立刻判定过期、把用户踢下线）。
func TestApplyServerExpiryIgnoresNonPositive(t *testing.T) {
	app := newApp(appDependencies{store: config.NewMemoryStore()})
	original := time.Now().Add(time.Hour).Unix()
	app.authData = &auth.AuthData{ExpireAtUnix: original, ExpireAtStr: "原值"}

	app.applyServerExpiry(0)
	app.applyServerExpiry(-1)

	if got := app.authData.ExpireAtUnix; got != original {
		t.Fatalf("非法值不该改动到期时间，实际 %d", got)
	}
	if app.authData.ExpireAtStr != "原值" {
		t.Fatalf("非法值不该改动展示串，实际 %q", app.authData.ExpireAtStr)
	}
}

// ---- 测试脚手架 ----

type expiryHarness struct {
	monitor    *monitor.Monitor
	heartbeats atomic.Int32
}

func (h *expiryHarness) start(t *testing.T) {
	t.Helper()
	if err := h.monitor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.monitor.Stop() })
}

func (h *expiryHarness) waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s（已心跳 %d 次）", what, h.heartbeats.Load())
}

// expiryConfig 是 newExpiryMonitor 的入参，避免把整个 monitor.Config 抄一遍。
type expiryConfig struct {
	ExpireAtUnix int64
	Beat         func(call int32) (monitor.HeartbeatResult, error)
	UpdateExpiry func(int64)
	StopTunnel   func(error)
}

func newExpiryMonitor(t *testing.T, cfg expiryConfig) *expiryHarness {
	t.Helper()
	h := &expiryHarness{}
	mon, err := monitor.New(monitor.Config{
		ServerAddr: "node.example", ServerPort: 7000, LocalAddr: "127.0.0.1:1",
		LeaseID: "lease-1",
		// 心跳调快，让用例几秒内能跑完；到期检查仍是真实的 1 秒 ticker
		HeartbeatInterval: 20 * time.Millisecond,
		ExpireAtUnix:      cfg.ExpireAtUnix,
		Heartbeater: heartbeatFunc(func(_ context.Context, _ string) (monitor.HeartbeatResult, error) {
			call := h.heartbeats.Add(1)
			return cfg.Beat(call)
		}),
		Hooks: monitor.Hooks{
			UpdateExpiry: cfg.UpdateExpiry,
			StopTunnel:   cfg.StopTunnel,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.monitor = mon
	return h
}
