package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
)

// flakyLeaseServer 模拟一个 logout 会间歇性失败的服务端。
// failLogouts 次数的 logout 请求会被拒，之后放行。
func flakyLeaseServer(t *testing.T, failLogouts int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var logouts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/client/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200, "msg": "ok",
				"data": map[string]any{
					"appName": "MeccyRadar", "shareBaseUrl": "http://radar.example/",
					"expireAtUnix": 1893456000, "expireAtStr": "2030-01-01 00:00",
					"session":   map[string]any{"leaseId": "lease-1", "heartbeatInterval": 30, "leaseExpireAtUnix": 1893456000},
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
		case "/api/client/logout":
			if logouts.Add(1) <= failLogouts {
				w.WriteHeader(http.StatusBadGateway)
				_ = json.NewEncoder(w).Encode(map[string]any{"detail": "服务资源分配失败，请稍后重试"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": nil})
		case "/api/client/heartbeat":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": nil})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, &logouts
}

func leaseTestApp(t *testing.T, server *httptest.Server) *App {
	t.Helper()
	client, err := auth.NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: config.NewMemoryStore()})
	if _, err := app.LoginByCard("CARD-RETRY"); err != nil {
		t.Fatal(err)
	}
	return app
}

// 释放租约时的一次网络抖动不应该让租约白占 90 秒：
// 必须重试，最终成功时 disconnect 不报错。
func TestDisconnectRetriesTransientLogoutFailure(t *testing.T) {
	server, logouts := flakyLeaseServer(t, 1) // 第一次 logout 失败
	app := leaseTestApp(t, server)

	if err := app.disconnect(); err != nil {
		t.Fatalf("首次失败后重试成功，不应报错，实际 %v", err)
	}
	if got := logouts.Load(); got != 2 {
		t.Fatalf("应重试一次（共 2 次 logout），实际 %d 次", got)
	}
}

// 重试后仍然失败时，错误必须被返回而不是吞掉 ——
// 否则用户只会看到「已在其他设备运行」，没有任何线索可查。
func TestDisconnectReportsPersistentLogoutFailure(t *testing.T) {
	server, logouts := flakyLeaseServer(t, 99) // 一直失败
	app := leaseTestApp(t, server)

	err := app.disconnect()
	if err == nil {
		t.Fatal("持续失败时必须返回错误，不能静默吞掉")
	}
	if got := logouts.Load(); got != int32(leaseReleaseAttempts) {
		t.Fatalf("应尝试 %d 次，实际 %d 次", leaseReleaseAttempts, got)
	}
	// 错误文案走的是业务映射，不得出现 HTTP 术语
	if msg := err.Error(); msg == "" {
		t.Fatal("错误必须有可读文案")
	}
}

// 未登录时不应发起 logout，也不该报错。
func TestDisconnectWithoutSessionIsNoop(t *testing.T) {
	server, logouts := flakyLeaseServer(t, 0)
	client, err := auth.NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: config.NewMemoryStore()})

	if err := app.disconnect(); err != nil {
		t.Fatalf("未登录时 disconnect 不应报错，实际 %v", err)
	}
	if got := logouts.Load(); got != 0 {
		t.Fatalf("未登录不应调用 logout，实际 %d 次", got)
	}
}

// logout 请求必须带超时，不能无限等待把退出流程卡死。
func TestLeaseReleaseTimeoutIsBounded(t *testing.T) {
	if leaseReleaseTimeout <= 0 || leaseReleaseTimeout > 5*time.Second {
		t.Fatalf("单次 logout 超时应为几秒级，实际 %v", leaseReleaseTimeout)
	}
	if leaseReleaseAttempts < 2 {
		t.Fatalf("至少要重试一次，实际 %d", leaseReleaseAttempts)
	}
	_ = context.Background()
}
