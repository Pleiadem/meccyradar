package monitor

import (
	"context"
	"net"
	"testing"
	"time"
)

func pingMonitor(t *testing.T, latencies []int64, dialErr func(i int) error) (*Monitor, *[]int64) {
	t.Helper()
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	calls := 0
	pings := &[]int64{}
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
			if dialErr != nil {
				if err := dialErr(idx); err != nil {
					return nil, err
				}
			}
			clock.advance(time.Duration(latencies[idx]) * time.Millisecond)
			conn, peer := net.Pipe()
			_ = peer.Close()
			return conn, nil
		},
		Hooks: Hooks{
			Emit: func(event Event) {
				// 排除终态 Done 事件：它带的是同一个平均值，
				// 算进来会让"每个成功样本各上报一次"的计数多一。
				if event.Kind == "tunnel:status" && event.PingMS > 0 && !event.Done {
					*pings = append(*pings, event.PingMS)
				}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return mon, pings
}

// 10 次采样、去掉 1 个最大值和 1 个最小值，其余取平均（四舍五入）。
func TestPingAveragesAfterDroppingExtremes(t *testing.T) {
	latencies := []int64{10, 11, 12, 13, 14, 15, 16, 17, 18, 100}
	mon, pings := pingMonitor(t, latencies, nil)
	mon.measurePing(context.Background())

	if len(*pings) != len(latencies) {
		t.Fatalf("每次成功都应更新显示，期望 %d 次，实际 %v", len(latencies), *pings)
	}
	// 去掉 10 与 100 → 11..18 共 8 个 → 116/8 = 14.5 → 四舍五入 15
	final := (*pings)[len(*pings)-1]
	if final != 15 {
		t.Fatalf("去极值后平均值应为 15，实际 %d（完整序列 %v）", final, *pings)
	}
	// 证明是平均值而不是最小值
	if final == 10 {
		t.Fatalf("不应显示最小值 10，实际 %v", *pings)
	}
}

// 样本不足 3 个时无法同时去掉最大和最小，先按全部取平均。
func TestPingAveragesEarlySamples(t *testing.T) {
	latencies := []int64{10, 11, 12, 100, 100, 100, 100, 100, 100, 100}
	mon, pings := pingMonitor(t, latencies, nil)
	mon.measurePing(context.Background())

	if len(*pings) < 3 {
		t.Fatalf("应至少上报 3 次，实际 %v", *pings)
	}
	// 第 1 次只有 10 → 10；第 2 次 (10+11)/2 = 10.5 → 四舍五入 11；
	// 第 3 次够 3 个样本，去掉 10 与 12 只剩 11 → 11
	if (*pings)[0] != 10 {
		t.Fatalf("第 1 次应为 10，实际 %v", *pings)
	}
	if (*pings)[1] != 11 {
		t.Fatalf("第 2 次应为 10.5 四舍五入 11，实际 %v", *pings)
	}
	if (*pings)[2] != 11 {
		t.Fatalf("第 3 次去极值后应为 11，实际 %v", *pings)
	}
}

// 全部失败仍要报"节点测速失败"，且只报一次。
func TestPingAllFailuresStillReported(t *testing.T) {
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
		Hooks: Hooks{Emit: func(event Event) {
			if event.Kind == "tunnel:status" && event.Status == StatusError {
				failures++
			}
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mon.measurePing(context.Background())

	if failures != 1 {
		t.Fatalf("全部失败应恰好报一次，实际 %d", failures)
	}
}

// 部分失败时只统计成功样本，且不因失败样本而报失败。
func TestPingPartialFailuresUseSuccessfulSamplesOnly(t *testing.T) {
	latencies := []int64{20, 20, 20, 20, 20, 20, 20, 20, 20, 20}
	failed := 0
	mon, pings := pingMonitor(t, latencies, func(i int) error {
		if i == 0 || i == 5 {
			failed++
			return context.DeadlineExceeded
		}
		return nil
	})
	mon.measurePing(context.Background())

	if len(*pings) != 8 {
		t.Fatalf("8 个成功样本应各上报一次，实际 %v", *pings)
	}
	for _, value := range *pings {
		if value != 20 {
			t.Fatalf("样本全为 20ms，平均值应为 20，实际 %v", *pings)
		}
	}
}

// 采样次数必须是 10 次。
func TestPingSampleCountIsTen(t *testing.T) {
	if pingSamples != 10 {
		t.Fatalf("采样次数应为 10，实际 %d", pingSamples)
	}
}

// trimmedAverage 是去极值平均的核心，单独钉住边界。
func TestTrimmedAverageDropsOneEachSide(t *testing.T) {
	cases := []struct {
		name   string
		values []int64
		want   int64
	}{
		{"10 个样本去两头", []int64{10, 11, 12, 13, 14, 15, 16, 17, 18, 100}, 15},
		{"单个样本原样返回", []int64{7}, 7},
		{"两个样本取平均", []int64{10, 11}, 11},
		{"三个样本去两头只剩中间", []int64{5, 9, 40}, 9},
		{"空样本返回 0", nil, 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := trimmedAverageMillis(testCase.values); got != testCase.want {
				t.Fatalf("期望 %d，实际 %d", testCase.want, got)
			}
		})
	}
}
