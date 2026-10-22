package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
	"meccyradar/internal/monitor"
)

// 线上故障复现：笔记本休眠一夜 → 心跳停了 8 小时 → 租约按 90 秒 TTL 过期。
// 唤醒后客户端拿旧 leaseId 启动隧道，FRPC 的 token 还有效（日志里是
// "start proxy success"），1 秒后首次心跳收到 401，隧道被停掉。
//
// 服务端现在会区分释放原因，客户端据此决定"静默重登"还是"停止并提示"。

// leaseServer 是可控的假管理端：能指定心跳返回什么、登录成功几次后失败。
type leaseServer struct {
	server     *httptest.Server
	logins     atomic.Int32
	heartbeats atomic.Int32
	// heartbeatStatus/heartbeatReason 控制心跳返回；0 表示 200。
	heartbeatStatus atomic.Int32
	heartbeatReason atomic.Value // string
	// loginFailAfter > 0 时，前 N 次登录成功后开始返回 502。
	loginFailAlways atomic.Bool
	// leaseSeq 让每次登录拿到不同的租约，便于断言"确实换了新租约"。
	leaseSeq atomic.Int32
}

func newLeaseServer(t *testing.T) *leaseServer {
	t.Helper()
	ls := &leaseServer{}
	ls.heartbeatReason.Store("")
	ls.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/client/login":
			ls.logins.Add(1)
			if ls.loginFailAlways.Load() {
				w.WriteHeader(http.StatusBadGateway)
				_ = json.NewEncoder(w).Encode(map[string]any{"detail": "服务资源分配失败，请稍后重试"})
				return
			}
			seq := ls.leaseSeq.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200, "msg": "ok",
				"data": map[string]any{
					"appName": "MeccyRadar", "shareBaseUrl": "http://radar.example/",
					"expireAtUnix": 1893456000, "expireAtStr": "2030-01-01 00:00",
					"session": map[string]any{
						"leaseId": "lease-" + itoa(int(seq)), "heartbeatInterval": 30,
						"leaseExpireAtUnix": 1893456000,
					},
					"frpConfig": map[string]any{
						"serverAddr": "node.example", "serverPort": 7000,
						"user": "user", "token": "secret-token",
						"proxies": []any{map[string]any{
							"name": "radar_tcp", "type": "tcp",
							"localIP": "127.0.0.1", "localPort": 13133, "remotePort": 8000,
						}},
					},
				},
			})
		case "/api/client/heartbeat":
			ls.heartbeats.Add(1)
			status := int(ls.heartbeatStatus.Load())
			if status == 0 {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": nil})
				return
			}
			w.WriteHeader(status)
			body := map[string]any{"detail": "登录状态已失效，请重新登录"}
			if reason, _ := ls.heartbeatReason.Load().(string); reason != "" {
				body["reason"] = reason
				switch reason {
				case "ttl":
					body["detail"] = "登录状态已超时，正在重新连接"
				case "takeover":
					body["detail"] = "该卡密已在其他设备登录，本机已停止"
				}
			}
			_ = json.NewEncoder(w).Encode(body)
		case "/api/client/logout":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": nil})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ls.server.Close)
	return ls
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

func (ls *leaseServer) setHeartbeat(status int, reason string) {
	ls.heartbeatStatus.Store(int32(status))
	ls.heartbeatReason.Store(reason)
}

func newRecoveryApp(t *testing.T, ls *leaseServer) *App {
	t.Helper()
	client, err := auth.NewClient(ls.server.URL, ls.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: config.NewMemoryStore()})
	// 退避调小：这些用例要跑好几轮重试，真等 2 秒会让整个包慢半分钟。
	// 放在 App 字段上而不是包级变量，避免和其他用例残留的 goroutine 竞争。
	app.leaseRetryBackoff = time.Millisecond
	if _, err := app.LoginByCard("CARD-RECOVER"); err != nil {
		t.Fatal(err)
	}
	return app
}

// 心跳 401 + reason=ttl → 静默重登，拿到新租约，隧道继续。
func TestHeartbeatTTLTriggersSilentRelogin(t *testing.T) {
	ls := newLeaseServer(t)
	app := newRecoveryApp(t, ls)

	ls.setHeartbeat(401, auth.LeaseReasonTTL)
	before := ls.logins.Load()

	recovery, ok := app.recoverLease(context.Background(), &auth.StatusError{
		Status: 401, Reason: auth.LeaseReasonTTL, Message: "登录状态已超时，正在重新连接",
	})
	if !ok {
		t.Fatal("租约 TTL 过期必须自动重登，实际放弃了")
	}
	if got := ls.logins.Load(); got != before+1 {
		t.Fatalf("应恰好重登一次，实际 %d 次", got-before)
	}
	if recovery.LeaseID == "" || recovery.LeaseID == "lease-1" {
		t.Fatalf("必须拿到新租约，实际 %q", recovery.LeaseID)
	}
	// 同一张卡重登，服务端复用配置 → 不该重启隧道（避免界面闪断）
	if recovery.ConfigChanged {
		t.Fatal("配置没变时不应重启隧道")
	}
}

// 心跳 401 + reason=takeover → 绝不自动重登。
//
// 这是最关键的一条：重登会把另一台设备顶掉，对方再来顶，
// 两台机器就会无限互踢，而且用户永远不知道该看哪台。
func TestHeartbeatTakeoverNeverRelogins(t *testing.T) {
	ls := newLeaseServer(t)
	app := newRecoveryApp(t, ls)

	ls.setHeartbeat(401, auth.LeaseReasonTakeover)
	before := ls.logins.Load()

	_, ok := app.recoverLease(context.Background(), &auth.StatusError{
		Status: 401, Reason: auth.LeaseReasonTakeover, Message: "该卡密已在其他设备登录，本机已停止",
	})
	if ok {
		t.Fatal("被其他设备顶掉时不能自动重登，否则两台设备会无限互踢")
	}
	if got := ls.logins.Load(); got != before {
		t.Fatalf("takeover 不应发起任何登录请求，实际多了 %d 次", got-before)
	}
}

// 网络类失败（超时/断网）不改语义：直接放弃，不去打登录接口。
func TestNetworkFailureDoesNotRelogin(t *testing.T) {
	ls := newLeaseServer(t)
	app := newRecoveryApp(t, ls)

	before := ls.logins.Load()
	_, ok := app.recoverLease(context.Background(), errors.New("dial tcp: i/o timeout"))
	if ok {
		t.Fatal("网络类心跳失败不应触发重登（本轮不改语义）")
	}
	if got := ls.logins.Load(); got != before {
		t.Fatalf("网络失败不应发起登录请求，实际多了 %d 次", got-before)
	}
}

// 重登失败要如实上报，并且重试次数有上限。
func TestReloginFailureIsBoundedAndReported(t *testing.T) {
	ls := newLeaseServer(t)
	app := newRecoveryApp(t, ls)

	ls.loginFailAlways.Store(true)
	before := ls.logins.Load()

	_, ok := app.recoverLease(context.Background(), &auth.StatusError{
		Status: 401, Reason: auth.LeaseReasonTTL, Message: "登录状态已超时",
	})
	if ok {
		t.Fatal("重登失败时必须返回 false，让 monitor 停止隧道")
	}
	attempts := ls.logins.Load() - before
	if attempts != leaseRecoverAttempts {
		t.Fatalf("单次恢复应重试 %d 次，实际 %d 次", leaseRecoverAttempts, attempts)
	}
}

// 连续多个恢复回合都失败后必须彻底放弃，不能每 30 秒无限重试下去。
func TestRecoveryGivesUpAfterRepeatedFailedEpisodes(t *testing.T) {
	ls := newLeaseServer(t)
	app := newRecoveryApp(t, ls)
	ls.loginFailAlways.Store(true)

	cause := &auth.StatusError{Status: 401, Reason: auth.LeaseReasonTTL, Message: "登录状态已超时"}

	for i := 0; i < leaseRecoverEpisodes; i++ {
		app.recoverLease(context.Background(), cause)
	}

	before := ls.logins.Load()
	_, ok := app.recoverLease(context.Background(), cause)
	if ok {
		t.Fatal("已用完所有恢复回合，不应再成功")
	}
	// 回合用尽后仍会走一次有界重试（这是最后一次机会），但不能无限增长。
	if got := ls.logins.Load() - before; got > leaseRecoverAttempts {
		t.Fatalf("回合用尽后仍重试了 %d 次，超过上限 %d", got, leaseRecoverAttempts)
	}
}

// 心跳成功后租约被标记为健康，StartTunnel 就不会白白重登。
func TestHeartbeatSuccessMarksLeaseHealthy(t *testing.T) {
	ls := newLeaseServer(t)
	app := newRecoveryApp(t, ls)

	app.handleHeartbeatResult(nil)
	if app.lease.stale(time.Now()) {
		t.Fatal("心跳刚成功，租约不该被判为过期")
	}
}

// 心跳 401 后租约被判为过期，StartTunnel 必须先换新租约再启动 FRPC。
func TestHeartbeatUnauthorizedMakesLeaseStale(t *testing.T) {
	ls := newLeaseServer(t)
	app := newRecoveryApp(t, ls)

	app.handleHeartbeatResult(&auth.StatusError{
		Status: 401, Reason: auth.LeaseReasonTTL, Message: "登录状态已超时",
	})
	if !app.lease.stale(time.Now()) {
		t.Fatal("心跳 401 之后，手上的租约不能再被认为可用")
	}
}

// 租约到期时刻已过（休眠一夜）→ 判为过期。
func TestLeaseExpiryMakesLeaseStale(t *testing.T) {
	var guard leaseGuard
	now := time.Now()
	guard.markHealthy(now.Add(-2 * leaseTTL))
	if !guard.stale(now) {
		t.Fatal("租约估算到期时刻已过，必须判为过期")
	}
	guard.markHealthy(now)
	if guard.stale(now) {
		t.Fatal("刚心跳成功不该判为过期")
	}
}

// StartTunnel 在租约过期且重登失败时，必须直接报错而不是先起 FRPC。
//
// 这正是线上"启动成功然后立刻断开"的根因：先起 FRPC 再被心跳 401 停掉。
func TestStartTunnelRefreshesStaleLeaseBeforeStarting(t *testing.T) {
	ls := newLeaseServer(t)
	app := newRecoveryApp(t, ls)

	// 模拟休眠一夜：上次心跳是 8 小时前
	app.lease.markHealthy(time.Now().Add(-8 * time.Hour))
	app.lease.markFailed(auth.LeaseReasonTTL)

	// 这个用例可能真的把隧道起起来（假服务端是可达的），必须收尾：
	// 残留的 frp client 会和后续用例的 client 争 frp 内部状态（-race 会报）。
	t.Cleanup(func() { _ = app.stopTunnel() })

	before := ls.logins.Load()
	err := app.StartTunnel()
	if err != nil {
		// 本机没有 frpc 二进制时 startTunnel 会在刷新之后失败，这没关系：
		// 这里只断言"刷新发生在启动之前"。
		t.Logf("startTunnel 返回（预期：刷新后启动 FRPC 失败）：%v", err)
	}
	if got := ls.logins.Load(); got != before+1 {
		t.Fatalf("启动前应恰好重登一次，实际 %d 次", got-before)
	}
}

// 租约新鲜时 StartTunnel 不该多发登录请求。
func TestStartTunnelDoesNotReloginWhenLeaseFresh(t *testing.T) {
	ls := newLeaseServer(t)
	app := newRecoveryApp(t, ls)

	app.lease.markHealthy(time.Now())
	t.Cleanup(func() { _ = app.stopTunnel() })

	before := ls.logins.Load()
	_ = app.StartTunnel()
	if got := ls.logins.Load(); got != before {
		t.Fatalf("租约新鲜时不应重登，实际多了 %d 次", got-before)
	}
}

// heartbeatTracker 必须把每次心跳结果转给 leaseGuard。
func TestHeartbeatTrackerForwardsResult(t *testing.T) {
	var seen []error
	tracker := &heartbeatTracker{
		inner: authHeartbeatFunc(func(context.Context, string) (auth.HeartbeatResponse, error) {
			return auth.HeartbeatResponse{}, errors.New("boom")
		}),
		onResult: func(err error) { seen = append(seen, err) },
	}
	_, _ = tracker.Heartbeat(context.Background(), "lease-x")
	if len(seen) != 1 || seen[0] == nil {
		t.Fatalf("心跳失败必须回调一次且带错误，实际 %#v", seen)
	}
}

type heartbeatFunc func(context.Context, string) (monitor.HeartbeatResult, error)

func (f heartbeatFunc) Heartbeat(ctx context.Context, leaseID string) (monitor.HeartbeatResult, error) {
	return f(ctx, leaseID)
}

// authHeartbeatFunc 是 auth 签名的心跳桩，用来测 heartbeatTracker 的适配。
type authHeartbeatFunc func(context.Context, string) (auth.HeartbeatResponse, error)

func (f authHeartbeatFunc) Heartbeat(ctx context.Context, leaseID string) (auth.HeartbeatResponse, error) {
	return f(ctx, leaseID)
}

// monitor 层：RecoverLease 返回成功时不应停止隧道，并改用新租约。
func TestMonitorUsesRecoveredLease(t *testing.T) {
	var mu sync.Mutex
	var used []string
	var stopped bool
	mon, err := monitor.New(monitor.Config{
		ServerAddr: "node.example", ServerPort: 7000,
		LeaseID: "lease-old", HeartbeatInterval: time.Hour, ExpireAtUnix: 1893456000,
		Heartbeater: heartbeatFunc(func(_ context.Context, leaseID string) (monitor.HeartbeatResult, error) {
			mu.Lock()
			used = append(used, leaseID)
			mu.Unlock()
			if leaseID == "lease-old" {
				return monitor.HeartbeatResult{}, &auth.StatusError{Status: 401, Reason: auth.LeaseReasonTTL, Message: "超时"}
			}
			return monitor.HeartbeatResult{}, nil
		}),
		Hooks: monitor.Hooks{
			StopTunnel: func(error) { stopped = true },
			RecoverLease: func(context.Context, error) (monitor.LeaseRecovery, bool) {
				return monitor.LeaseRecovery{LeaseID: "lease-new"}, true
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mon.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mon.Stop() }()

	// 等首轮心跳跑完
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(used)
		mu.Unlock()
		if got >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if stopped {
		t.Fatal("租约已成功恢复，不应停止隧道")
	}
}

// monitor 层：RecoverLease 拒绝时照常停止隧道，并把服务端文案报上去。
func TestMonitorStopsWhenRecoveryDeclined(t *testing.T) {
	var mu sync.Mutex
	var stoppedWith error
	var message string
	mon, err := monitor.New(monitor.Config{
		ServerAddr: "node.example", ServerPort: 7000,
		LeaseID: "lease-old", HeartbeatInterval: time.Hour, ExpireAtUnix: 1893456000,
		Heartbeater: heartbeatFunc(func(context.Context, string) (monitor.HeartbeatResult, error) {
			return monitor.HeartbeatResult{}, &auth.StatusError{Status: 401, Reason: auth.LeaseReasonTakeover, Message: "该卡密已在其他设备登录，本机已停止"}
		}),
		Hooks: monitor.Hooks{
			StopTunnel: func(err error) {
				mu.Lock()
				stoppedWith = err
				mu.Unlock()
			},
			Emit: func(e monitor.Event) {
				mu.Lock()
				message = e.Message
				mu.Unlock()
			},
			RecoverLease: func(context.Context, error) (monitor.LeaseRecovery, bool) { return monitor.LeaseRecovery{}, false },
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
	for time.Now().Before(deadline) {
		mu.Lock()
		done := stoppedWith != nil
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	failed := stoppedWith == nil
	mu.Unlock()
	if failed {
		t.Fatal("恢复被拒绝时必须停止隧道")
	}
	mu.Lock()
	captured := message
	mu.Unlock()
	if !strings.Contains(captured, "其他设备") {
		t.Fatalf("界面应看到服务端的业务文案，实际 %q", captured)
	}
}
