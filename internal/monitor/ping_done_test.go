package monitor

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// doneRecorder 记录所有事件，并统计 Done 终态事件。
type doneRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *doneRecorder) record(event Event) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *doneRecorder) doneEvents() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, event := range r.events {
		if event.Done {
			out = append(out, event)
		}
	}
	return out
}

func (r *doneRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// slowDialMonitor 每次 dial 都成功，但会先等 delay，便于制造"一轮还在跑"的窗口。
func slowDialMonitor(t *testing.T, delay time.Duration, round time.Duration) (*Monitor, *doneRecorder) {
	t.Helper()
	recorder := &doneRecorder{}
	mon, err := New(Config{
		ServerAddr:        "node.example",
		ServerPort:        7000,
		HeartbeatInterval: time.Hour,
		LeaseExpireAtUnix: time.Now().Add(time.Hour).Unix(),
		ExpireAtUnix:      time.Now().Add(24 * time.Hour).Unix(),
		PingTimeout:       round,
		Now:               time.Now,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			conn, peer := net.Pipe()
			_ = peer.Close()
			return conn, nil
		},
		Hooks: Hooks{Emit: recorder.record},
	})
	if err != nil {
		t.Fatal(err)
	}
	return mon, recorder
}

// 正常跑完的一轮必须以恰好一个 Done 事件收尾。
// 这是前端解除"测速中"的唯一依据，多一个会让按钮提前可点，
// 少一个会让界面永久卡住。
func TestSuccessfulRoundEmitsExactlyOneDoneEvent(t *testing.T) {
	mon, recorder := slowDialMonitor(t, time.Millisecond, 5*time.Second)

	if err := mon.measurePing(context.Background()); err != nil {
		t.Fatal(err)
	}

	done := recorder.doneEvents()
	if len(done) != 1 {
		t.Fatalf("应恰好一个 Done 事件，实际 %d（全部事件 %+v）", len(done), recorder.events)
	}
	if done[0].Status != StatusConnected {
		t.Fatalf("成功轮次的 Done 事件应为 connected，实际 %s", done[0].Status)
	}
	// 终态必须是最后一个事件，否则前端会在事件之后继续等
	if last := recorder.events[recorder.count()-1]; !last.Done {
		t.Fatalf("Done 事件必须是最后一个，实际最后一个 %+v", last)
	}
}

// 全部失败的一轮同样要有且仅有一个 Done 事件。
func TestFailedRoundEmitsExactlyOneDoneEvent(t *testing.T) {
	recorder := &doneRecorder{}
	mon, err := New(Config{
		ServerAddr:        "node.example",
		ServerPort:        7000,
		HeartbeatInterval: time.Hour,
		LeaseExpireAtUnix: time.Now().Add(time.Hour).Unix(),
		ExpireAtUnix:      time.Now().Add(24 * time.Hour).Unix(),
		Now:               time.Now,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, context.DeadlineExceeded
		},
		Hooks: Hooks{Emit: recorder.record},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = mon.measurePing(context.Background())

	done := recorder.doneEvents()
	if len(done) != 1 {
		t.Fatalf("失败轮次应恰好一个 Done 事件，实际 %d", len(done))
	}
	if done[0].Status != StatusError {
		t.Fatalf("失败轮次的 Done 事件应为 error，实际 %s", done[0].Status)
	}
}

// 超时的一轮同样要有且仅有一个 Done 事件。
func TestTimedOutRoundEmitsExactlyOneDoneEvent(t *testing.T) {
	recorder := &doneRecorder{}
	mon, err := New(Config{
		ServerAddr:        "node.example",
		ServerPort:        7000,
		HeartbeatInterval: time.Hour,
		LeaseExpireAtUnix: time.Now().Add(time.Hour).Unix(),
		ExpireAtUnix:      time.Now().Add(24 * time.Hour).Unix(),
		PingTimeout:       60 * time.Millisecond,
		Now:               time.Now,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Hooks: Hooks{Emit: recorder.record},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = mon.measurePing(context.Background())

	done := recorder.doneEvents()
	if len(done) != 1 {
		t.Fatalf("超时轮次应恰好一个 Done 事件，实际 %d", len(done))
	}
	if !done[0].Timeout {
		t.Fatalf("超时轮次的 Done 事件应带 Timeout 标记，实际 %+v", done[0])
	}
}

// 快速连点两次：第二次必须被拒绝，且正在进行的那一轮仍要发出 Done 事件。
// 这是"第二次点击不会把界面卡死"的根本保证。
func TestRapidDoubleClickStillEmitsDoneForRunningRound(t *testing.T) {
	mon, recorder := slowDialMonitor(t, 15*time.Millisecond, 5*time.Second)

	// 第一次点击：异步启动一轮
	if err := mon.RemeasurePingAsync(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 第二次点击：必须立刻被拒绝
	err := mon.RemeasurePingAsync(context.Background())
	if !errors.Is(err, ErrPingInProgress) {
		t.Fatalf("第二次点击应被拒绝，实际 %v", err)
	}

	// 被拒绝后，第一轮仍必须正常收尾并发出 Done —— 前端靠它解除"测速中"
	waitFor(t, func() bool { return len(recorder.doneEvents()) == 1 })

	done := recorder.doneEvents()
	if len(done) != 1 {
		t.Fatalf("应恰好一个 Done 事件，实际 %d", len(done))
	}
	if done[0].Status != StatusConnected {
		t.Fatalf("Done 事件应为 connected，实际 %s", done[0].Status)
	}
}

// 中间样本不得带 Done，否则前端会在测速未结束时解除"测速中"。
func TestIntermediateSamplesAreNotDone(t *testing.T) {
	mon, recorder := slowDialMonitor(t, time.Millisecond, 5*time.Second)

	if err := mon.measurePing(context.Background()); err != nil {
		t.Fatal(err)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	samples := 0
	for i, event := range recorder.events {
		if event.Done {
			continue
		}
		if event.Status == StatusConnected {
			samples++
			if i == len(recorder.events)-1 {
				t.Fatal("最后一个事件不能是未标记 Done 的中间样本")
			}
		}
	}
	if samples != pingSamples {
		t.Fatalf("应有 %d 个中间样本，实际 %d", pingSamples, samples)
	}
}
