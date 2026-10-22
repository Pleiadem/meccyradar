package monitor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"meccyradar/internal/auth"
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
	ExpiresIn int64
	Message   string
	// Timeout 标记这一轮测速是因为整轮超时被强制结束的，
	// 前端据此提示"测速超时，点击重试"而不是普通的测速失败。
	Timeout bool
	// Done 标记这一轮测速已结束（成功/失败/超时都算）。
	// 前端只能凭它解除"测速中"：中间样本也是成功事件，
	// 如果拿它当终态，界面上按钮会在测速还没结束时就重新可点。
	Done bool
	At   time.Time
}

// HeartbeatResult 是心跳成功后服务端带回的信息。
type HeartbeatResult struct {
	// ExpiresAt 是 ISO 8601 的卡密到期时间；空表示服务端没下发（旧版本）。
	ExpiresAt string
	// LeaseID 便于日志追踪，客户端不依赖它。
	LeaseID string
}

type Heartbeater interface {
	Heartbeat(context.Context, string) (HeartbeatResult, error)
}

type Hooks struct {
	Emit       func(Event)
	StopTunnel func(error)
	Log        func(string)
	// RecoverLease 在心跳失败时被调用，用于静默重登。
	// 返回 false 表示放弃（不重登/重登失败），monitor 会照常停止隧道。
	//
	// 分类（该不该重登、被顶掉要不要提示）全部由实现方决定：
	// monitor 只负责"心跳失败了，你要不要接"，不解析错误类型，
	// 免得把 auth 的业务语义漏进监控层。
	RecoverLease func(ctx context.Context, err error) (LeaseRecovery, bool)
	// RetryableHeartbeat 判断这次心跳失败是否值得重试（网络类：超时/断网/DNS）。
	//
	// 只有网络类才重试。业务错误（401/403/409）重试没有任何意义：
	// 租约失效了再发一百次也是 401，反而把重登时机拖后。
	RetryableHeartbeat func(err error) bool
	// UpdateExpiry 在心跳带回权威到期时间时被调用。
	//
	// 管理员在后台改期限，客户端只能从心跳得知。没有这个回调，
	// 延长了用户仍到点被踢，改成过期了用户还能一直用到重启。
	UpdateExpiry func(newExpireAtUnix int64)
	// RestartTunnel 在重登后 FRPC 配置发生变化时调用。
	// 配置没变就不要调：停一次隧道会让用户看到界面闪断。
	RestartTunnel func()
}

// LeaseRecovery 是一次静默重登的结果。
type LeaseRecovery struct {
	// LeaseID 是重登后拿到的租约。
	LeaseID string
	// ConfigChanged 表示服务端下发的 FRPC 配置（地址/端口/user/token）
	// 与当前隧道用的不同，必须重启 FRPC 才能生效。
	ConfigChanged bool
}

type Config struct {
	ServerAddr        string
	ServerPort        int
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
	// PingTimeout 整轮测速超时，留空用 pingRoundTimeout。
	PingTimeout time.Duration
	Now         func() time.Time
	Hooks       Hooks
}

const (
	// pingDialTimeout 节点测速的连接超时。
	pingDialTimeout = 2 * time.Second
	// probeDialTimeout 本地端口探测的超时：只判断本地端口是否可连，
	// 保持短超时以免拖慢状态刷新。
	probeDialTimeout = 500 * time.Millisecond
	// pingRoundTimeout 整轮测速的硬超时（10 次 × 2s + 余量）。
	// 这是"不卡死"的最后一道保险：即使某个 dial 不响应取消，
	// 整轮也会在这里被切断并上报超时。
	pingRoundTimeout = 25 * time.Second
)

// ErrPingInProgress 表示已有一轮测速在进行，用于拒绝并发重测。
var ErrPingInProgress = errors.New("测速进行中")

// 心跳网络类失败的重试策略。
const (
	// heartbeatRetryAttempts 是网络类心跳失败的重试次数。
	// 3 次 × (2s+4s+8s) 约 14 秒，够覆盖 Wi-Fi 切换/唤醒后的短暂断网，
	// 又不会让用户等太久才看到"真的连不上"。
	heartbeatRetryAttempts = 3
)

// heartbeatRetryBackoffNanos 是第一次重试前的等待，之后每次翻倍（2s/4s/8s）。
//
// 用 atomic 而不是普通变量：测试会调小它，而其他用例的 monitor goroutine
// 可能还在读，普通变量会被 -race 抓成数据竞争。
var heartbeatRetryBackoffNanos atomic.Int64

func init() { heartbeatRetryBackoffNanos.Store(int64(2 * time.Second)) }

// heartbeatRetryBackoff 返回当前退避间隔。
func heartbeatRetryBackoff() time.Duration {
	return time.Duration(heartbeatRetryBackoffNanos.Load())
}

// SetHeartbeatRetryBackoffForTest 让测试把退避间隔调小。
// 返回原值，调用方负责在 Cleanup 里还原。
func SetHeartbeatRetryBackoffForTest(value time.Duration) time.Duration {
	return time.Duration(heartbeatRetryBackoffNanos.Swap(int64(value)))
}

// HeartbeatRetryAttempts 暴露重试次数给测试断言用。
func HeartbeatRetryAttempts() int { return heartbeatRetryAttempts }

type Monitor struct {
	cfg Config
	// pinging 保证同一时刻只有一轮测速，避免连点重测造成协程堆积。
	pinging atomic.Bool

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
	// leaseID 会随静默重登更新，所以不能只放在 cfg 里（cfg 是值拷贝）。
	// 只在 monitor 自己的 goroutine 里读写。
	leaseID string
	// expireAtUnix 是当前生效的卡密到期时间。心跳会把它刷新成服务端下发的
	// 权威值，而 expiresIn() 每秒被读一次，所以必须走 mu 保护。
	expireAtUnix int64
}

func New(cfg Config) (*Monitor, error) {
	if cfg.ServerAddr == "" || cfg.ServerPort < 1 || cfg.ServerPort > 65535 {
		return nil, errors.New("缺少服务器地址或端口")
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
	return &Monitor{cfg: cfg, leaseID: cfg.LeaseID, expireAtUnix: cfg.ExpireAtUnix}, nil
}

func (m *Monitor) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return errors.New("监控已在运行")
	}
	// 每次启动都从 cfg 重新取一次租约：同一张 Monitor 不会被复用，
	// 但如果调用方在 Start 之前改了 cfg.LeaseID，这里要跟上。
	m.leaseID = m.cfg.LeaseID
	m.expireAtUnix = m.cfg.ExpireAtUnix
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
	// 连接时同步测速；出错（含"已有测速在进行"）都不影响隧道主循环。
	// measurePing 自身有整轮超时，不会把心跳/端口探测拖住。
	_ = m.measurePing(ctx)
	if !m.heartbeat(ctx) {
		return
	}

	heartbeatTicker := time.NewTicker(m.cfg.HeartbeatInterval)
	defer heartbeatTicker.Stop()
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
// measurePing 同步跑一轮测速（连接时调用），带重入保护。
func (m *Monitor) measurePing(ctx context.Context) error {
	if err := m.acquirePing(); err != nil {
		return err
	}
	defer m.pinging.Store(false)
	m.measureRound(ctx)
	return nil
}

// acquirePing 原子抢占测速权；已被占用返回 ErrPingInProgress。
func (m *Monitor) acquirePing() error {
	if !m.pinging.CompareAndSwap(false, true) {
		return ErrPingInProgress
	}
	return nil
}

// RemeasurePingAsync 供界面"点击重测"使用。
//
// 同步抢占标志位再异步执行：这样连续点击的第二次会立刻被拒绝，
// 而不是等协程起来才发现重复（那种竞态下两次都会跑完）。
func (m *Monitor) RemeasurePingAsync(ctx context.Context) error {
	if err := m.acquirePing(); err != nil {
		return err
	}
	go func() {
		defer m.pinging.Store(false)
		m.measureRound(ctx)
	}()
	return nil
}

func (m *Monitor) measureRound(ctx context.Context) {
	// 整轮硬超时：这是"绝不卡死"的兜底。
	// 单个 dial 有 2s 超时，但 10 次串起来最坏 20s，
	// 且无法排除某个 dial 不响应取消，所以整轮必须自己封顶。
	round := m.cfg.PingTimeout
	if round <= 0 {
		round = pingRoundTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, round)
	defer cancel()

	address := net.JoinHostPort(m.cfg.ServerAddr, fmt.Sprint(m.cfg.ServerPort))
	samples := make([]int64, 0, pingSamples)
	// 用独立的标记判断"是否有过成功样本"。
	// 不能用样本值 == 0 代替：本机开着 TUN/代理时握手可能本地完成，
	// 取整后正好是 0ms，那样会把成功的测速误判成"节点测速失败"。
	succeeded := false
	timedOut := false
	for i := 0; i < pingSamples; i++ {
		// 每轮开始前检查：整轮已超时或隧道已停止时立刻退出，
		// 不再发起新的 dial。
		if ctx.Err() != nil {
			timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
			break
		}
		started := m.cfg.Now()
		conn, err := m.cfg.PingDialContext(ctx, "tcp", address)
		if err != nil {
			// 单次失败不影响其余样本，也不丢弃已采集的样本。
			if ctx.Err() != nil {
				timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
				break
			}
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

	// 每轮测速必定以恰好一个 Done=true 事件收尾。
	// 这是前端能够解除"测速中"的唯一保证：无论成功、失败还是超时，
	// 都不能出现"这一轮没有任何终态事件"的情况。
	if timedOut {
		// 超时必须给出终态事件，否则界面会一直停在"测速中…"。
		m.emit(Event{
			Kind:      "tunnel:status",
			Status:    StatusError,
			Message:   "测速超时，点击重试",
			PingMS:    trimmedAverageMillis(samples),
			Timeout:   true,
			Done:      true,
			ExpiresIn: m.expiresIn(),
		})
		m.log("节点测速超时，已强制结束本轮")
		return
	}
	if !succeeded {
		m.emit(Event{Kind: "tunnel:status", Status: StatusError, Message: "节点测速失败", Done: true})
		m.log("节点 TCP 测速失败")
		return
	}
	// 正常跑完 10 个样本，补一个终态事件。
	m.emit(Event{
		Kind:      "tunnel:status",
		Status:    StatusConnected,
		PingMS:    trimmedAverageMillis(samples),
		Done:      true,
		ExpiresIn: m.expiresIn(),
	})
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
	if m.cfg.Heartbeater == nil || m.leaseID == "" {
		return true
	}
	result, err := m.cfg.Heartbeater.Heartbeat(ctx, m.leaseID)
	if err != nil {
		// 网络类失败先原地重试：笔记本合盖唤醒、Wi-Fi 切换、临时丢包
		// 都会让一两次心跳超时，这时候把隧道停掉纯属误伤。
		if m.shouldRetryHeartbeat(err) && m.retryHeartbeat(ctx, err) {
			return true
		}
		// 租约类失败走静默重登（被顶掉/网络类由实现方自己拒绝）。
		if m.recoverLease(ctx, err) {
			return true
		}
		m.emit(Event{Kind: "tunnel:status", Status: StatusError, Message: heartbeatFailureMessage(err)})
		m.log(heartbeatFailureMessage(err))
		m.stopTunnel(err)
		return false
	}
	// 心跳成功顺手刷新权威到期时间：管理员在后台改了期限，
	// 下一次心跳就能让本地判断跟上，不需要用户重启客户端。
	m.applyExpiresAt(result.ExpiresAt)
	return true
}

// shouldRetryHeartbeat 只有实现方明确认定为网络类时才重试。
// 没有配置判定函数时一律不重试，保持"立即停止"的保守行为。
func (m *Monitor) shouldRetryHeartbeat(err error) bool {
	if m.cfg.Hooks.RetryableHeartbeat == nil {
		return false
	}
	return m.cfg.Hooks.RetryableHeartbeat(err)
}

// retryHeartbeat 按指数退避重试心跳。返回 true 表示重试中恢复成功。
//
// 退避 2s / 4s / 8s，共 heartbeatRetryAttempts 次。重试期间**不动隧道状态**：
// 隧道还在跑，只是心跳这一路暂时不通，界面保持"运行中"比闪成错误更准确。
func (m *Monitor) retryHeartbeat(ctx context.Context, cause error) bool {
	delay := heartbeatRetryBackoff()
	for attempt := 1; attempt <= heartbeatRetryAttempts; attempt++ {
		m.log(fmt.Sprintf("心跳失败，%s 后重试（%d/%d）：%s",
			delay.Round(time.Millisecond), attempt, heartbeatRetryAttempts, heartbeatFailureMessage(cause)))
		select {
		case <-ctx.Done():
			// 隧道被用户停掉/监控退出：不用再重试，也不算失败。
			return false
		case <-time.After(delay):
		}
		if result, err := m.cfg.Heartbeater.Heartbeat(ctx, m.leaseID); err == nil {
			m.log("心跳已恢复")
			// 重试成功同样要刷新到期时间，否则管理员改期限后
			// 用户要等到下一次正常心跳才知道。
			m.applyExpiresAt(result.ExpiresAt)
			return true
		} else {
			cause = err
			if !m.shouldRetryHeartbeat(err) {
				// 重试途中变成了业务错误（例如租约刚好过期）：立刻交给
				// 重登/停止逻辑，别把剩下的重试次数浪费掉。
				return false
			}
		}
		delay *= 2
	}
	m.log(fmt.Sprintf("心跳连续失败 %d 次，已停止隧道", heartbeatRetryAttempts))
	return false
}

// heartbeatFailureMessage 优先用错误自带的业务文案。
// 被顶掉时服务端会回"该卡密已在其他设备登录，本机已停止"，
// 这比笼统的"心跳失败"有用得多。
func heartbeatFailureMessage(err error) string {
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		return err.Error()
	}
	return "心跳失败"
}

// recoverLease 尝试静默重登。返回 true 表示租约已换新，隧道可以继续跑。
func (m *Monitor) recoverLease(ctx context.Context, err error) bool {
	if m.cfg.Hooks.RecoverLease == nil {
		return false
	}
	recovery, ok := m.cfg.Hooks.RecoverLease(ctx, err)
	if !ok || recovery.LeaseID == "" {
		return false
	}
	m.leaseID = recovery.LeaseID
	m.log("登录状态已恢复，隧道继续运行")
	if recovery.ConfigChanged && m.cfg.Hooks.RestartTunnel != nil {
		// 配置变了（换服/换端口）必须重启 FRPC，否则连的还是旧资源。
		m.cfg.Hooks.RestartTunnel()
	}
	return true
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
	//
	// expireAtUnix 会被心跳刷新（管理员改期限），expiresIn 每秒读一次，
	// 所以必须加锁——否则是数据竞争。
	m.mu.Lock()
	expireAt := m.expireAtUnix
	m.mu.Unlock()
	return expireAt - m.cfg.Now().Unix()
}

// parseExpiresAt 解析 ISO 8601 到期时间。
//
// 复用 auth 的实现而不是在这里重写：两处解析规则一旦不一致，
// 就会出现"服务端下发了但客户端读不出来"这种很难查的问题。
func parseExpiresAt(value string) int64 { return auth.ParseExpiresAt(value) }

// RemainingSeconds 返回距离卡密到期的剩余秒数（可为负）。
//
// 导出给测试与上层查询用；内部每秒的到期检查走 expiresIn。
func (m *Monitor) RemainingSeconds() int64 { return m.expiresIn() }

// applyExpiresAt 用服务端下发的权威到期时间刷新本地值。
//
// 空串/格式错误一律忽略：把解析失败当成"到期时间为 0"会让客户端
// 立刻判定已过期，把正常用户踢下线——宁可保持旧值。
func (m *Monitor) applyExpiresAt(value string) {
	unix := parseExpiresAt(value)
	if unix <= 0 {
		return
	}
	m.mu.Lock()
	changed := m.expireAtUnix != unix
	m.expireAtUnix = unix
	m.mu.Unlock()
	if !changed {
		return
	}
	if m.cfg.Hooks.UpdateExpiry != nil {
		m.cfg.Hooks.UpdateExpiry(unix)
	}
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
