package monitor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
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
	// PingDialContext 单独给测速用：海外节点 500ms 常常不够，
	// 但端口探测要的是"本地端口通不通"，必须保持短超时，两者不能混用。
	// 留空时：若 DialContext 是外部注入的（测试/自定义）则沿用它，
	// 否则用 pingDialTimeout 的默认 dialer。
	PingDialContext func(context.Context, string, string) (net.Conn, error)
	Now             func() time.Time
	Hooks           Hooks
}

const (
	// pingDialTimeout 节点测速的连接超时。
	pingDialTimeout = 2 * time.Second
	// probeDialTimeout 本地端口探测的超时：只判断本地端口是否可连，
	// 保持短超时以免拖慢状态刷新。
	probeDialTimeout = 500 * time.Millisecond
)

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
	customDial := cfg.DialContext
	if cfg.DialContext == nil {
		dialer := &net.Dialer{Timeout: probeDialTimeout}
		cfg.DialContext = dialer.DialContext
	}
	if cfg.PingDialContext == nil {
		if customDial != nil {
			// 外部注入了 dialer（测试或自定义实现）：沿用同一个，
			// 否则测试无法控制测速的连接行为。
			cfg.PingDialContext = customDial
		} else {
			pingDialer := &net.Dialer{Timeout: pingDialTimeout}
			cfg.PingDialContext = pingDialer.DialContext
		}
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
const pingSamples = 10

// measurePing 连测 pingSamples 次，并在每次成功后就上报"历史最低值"。
//
// 为什么每次都要上报而不是只报最终值：用户要的是"始终显示见过的最快延迟"，
// 首次成功就要立刻有数字，之后只在更快时更新。若只发一次最终值，界面在
// 整个测速期间都是空的，用户以为没在测。
func (m *Monitor) measurePing(ctx context.Context) {
	address := net.JoinHostPort(m.cfg.ServerAddr, fmt.Sprint(m.cfg.ServerPort))
	samples := make([]int64, 0, pingSamples)
	// 用独立的标记判断"是否有过成功样本"。
	// 不能用样本值 == 0 代替：本机开着 TUN/代理时握手可能本地完成，
	// 取整后正好是 0ms，那样会把成功的测速误判成"节点测速失败"。
	succeeded := false
	for i := 0; i < pingSamples; i++ {
		started := m.cfg.Now()
		conn, err := m.cfg.PingDialContext(ctx, "tcp", address)
		if err != nil {
			// 单次失败不影响其余样本，也不丢弃已采集的样本。
			continue
		}
		_ = conn.Close()
		samples = append(samples, durationMillis(m.cfg.Now().Sub(started)))
		succeeded = true
		// 每次成功都更新显示：当前有效样本（去极值后）的平均值。
		m.emit(Event{
			Kind:      "tunnel:status",
			Status:    StatusConnected,
			PingMS:    trimmedAverageMillis(samples),
			ExpiresIn: m.expiresIn(),
		})
	}
	if !succeeded {
		m.emit(Event{Kind: "tunnel:status", Status: StatusError, Message: "节点测速失败"})
		m.log("节点 TCP 测速失败")
	}
}

// MeasurePingNow 供界面"点击重测"调用，立即跑一轮测速。
func (m *Monitor) MeasurePingNow(ctx context.Context) {
	m.measurePing(ctx)
}

// durationMillis 把耗时换算为毫秒（四舍五入）。
//
// 这里不设 1ms 下限：亚毫秒握手就是 0ms，硬抬到 1ms 会显示一个
// 本地代理环境下永远不变的假数字。是否"测到了"由 succeeded 判断，
// 与显示值无关。
func durationMillis(d time.Duration) int64 {
	if d < 0 {
		d = 0
	}
	return int64(math.Round(float64(d) / float64(time.Millisecond)))
}

// trimmedAverageMillis 去掉 1 个最大值与 1 个最小值后取平均（四舍五入）。
//
// 去极值是为了滤掉偶发的一次慢握手（例如 100ms）把整体拉高。
// 样本少于 3 个时无法同时去掉两头，先按全部样本取平均。
func trimmedAverageMillis(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	if len(values) < 3 {
		return meanMillis(values)
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return meanMillis(sorted[1 : len(sorted)-1])
}

func meanMillis(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	var sum int64
	for _, value := range values {
		sum += value
	}
	return int64(math.Round(float64(sum) / float64(len(values))))
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
