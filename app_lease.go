package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
	"meccyradar/internal/monitor"
)

// 租约自愈的边界。
//
// 线上故障：笔记本休眠一夜，心跳停了 8 小时，90 秒 TTL 早已过期。
// 唤醒后客户端拿着内存里的旧 leaseId 启动隧道，FRPC 的 token 还有效
// （所以日志里是 "start proxy success"），1 秒后首次心跳收到 401，隧道被停。
//
// 修法不是"心跳失败就无限重登"：那会让被顶掉的设备去顶回来，
// 两台机器互相踢成死循环。所以：
//   - 只有租约类失效才重登（网络类失败保持立即停止的原语义）
//   - 被其他设备顶掉（takeover）绝不重登，直接停并提示
//   - 单次恢复最多重试 leaseRecoverAttempts 次，且两次之间退避
//   - 连续 leaseRecoverEpisodes 个恢复回合都失败就彻底放弃，等用户手动操作
const (
	leaseRecoverAttempts = 3
	leaseRecoverEpisodes = 3
	// leaseTTL 与服务端 LEASE_TTL_SECONDS 一致：服务端每次心跳把
	// lease_expires_at 推到 now+90s，客户端据此估算手上的租约还有多久。
	leaseTTL = 90 * time.Second
)

// leaseRecoverBackoff 是两次重登之间的退避间隔。
// 用变量而不是常量，测试里调小才不用真的等 6 秒。
var leaseRecoverBackoff = 2 * time.Second

// leaseGuard 记录"手上的租约还能不能用"。
//
// 不能只看 authData.Session.LeaseExpireAtUnix：那是登录时下发的值，
// 之后服务端每次心跳都会往后推，客户端不同步就会误判成已过期。
// 所以由 heartbeatTracker 在每次心跳结果上刷新本地估算。
type leaseGuard struct {
	mu sync.Mutex
	// expiresAt 是本机估算的租约失效时刻；零值表示"没有有效租约"。
	expiresAt time.Time
	// lastReason 是最近一次心跳失败的原因（空串表示最近一次是成功）。
	lastReason string
	// failedEpisodes 是连续失败的恢复回合数，成功一次就清零。
	failedEpisodes int
}

func (g *leaseGuard) markHealthy(now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expiresAt = now.Add(leaseTTL)
	g.lastReason = ""
	g.failedEpisodes = 0
}

func (g *leaseGuard) markFailed(reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastReason = reason
}

func (g *leaseGuard) noteRecovered() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failedEpisodes = 0
}

// noteEpisodeFailed 记一次失败的恢复回合，返回是否已经用完所有回合。
func (g *leaseGuard) noteEpisodeFailed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failedEpisodes++
	return g.failedEpisodes >= leaseRecoverEpisodes
}

// stale 判断手上的租约是否**明知**已经不能用了。
//
// 判据有二：
//  1. 本地估算的租约到期时刻已过（休眠一夜就是这种）；
//  2. 上一次心跳就是租约类 401。
//
// 只要命中一条，就不该拿它去启动 FRPC —— 那只会得到
// "start proxy success + 1 秒后心跳 401" 的假成功。
func (g *leaseGuard) stale(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lastReason != "" && g.lastReason != auth.LeaseReasonTakeover {
		return true
	}
	if !g.expiresAt.IsZero() && !now.Before(g.expiresAt) {
		return true
	}
	return false
}

// heartbeatTracker 包一层 Heartbeater，把每次心跳结果喂给 leaseGuard。
//
// monitor 只知道自己心跳成功/失败，不知道"租约还剩多久"；
// 而 startTunnel 恰恰需要这个信息才能避免拿过期租约去启动。
type heartbeatTracker struct {
	inner    monitor.Heartbeater
	onResult func(err error)
}

func (t *heartbeatTracker) Heartbeat(ctx context.Context, leaseID string) error {
	err := t.inner.Heartbeat(ctx, leaseID)
	if t.onResult != nil {
		t.onResult(err)
	}
	return err
}

// isRetryableHeartbeat 判断心跳失败是否属于网络类（值得退避重试）。
//
// 只认 TransportError：请求没到达服务端才重试。
// 业务错误（401/403/409）重试再多次也是同样的结果，只会把重登时机拖后。
func isRetryableHeartbeat(err error) bool {
	var transportErr *auth.TransportError
	return errors.As(err, &transportErr)
}

// asStatusError 取出服务端业务错误；网络类失败返回 nil。
func asStatusError(err error) *auth.StatusError {
	var statusErr *auth.StatusError
	if errors.As(err, &statusErr) {
		return statusErr
	}
	return nil
}

// leaseFailureReason 归一化错误里的原因。老服务端没有 reason 字段 → unknown。
func leaseFailureReason(err error) (string, bool) {
	statusErr := asStatusError(err)
	if statusErr == nil {
		// 不是服务端业务错误（超时、断网、连接被拒）：不重登。
		return "", false
	}
	reason := strings.TrimSpace(statusErr.Reason)
	if reason == "" {
		reason = auth.LeaseReasonUnknown
	}
	return reason, true
}

// recoverLease 是 monitor.Hooks.RecoverLease 的实现。
//
// 返回 ok=false 时 monitor 会照常停止隧道并上报错误文案。
func (a *App) recoverLease(ctx context.Context, cause error) (monitor.LeaseRecovery, bool) {
	reason, isLeaseFailure := leaseFailureReason(cause)
	if !isLeaseFailure {
		// 网络类失败：本轮不改语义，直接停止。
		return monitor.LeaseRecovery{}, false
	}

	a.lease.markFailed(reason)

	if reason == auth.LeaseReasonTakeover {
		// 关键：绝不自动重登。重登会把另一台设备顶掉，对方再来顶，
		// 两台机器就会无限互踢，而且用户永远不知道该看哪台。
		a.emitLog("已在其他设备登录，本机已停止")
		a.emitDiagnosis("该卡密已在其他设备登录，本机已停止")
		return monitor.LeaseRecovery{}, false
	}
	if reason == auth.LeaseReasonLogout {
		// 本机主动退出过：重登等于把用户刚关掉的会话又拉起来。
		a.emitLog("本机已退出登录，请重新登录")
		return monitor.LeaseRecovery{}, false
	}

	a.mu.Lock()
	client, cardKey := a.deps.client, a.cardKey
	oldConfig := a.authData
	a.mu.Unlock()
	if client == nil || strings.TrimSpace(cardKey) == "" {
		return monitor.LeaseRecovery{}, false
	}

	a.emitLog("登录状态已超时，正在自动重新登录")
	var lastErr error
	for attempt := 1; attempt <= leaseRecoverAttempts; attempt++ {
		if attempt > 1 {
			delay := time.Duration(attempt-1) * leaseRecoverBackoff
			select {
			case <-ctx.Done():
				return monitor.LeaseRecovery{}, false
			case <-time.After(delay):
			}
		}
		data, err := client.Login(ctx, cardKey)
		if err == nil {
			recovery := a.adoptRefreshedAuth(data, oldConfig)
			a.lease.noteRecovered()
			a.emitLog("登录状态已恢复")
			return recovery, true
		}
		lastErr = err
		if nextReason, ok := leaseFailureReason(err); ok && nextReason == auth.LeaseReasonTakeover {
			// 重登过程中被别的设备抢先顶掉了：同样不能继续。
			a.emitLog("已在其他设备登录，本机已停止")
			a.emitDiagnosis("该卡密已在其他设备登录，本机已停止")
			return monitor.LeaseRecovery{}, false
		}
	}
	if lastErr != nil {
		a.emitLog("自动重新登录失败：" + lastErr.Error())
	}
	if a.lease.noteEpisodeFailed() {
		a.emitDiagnosis("自动重新登录多次失败，请手动重新登录")
	}
	return monitor.LeaseRecovery{}, false
}

// adoptRefreshedAuth 把重登拿到的新会话写回内存与磁盘，返回 monitor 需要的恢复结果。
func (a *App) adoptRefreshedAuth(data auth.AuthData, previous *auth.AuthData) monitor.LeaseRecovery {
	a.mu.Lock()
	a.authData = &data
	radarPassword := a.radarPassword
	cardKey := a.cardKey
	a.mu.Unlock()

	if a.deps.store != nil {
		// 与 LoginByCard 一样：只写 CardKey 会把已保存的雷达密码抹掉。
		_ = a.deps.store.Save(config.Session{
			CardKey:       cardKey,
			SavedAtUnix:   time.Now().Unix(),
			RadarPassword: radarPassword,
		})
	}
	a.lease.markHealthy(time.Now())

	configChanged := previous == nil || !frpConfigEqual(previous.FRPConfig, data.FRPConfig)
	return monitor.LeaseRecovery{LeaseID: data.Session.LeaseID, ConfigChanged: configChanged}
}

// frpConfigEqual 判断两份 FRPC 配置是否等价。
//
// 只有地址/端口/用户/token/代理集合全一致才算"没变"：
// 变了就必须重启 FRPC，否则隧道还连着旧资源，界面却显示已恢复。
func frpConfigEqual(left, right auth.FRPConfig) bool {
	if left.ServerAddr != right.ServerAddr ||
		left.ServerPort != right.ServerPort ||
		left.User != right.User ||
		left.Token != right.Token {
		return false
	}
	if len(left.Proxies) != len(right.Proxies) {
		return false
	}
	for i := range left.Proxies {
		l, r := left.Proxies[i], right.Proxies[i]
		if l.Name != r.Name || l.Type != r.Type ||
			l.LocalIP != r.LocalIP || l.LocalPort != r.LocalPort ||
			l.RemotePort != r.RemotePort || l.Subdomain != r.Subdomain {
			return false
		}
		if len(l.CustomDomains) != len(r.CustomDomains) {
			return false
		}
		for j := range l.CustomDomains {
			if l.CustomDomains[j] != r.CustomDomains[j] {
				return false
			}
		}
	}
	return true
}

// handleHeartbeatResult 是每次心跳后的回调（由 heartbeatTracker 调用）。
//
// 成功就刷新本地估算的租约到期时刻；失败记下原因，让下一次
// StartTunnel 知道"这个租约不能用了"。
func (a *App) handleHeartbeatResult(err error) {
	if err == nil {
		a.lease.markHealthy(time.Now())
		return
	}
	if reason, ok := leaseFailureReason(err); ok {
		a.lease.markFailed(reason)
	}
}

// restartTunnelAfterRecovery 在重登后配置变化时重建隧道。
//
// 先停旧的再起新的：FRPC 配置（server/port/user/token）变了，
// 继续用旧进程等于连着一个已经不存在的资源。
func (a *App) restartTunnelAfterRecovery() error {
	if err := a.stopTunnel(); err != nil {
		a.emitLog("重建隧道时停止旧连接失败：" + err.Error())
	}
	return a.startTunnel()
}

// refreshStaleLeaseBeforeStart 在启动 FRPC 之前，把明知已失效的租约换掉。
//
// 不这么做就会出现线上那个假成功：FRPC 用还没过期的 token 连上了
// （日志 "start proxy success"），1 秒后心跳 401 才把隧道停掉，
// 用户看到的是"启动成功然后立刻断开"。
func (a *App) refreshStaleLeaseBeforeStart(ctx context.Context) error {
	a.mu.Lock()
	client, cardKey, data := a.deps.client, a.cardKey, a.authData
	a.mu.Unlock()
	if client == nil || data == nil || strings.TrimSpace(cardKey) == "" {
		return nil
	}
	if !a.lease.stale(time.Now()) {
		return nil
	}
	a.emitLog("检测到登录状态可能已过期，正在重新登录")
	refreshed, err := client.Login(ctx, cardKey)
	if err != nil {
		// 被别的设备顶掉时不能"重试到成功"：直接如实上报。
		return err
	}
	a.adoptRefreshedAuth(refreshed, data)
	return nil
}
