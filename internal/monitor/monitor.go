package monitor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

type Status string

const (
	StatusIdle       Status = "idle"
	StatusConnecting Status = "connecting"
	StatusConnected  Status = "connected"
	StatusExpired    Status = "expired"
	StatusError      Status = "error"
)

type Event struct {
	Kind      string
	Status    Status
	PingMS    int64
	PortOpen  bool
	ExpiresIn int64
	Message   string
	At        time.Time
}

type Heartbeater interface {
	Heartbeat(context.Context, string) error
}

type Hooks struct {
	Emit       func(Event)
	StopTunnel func(error)
	Log        func(string)
}

type Config struct {
	ServerAddr        string
	ServerPort        int
	LocalAddr         string
	LeaseID           string
	HeartbeatInterval time.Duration
	LeaseExpireAtUnix int64
	ExpireAtUnix      int64
	Heartbeater       Heartbeater
	DialContext       func(context.Context, string, string) (net.Conn, error)
	Now               func() time.Time
	Hooks             Hooks
}

type Monitor struct {
	cfg Config

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
}

func New(cfg Config) (*Monitor, error) {
	if cfg.ServerAddr == "" || cfg.ServerPort < 1 || cfg.ServerPort > 65535 {
		return nil, errors.New("缺少服务器地址或端口")
	}
	if cfg.LocalAddr == "" {
		cfg.LocalAddr = "127.0.0.1:13133"
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.DialContext == nil {
		dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
		cfg.DialContext = dialer.DialContext
	}
	if cfg.ExpireAtUnix <= 0 {
		return nil, errors.New("缺少到期时间")
	}
	return &Monitor{cfg: cfg}, nil
}

func (m *Monitor) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return errors.New("监控已在运行")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.done = make(chan struct{})
	m.running = true
	go m.run(ctx, m.done)
	return nil
}

func (m *Monitor) Stop() error {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return errors.New("监控未运行")
	}
	cancel, done := m.cancel, m.done
	m.mu.Unlock()
	cancel()
	<-done
	return nil
}

func (m *Monitor) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func (m *Monitor) run(ctx context.Context, done chan struct{}) {
	defer func() {
		m.mu.Lock()
		m.running = false
		m.cancel = nil
		m.mu.Unlock()
		close(done)
	}()

	m.emit(Event{Kind: "tunnel:status", Status: StatusConnecting})
	if m.expired() {
		m.expire(errors.New("授权已过期"))
		return
	}
	m.measurePing(ctx)
	if !m.heartbeat(ctx) {
		return
	}
	m.probePort(ctx)

	heartbeatTicker := time.NewTicker(m.cfg.HeartbeatInterval)
	defer heartbeatTicker.Stop()
	portTicker := time.NewTicker(3 * time.Second)
	defer portTicker.Stop()
	expiryTicker := time.NewTicker(time.Second)
	defer expiryTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeatTicker.C:
			if m.expired() {
				m.expire(errors.New("授权已过期"))
				return
			}
			if !m.heartbeat(ctx) {
				return
			}
		case <-portTicker.C:
			m.probePort(ctx)
		case <-expiryTicker.C:
			if m.expired() {
				m.expire(errors.New("授权已过期"))
				return
			}
			m.emit(Event{Kind: "tunnel:status", Status: StatusConnected, ExpiresIn: m.expiresIn()})
		}
	}
}

// pingSamples 每次连接后测速的样本数。
const pingSamples = 5

// measurePing 连测 pingSamples 次，并在每次成功后就上报"历史最低值"。
//
// 为什么每次都要上报而不是只报最终值：用户要的是"始终显示见过的最快延迟"，
// 首次成功就要立刻有数字，之后只在更快时更新。若只发一次最终值，界面在
// 整个测速期间都是空的，用户以为没在测。
func (m *Monitor) measurePing(ctx context.Context) {
	address := net.JoinHostPort(m.cfg.ServerAddr, fmt.Sprint(m.cfg.ServerPort))
	var best int64
	for i := 0; i < pingSamples; i++ {
		started := m.cfg.Now()
		conn, err := m.cfg.DialContext(ctx, "tcp", address)
		if err != nil {
			// 单次失败不影响其余样本，也不把已得到的最低值丢掉。
			continue
		}
		_ = conn.Close()
		ms := m.cfg.Now().Sub(started).Milliseconds()
		if best == 0 || ms < best {
			best = ms
		}
		// 每次成功测量都上报"历史最低值"：界面首次成功立即有数字，
		// 之后即使本次更慢，显示的仍是见过的最快值（不会回退）。
		m.emit(Event{Kind: "tunnel:status", Status: StatusConnected, PingMS: best, ExpiresIn: m.expiresIn()})
	}
	if best == 0 {
		m.emit(Event{Kind: "tunnel:status", Status: StatusError, Message: "节点测速失败"})
		m.log("节点 TCP 测速失败")
	}
}

func (m *Monitor) heartbeat(ctx context.Context) bool {
	if m.cfg.Heartbeater == nil || m.cfg.LeaseID == "" {
		return true
	}
	if err := m.cfg.Heartbeater.Heartbeat(ctx, m.cfg.LeaseID); err != nil {
		m.emit(Event{Kind: "tunnel:status", Status: StatusError, Message: "心跳失败"})
		m.log("服务端心跳失败")
		m.stopTunnel(err)
		return false
	}
	return true
}

func (m *Monitor) probePort(ctx context.Context) {
	conn, err := m.cfg.DialContext(ctx, "tcp", m.cfg.LocalAddr)
	if err == nil {
		_ = conn.Close()
	}
	m.emit(Event{Kind: "port:status", PortOpen: err == nil})
}

func (m *Monitor) expire(err error) {
	m.emit(Event{Kind: "tunnel:status", Status: StatusExpired, ExpiresIn: 0, Message: err.Error()})
	m.log("授权已到期，隧道已停止")
	m.stopTunnel(err)
}

func (m *Monitor) expired() bool { return m.expiresIn() <= 0 }

func (m *Monitor) expiresIn() int64 {
	// 只以卡密到期时间为准。
	//
	// 租约到期时间（90 秒）是"多久没心跳就算掉线"的存活窗口，不是授权期限：
	// 每次心跳成功服务端都会把租约续到 now+90s。之前把它取 min 进来，导致
	// 登录 90 秒后必然判定"授权已到期"，与卡密的 30 天完全无关。
	//
	// 会话真实失效由心跳失败承担：heartbeat() 收到错误会停止隧道，
	// 所以这里不做租约上限也不会无限续期。
	return m.cfg.ExpireAtUnix - m.cfg.Now().Unix()
}

func (m *Monitor) stopTunnel(err error) {
	if m.cfg.Hooks.StopTunnel != nil {
		m.cfg.Hooks.StopTunnel(err)
	}
}

func (m *Monitor) emit(event Event) {
	if event.At.IsZero() {
		event.At = m.cfg.Now()
	}
	if m.cfg.Hooks.Emit != nil {
		m.cfg.Hooks.Emit(event)
	}
}

func (m *Monitor) log(message string) {
	if m.cfg.Hooks.Log != nil {
		m.cfg.Hooks.Log(message)
	}
}
