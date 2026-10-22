package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
)

// 客户端换服：停隧道 → 调服务端 → 用返回的 envelope 替换 authData → 自动重连。
//
// 服务端返回的结构与 /client/login 完全一致，所以客户端直接复用登录解析。

type changeServerMock struct {
	server     *httptest.Server
	calls      atomic.Int32
	failStatus atomic.Int32
	failDetail atomic.Value // string
	serverAddr atomic.Value // string，每次登录/换服返回不同的地址
	leaseSeq   atomic.Int32
}

func newChangeServerMock(t *testing.T) *changeServerMock {
	t.Helper()
	m := &changeServerMock{}
	m.failStatus.Store(0)
	m.failDetail.Store("")
	m.serverAddr.Store("node-a.example")
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/client/login", "/api/client/change-server":
			if r.URL.Path == "/api/client/change-server" {
				m.calls.Add(1)
			}
			if status := int(m.failStatus.Load()); status != 0 {
				w.WriteHeader(status)
				detail, _ := m.failDetail.Load().(string)
				_ = json.NewEncoder(w).Encode(map[string]any{"detail": detail})
				return
			}
			seq := m.leaseSeq.Add(1)
			addr, _ := m.serverAddr.Load().(string)
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
						"serverAddr": addr, "serverPort": 7000,
						"user": "user", "token": "secret-token",
						"proxies": []any{map[string]any{
							"name": "radar_tcp", "type": "tcp",
							"localIP": "127.0.0.1", "localPort": 13133, "remotePort": 8000,
						}},
					},
				},
			})
		case "/api/client/heartbeat", "/api/client/logout":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": nil})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(m.server.Close)
	return m
}

func newChangeServerApp(t *testing.T, m *changeServerMock) *App {
	t.Helper()
	client, err := auth.NewClient(m.server.URL, m.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: config.NewMemoryStore()})
	if _, err := app.LoginByCard("CARD-CHANGE"); err != nil {
		t.Fatal(err)
	}
	return app
}

// 成功换服：authData 必须整体换成服务端返回的那份（新租约、新地址），
// 并且 leaseGuard 要刷新 —— 否则下一次心跳还会拿旧租约。
func TestChangeServerAdoptsNewEnvelope(t *testing.T) {
	m := newChangeServerMock(t)
	app := newChangeServerApp(t, m)
	t.Cleanup(func() { _ = app.stopTunnel() })

	before := app.authData
	beforeLease := before.Session.LeaseID
	beforeAddr := before.FRPConfig.ServerAddr

	m.serverAddr.Store("node-b.example")

	result, err := app.ChangeServer()
	if err != nil {
		t.Fatalf("换服应成功，实际 %v", err)
	}

	app.mu.Lock()
	after := app.authData
	app.mu.Unlock()

	if after == before {
		t.Fatal("authData 必须被替换成新对象")
	}
	if after.Session.LeaseID == beforeLease {
		t.Fatalf("租约必须换新，实际仍是 %q", after.Session.LeaseID)
	}
	if after.FRPConfig.ServerAddr != "node-b.example" {
		t.Fatalf("地址应更新，实际 %q", after.FRPConfig.ServerAddr)
	}
	if beforeAddr == after.FRPConfig.ServerAddr {
		t.Fatal("前提：换服前后地址应该不同")
	}
	// 换服不是续期
	if after.ExpireAtUnix != before.ExpireAtUnix {
		t.Fatalf("到期时间不该变：%d -> %d", before.ExpireAtUnix, after.ExpireAtUnix)
	}
	// leaseGuard 已刷新：不该被判成过期
	if app.lease.stale(time.Now()) {
		t.Fatal("换服后 leaseGuard 必须刷新，不能还认为租约失效")
	}
	if result.ExpireAtUnix != before.ExpireAtUnix {
		t.Fatalf("返回的到期时间应与 envelope 一致")
	}
}

// 失败时保留原配置，并尽量恢复原来的连接状态。
func TestChangeServerFailureKeepsOldConfig(t *testing.T) {
	m := newChangeServerMock(t)
	app := newChangeServerApp(t, m)
	t.Cleanup(func() { _ = app.stopTunnel() })

	before := app.authData
	beforeLease := before.Session.LeaseID
	beforeAddr := before.FRPConfig.ServerAddr

	m.failStatus.Store(http.StatusServiceUnavailable)
	m.failDetail.Store("暂无其他可用服务器，请稍后重试")

	_, err := app.ChangeServer()
	if err == nil {
		t.Fatal("服务端拒绝时必须返回错误")
	}
	if msg := err.Error(); msg != "暂无其他可用服务器，请稍后重试" {
		t.Fatalf("应原样展示服务端中文文案，实际 %q", msg)
	}

	app.mu.Lock()
	after := app.authData
	app.mu.Unlock()

	if after != before {
		t.Fatal("失败时不能替换 authData")
	}
	if after.Session.LeaseID != beforeLease || after.FRPConfig.ServerAddr != beforeAddr {
		t.Fatal("失败时原配置必须原样保留")
	}
}

// 未登录时换服要明确报错，不能静默无反应。
func TestChangeServerRequiresLogin(t *testing.T) {
	m := newChangeServerMock(t)
	client, err := auth.NewClient(m.server.URL, m.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: config.NewMemoryStore()})

	if _, err := app.ChangeServer(); err == nil {
		t.Fatal("未登录时应报错")
	}
	if got := m.calls.Load(); got != 0 {
		t.Fatalf("未登录不该发请求，实际 %d 次", got)
	}
}

// 换服成功后雷达链接必须用新地址/端口（链接只在 Go 侧生成）。
func TestChangeServerUpdatesShareLink(t *testing.T) {
	m := newChangeServerMock(t)
	app := newChangeServerApp(t, m)
	t.Cleanup(func() { _ = app.stopTunnel() })

	var copied string
	app.deps.clipboard = func(_ context.Context, value string) error {
		copied = value
		return nil
	}

	// 换服后服务端把地址换到另一台
	m.serverAddr.Store("node-b.example")
	if _, err := app.ChangeServer(); err != nil {
		t.Fatalf("换服应成功：%v", err)
	}
	if err := app.CopyShareLink("12345678"); err != nil {
		t.Fatalf("复制链接失败：%v", err)
	}
	if !strings.Contains(copied, "node-b.example") {
		t.Fatalf("链接应使用换服后的新地址，实际 %q", copied)
	}
}
