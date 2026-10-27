package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"meccyradar/internal/auth"
	"meccyradar/internal/config"
	"meccyradar/internal/diagnosis"
	"meccyradar/internal/frpengine"
	"meccyradar/internal/monitor"
)

const (
	managementURL = "MECCYRADAR_MANAGEMENT_URL"
	// 指向真实 RentForge 管理端。mock 服务器（cmd/mockserver）只用于自测，
	// 需要时用 MECCYRADAR_MANAGEMENT_URL 显式覆盖，绝不能做默认值：
	// 连到 mock 会把所有真实卡密都判成无效，报错还长得像卡密本身有问题。
	defaultManagementURL = "http://43.138.163.188:10003/"
	radarProxyName       = "radar_tcp"
	// 雷达分享域名固定为公网地址。
	// 不能依赖服务端下发的 shareBaseUrl：RentForge 的 client_share_base_url
	// 默认值就是管理端地址，照抄会让用户拿到一个指向管理端的无效链接。
	defaultRadarBaseURL = "http://radar.fastcheat.top/"
	// 仅用于本地开发/mock 覆盖；填回环地址会被忽略。
	radarBaseURLEnv = "MECCYRADAR_RADAR_BASE_URL"
	// leaseReleaseAttempts 释放租约的重试次数。
	// 退出时网络抖一下就会漏掉 logout，租约要白占 90 秒（服务端 TTL），
	// 期间用户重启客户端只会一直看到「已在其他设备运行」。
	leaseReleaseAttempts = 2
	// leaseReleaseTimeout 单次 logout 的超时。
	// 不能太长：退出流程要等它，点「退出」卡十几秒比漏发一次更糟。
	leaseReleaseTimeout = 3 * time.Second
)

var (
	errNotAuthenticated  = errors.New("尚未登录")
	errRadarProxyMissing = errors.New("未配置 radar_tcp 代理")
)

type InitStateDTO struct {
	Authenticated bool   `json:"authenticated"`
	AppName       string `json:"appName,omitempty"`
	ExpireAtStr   string `json:"expireAtStr,omitempty"`
	ExpireAtUnix  int64  `json:"expireAtUnix,omitempty"`
	ExpiresIn     int64  `json:"expiresIn,omitempty"`
	TunnelStatus  string `json:"tunnelStatus"`
}

type AuthResultDTO struct {
	AppName      string `json:"appName"`
	ExpireAtStr  string `json:"expireAtStr"`
	ExpireAtUnix int64  `json:"expireAtUnix"`
	TunnelStatus string `json:"tunnelStatus"`
	Warning      string `json:"warning,omitempty"`
}

type TunnelStatusDTO struct {
	Status    string `json:"status"`
	PingMS    int64  `json:"pingMs,omitempty"`
	ExpiresIn int64  `json:"expiresIn,omitempty"`
	Message   string `json:"message,omitempty"`
	// Done 表示这一轮测速已结束，前端据此解除"测速中"。
	Done bool `json:"done,omitempty"`
	// Timeout 表示这一轮是因为整轮超时被强制结束的。
	Timeout bool `json:"timeout,omitempty"`
}

type PortStatusDTO struct {
	Open bool `json:"open"`
}

type DiagnosisDTO struct {
	Level string `json:"level"`
	Title string `json:"title"`
	Hint  string `json:"hint,omitempty"`
	Raw   string `json:"raw,omitempty"`
}

type LogEventDTO struct {
	Message string `json:"message"`
	At      string `json:"at"`
}

type appDependencies struct {
	store     config.CredentialStore
	client    *auth.Client
	clipboard func(context.Context, string) error
	// settings 存用户偏好（开机自启等）。与凭据分开：退出登录不能把偏好清掉。
	settings autostartStore
	// autostart 是系统启动项操作；非 Windows 平台为 nil。
	autostart autostartManager
}

type loginAttempt struct {
	cardKey    string
	generation uint64
	waiters    int
	done       chan struct{}
	data       auth.AuthData
	err        error
}

type App struct {
	ctx context.Context

	mu               sync.Mutex
	changeMu         sync.Mutex
	loginMu          sync.Mutex
	loginInFlight    *loginAttempt
	loginGeneration  uint64
	logoutInProgress bool
	// Test hooks make lifecycle race windows deterministic without affecting production.
	loginBeforeJoin   func(string)
	loginBeforeCommit func()
	deps              appDependencies
	authData          *auth.AuthData
	cardKey           string
	// 雷达密码：来自界面输入，随会话持久化，重启后自动带出。
	radarPassword string
	engine        *frpengine.Engine
	monitor       *monitor.Monitor
	lastStatus    string
	initErr       error
	// lease 记录手上的租约还能不能用，供 StartTunnel 判断要不要先重登。
	lease leaseGuard
	// logEmitter 是测试用的日志出口；生产为 nil，走 runtime.EventsEmit。
	logEmitter func(LogEventDTO)
	// diagnosisEmitter mirrors logEmitter for recovery-message tests.
	diagnosisEmitter func(string)
	// leaseRetryBackoff 是重登退避间隔；0 表示用默认值。测试里调小用。
	leaseRetryBackoff time.Duration
	// 本地端口探测循环（app_port.go）。与隧道无关，登录后一直跑。
	portProbeCancel context.CancelFunc
	portProbeDone   chan struct{}
	portProbeKnown  bool
	portProbeOpen   bool
	// portProbeInterval 是该 App 的探测周期；零值使用默认 2 秒。实例字段避免
	// 测试改包级 interval 时与另一个用例尚未退出的探测 goroutine 产生 race。
	portProbeInterval time.Duration
	// version gate is verified before any session restoration or tunnel operation.
	versionGate         bool
	versionInfo         auth.VersionInfo
	versionGateState    versionGateState
	versionCheckPending bool
	versionCheckError   string
	versionCheckMu      sync.Mutex
	versionUseMu        sync.RWMutex
	versionStateFile    string
	// autostartMode 表示本次是开机自启拉起来的：窗口隐藏、自动登录并连隧道。
	autostartMode bool
}

// resolveManagementURL returns the management endpoint, defaulting to the real
// RentForge service. The mock server must be opted into explicitly.
func resolveManagementURL() string {
	if value := strings.TrimSpace(os.Getenv(managementURL)); value != "" {
		return value
	}
	return defaultManagementURL
}

func NewApp() *App {
	store, storeErr := config.NewDefaultStore()
	baseURL := resolveManagementURL()
	client, clientErr := auth.NewClient(baseURL, nil)
	settingsStore, _ := config.NewDefaultSettingsStore()
	autostart, _ := newPlatformAutostart()
	app := newApp(appDependencies{
		store:     store,
		client:    client,
		clipboard: runtime.ClipboardSetText,
		settings:  settingsStore,
		autostart: autostart,
	})
	if storeErr != nil {
		app.initErr = storeErr
	} else if clientErr != nil {
		app.initErr = clientErr
	}
	return app
}

func newApp(deps appDependencies) *App {
	return &App{deps: deps, lastStatus: "idle"}
}

func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	a.ctx = ctx
	a.mu.Unlock()
	// 已持久化的强制要求在网络检查启动前同步加载，离线时也不会被绕过。
	a.loadLocalVersionGate()
	// Windows 需要在启动时就挂上托盘：窗口关闭后是隐藏而非退出，
	// 没有托盘就再也点不回来了。
	a.startTray()
	a.handleProcessSignals()
	// 设置与系统启动项可能不一致（重装、手动删快捷方式、绿色版换目录），
	// 启动时对齐一次。
	a.syncAutostart()
	// 开机自启拉起时自动恢复会话并连接；普通启动沿用同一恢复行为。
	// StartHidden 由 main.go 控制，startup 时无需再显示/隐藏窗口。
	go a.restoreCachedSession()
	go func() {
		if err := a.CheckVersion(); err != nil {
			log.Printf("MeccyRadar version check did not complete: %v", err)
		}
	}()
}

// handleProcessSignals 兜住 SIGINT/SIGTERM 这类非界面退出路径。
//
// 正常退出（托盘/界面按钮）会走 Quit() 释放租约；但被 Ctrl+C 或
// 服务管理器终止时不会。这里补上，避免租约白占 90 秒（服务端 TTL）。
// 注意：任务管理器强杀进程无法被任何处理器捕获，那种情况只能等
// 服务端回收过期租约（RentForge 的 _release_stale_leases）。
func (a *App) handleProcessSignals() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		if _, ok := <-signals; !ok {
			return
		}
		a.Quit()
	}()
}

// restoreWindow 把隐藏或最小化的窗口拉回前台。
// 重复启动时由单实例回调触发：用户看到的是"唤回已经开着的那一个"，
// 而不是新起一个进程去登录被拒。
func (a *App) restoreWindow() {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		return
	}
	runtime.WindowShow(ctx)
	runtime.WindowUnminimise(ctx)
}

// disconnect 停止隧道并释放服务端租约，但保留本地缓存的卡密，
// 使下次启动仍能自动恢复。这和「退出登录」(Logout) 不同，后者会清凭据。
func (a *App) disconnect() error {
	// 先停隧道并等它真正结束（monitor.Stop 会 <-done），
	// 避免心跳在 logout 之后还发一次。
	stopErr := a.stopTunnel()

	a.mu.Lock()
	client, data := a.deps.client, a.authData
	a.authData, a.cardKey, a.lastStatus = nil, "", "idle"
	a.mu.Unlock()

	if client == nil || data == nil || data.Session.LeaseID == "" {
		return stopErr
	}

	leaseID := data.Session.LeaseID
	var lastErr error
	for attempt := 1; attempt <= leaseReleaseAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), leaseReleaseTimeout)
		err := client.Logout(ctx, leaseID)
		cancel()
		if err == nil {
			return stopErr
		}
		lastErr = err
		a.logf("释放租约失败（第 %d/%d 次）：%v", attempt, leaseReleaseAttempts, err)
	}
	return lastErr
}

// logf 同时写界面日志与进程标准日志。
//
// 退出路径上的失败必须留下痕迹：界面马上就没了，只有标准日志能事后查。
func (a *App) logf(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	log.Printf("%s", message)
	a.emitLog(message)
}

// shutdown 在应用真正退出时执行：断开连接、释放租约。
// 关闭窗口只是隐藏（HideWindowOnClose），不会走到这里，隧道保持在线。
func (a *App) shutdown(_ context.Context) {
	if err := a.disconnect(); err != nil {
		a.logf("退出时释放租约失败，服务端将在 90 秒后自动回收：%v", err)
	}
}

// hideWindow 只隐藏窗口，进程与隧道都继续运行。
func (a *App) hideWindow() {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx != nil {
		runtime.WindowHide(ctx)
	}
}

// shouldQuitOnClose 决定点关闭按钮时是"隐藏"还是"真退出"。
//
// 登录后有连接要维持，关窗只隐藏；未登录时没有任何需要维持的东西，
// 关窗就应该真的退出 —— 否则用户以为关掉了，进程却挂在托盘里，
// 再次双击图标还会被单实例挡回去，看起来像"打不开"。
func (a *App) shouldQuitOnClose() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.authData == nil
}

// CloseWindow 处理界面上的关闭按钮。
//
// 窗口是无边框的（Frameless），界面上那个 X 是唯一的关闭入口，
// 所以行为必须由后端判断：登录后隐藏并保持连接，未登录则真正退出。
func (a *App) CloseWindow() {
	if a.shouldQuitOnClose() {
		a.Quit()
		return
	}
	a.hideWindow()
}

// Quit 供界面或托盘调用：先断开再退出，避免退出后卡密仍被占用。
func (a *App) Quit() {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	// 释放失败不能拦住退出（用户点了退出就该退出），但必须留记录：
	// 这种情况用户下次登录会撞上「已在其他设备运行」，日志是唯一线索。
	if err := a.disconnect(); err != nil {
		a.logf("退出时释放租约失败，服务端将在 90 秒后自动回收：%v", err)
	}
	if ctx != nil {
		runtime.Quit(ctx)
	}
}

// loadSession 从凭据存储恢复卡密与雷达密码（供启动流程与测试使用）。
func (a *App) loadSession() error {
	a.mu.Lock()
	store := a.deps.store
	a.mu.Unlock()
	if store == nil {
		return errors.New("凭据存储不可用")
	}
	session, err := store.Load()
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.cardKey = session.CardKey
	a.radarPassword = session.RadarPassword
	a.mu.Unlock()
	return nil
}

// RadarPassword 返回已保存的雷达密码，供界面初始化时回填。
func (a *App) RadarPassword() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.radarPassword
}

// SaveRadarPassword 记住雷达密码，跨重启生效。
func (a *App) SaveRadarPassword(password string) error {
	a.mu.Lock()
	a.radarPassword = password
	cardKey, store := a.cardKey, a.deps.store
	a.mu.Unlock()
	if store == nil || cardKey == "" {
		// 未登录时只留在内存，登录后由会话保存带走。
		return nil
	}
	return store.Save(config.Session{
		CardKey:       cardKey,
		SavedAtUnix:   time.Now().Unix(),
		RadarPassword: password,
	})
}

var errSessionAlreadyAuthenticated = errors.New("当前已登录，请先退出当前会话")

func (a *App) restoreCachedSession() {
	a.versionUseMu.RLock()
	if a.versionBlocked() {
		a.versionUseMu.RUnlock()
		return
	}
	a.mu.Lock()
	// A manual login may have completed just before this background restoration
	// began. Do not issue another lease that would invalidate that live monitor;
	// this is only a duplicate-operation guard, not server-side lease validation.
	if a.authData != nil {
		a.mu.Unlock()
		a.versionUseMu.RUnlock()
		return
	}
	store, client, initErr := a.deps.store, a.deps.client, a.initErr
	a.mu.Unlock()
	if initErr != nil || store == nil || client == nil {
		a.versionUseMu.RUnlock()
		return
	}
	session, err := store.Load()
	if err != nil {
		a.versionUseMu.RUnlock()
		return
	}
	if hook := a.loginBeforeJoin; hook != nil {
		hook(session.CardKey)
	}
	_, err = a.loginOnce(client, session)
	if err != nil {
		a.versionUseMu.RUnlock()
		// A manual login can commit after the initial duplicate-operation check.
		// In that ordering restore lost the race benignly; do not publish this
		// expected state transition as a tunnel error to the UI.
		if errors.Is(err, errSessionAlreadyAuthenticated) {
			return
		}
		a.emitStatus("error", 0, err.Error(), false, false)
		return
	}
	a.versionUseMu.RUnlock()
	// 与手动登录相同：只有 Wails 运行时已启动才开探测循环。
	// 测试直接调用 restoreCachedSession() 时没有 ctx，不应泄漏后台 goroutine。
	a.mu.Lock()
	hasRuntime := a.ctx != nil
	a.mu.Unlock()
	if hasRuntime {
		a.startPortProbe()
	}
	_ = a.startTunnel()
	a.emitSessionRestored()
}

// emitSessionRestored 把"登录态已恢复"推给界面。
//
// 为什么必须有这个事件：restoreCachedSession 是在后台 goroutine 里跑的
// （startup 用 go 调用），而前端只在挂载时取一次 GetInitState。
// 自动登录在取完之后才成功的话，前端永远停在登录页 —— 用户以为没记住卡密，
// 于是重新输一遍，而那次登录必然撞上 409（租约已被自动登录占住），
// 表现就是"每次打开都要重打卡密，还进不去主界面"。
func (a *App) emitSessionRestored() {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		return
	}
	runtime.EventsEmit(ctx, "session:restored", a.GetInitState())
}

// SavedCardKey 返回上次保存的卡密，供登录页预填。
//
// 自动登录失败时（网络不通、卡密被占用）界面会回到登录页，
// 此时把卡密填好，用户只需要点一下"登录"，而不是重新手打一遍。
// 读取走既有的凭据存储（Windows 上是 DPAPI 加密）。
func (a *App) SavedCardKey() string {
	a.mu.Lock()
	store := a.deps.store
	a.mu.Unlock()
	if store == nil {
		return ""
	}
	session, err := store.Load()
	if err != nil {
		return ""
	}
	return session.CardKey
}

func (a *App) GetInitState() InitStateDTO {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := InitStateDTO{TunnelStatus: a.lastStatus}
	if a.authData == nil {
		return state
	}
	state.Authenticated = true
	state.AppName = a.authData.AppName
	state.ExpireAtStr = a.authData.ExpireAtStr
	state.ExpireAtUnix = a.authData.ExpireAtUnix
	state.ExpiresIn = maxZero(a.authData.ExpireAtUnix - time.Now().Unix())
	return state
}

// loginOnce coalesces restoration/manual login and owns the single session commit.
// The in-flight marker remains present until authData and the credential store have
// been committed, so a late caller cannot create a second server lease in the gap.
func (a *App) loginOnce(client *auth.Client, session config.Session) (auth.AuthData, error) {
	cardKey := session.CardKey
	a.loginMu.Lock()
	if a.logoutInProgress {
		a.loginMu.Unlock()
		return auth.AuthData{}, errors.New("正在退出登录，请稍后重试")
	}
	if attempt := a.loginInFlight; attempt != nil {
		if attempt.cardKey != cardKey {
			a.loginMu.Unlock()
			return auth.AuthData{}, errors.New("正在恢复其他会话，请稍后重试")
		}
		attempt.waiters++
		done := attempt.done
		a.loginMu.Unlock()
		<-done
		return attempt.data, attempt.err
	}
	// Atomically couple the no-active-session check with reserving this attempt.
	// This closes the gap where a caller passed its outer check, paused, and then
	// arrived after another caller had committed and cleared loginInFlight.
	a.mu.Lock()
	hasSession := a.authData != nil
	a.mu.Unlock()
	if hasSession {
		a.loginMu.Unlock()
		return auth.AuthData{}, errSessionAlreadyAuthenticated
	}
	attempt := &loginAttempt{cardKey: cardKey, generation: a.loginGeneration, done: make(chan struct{})}
	a.loginInFlight = attempt
	a.loginMu.Unlock()

	data, err := client.Login(context.Background(), cardKey)
	if hook := a.loginBeforeCommit; hook != nil {
		hook()
	}

	a.loginMu.Lock()
	if err == nil && (attempt.generation != a.loginGeneration || a.logoutInProgress) {
		err = errors.New("登录已取消，请重新操作")
	}
	if err == nil && a.versionBlocked() {
		err = errors.New("此版本已停止使用，请更新后继续")
	}
	if err == nil {
		a.mu.Lock()
		store := a.deps.store
		radarPassword := session.RadarPassword
		if radarPassword == "" {
			radarPassword = a.radarPassword
		}
		a.mu.Unlock()
		if store == nil {
			err = errors.New("凭据存储不可用")
		} else {
			err = store.Save(config.Session{CardKey: cardKey, SavedAtUnix: time.Now().Unix(), RadarPassword: radarPassword})
		}
		if err == nil {
			a.mu.Lock()
			a.cardKey = cardKey
			a.radarPassword = radarPassword
			a.authData = &data
			a.lastStatus = "idle"
			a.mu.Unlock()
			a.lease.markHealthy(time.Now())
		}
	}
	attempt.data, attempt.err = data, err
	a.loginInFlight = nil
	close(attempt.done)
	a.loginMu.Unlock()

	// A successful server response rejected locally (for example because Logout
	// won the race) still created a remote lease; release this exact response.
	if err != nil && data.Session.LeaseID != "" {
		_ = client.Logout(context.Background(), data.Session.LeaseID)
	}
	return data, err
}

func (a *App) LoginByCard(cardKey string) (AuthResultDTO, error) {
	a.mu.Lock()
	if a.authData != nil {
		a.mu.Unlock()
		return AuthResultDTO{}, errSessionAlreadyAuthenticated
	}
	a.mu.Unlock()
	a.versionUseMu.RLock()
	defer a.versionUseMu.RUnlock()
	if a.versionBlocked() {
		return AuthResultDTO{}, errors.New("此版本已停止使用，请更新后继续")
	}
	if strings.TrimSpace(cardKey) == "" {
		return AuthResultDTO{}, errors.New("请输入卡密")
	}
	a.mu.Lock()
	client, store, initErr := a.deps.client, a.deps.store, a.initErr
	a.mu.Unlock()
	if initErr != nil {
		return AuthResultDTO{}, initErr
	}
	if client == nil || store == nil {
		return AuthResultDTO{}, errors.New("应用依赖未配置")
	}
	a.mu.Lock()
	radarPassword := a.radarPassword
	a.mu.Unlock()
	if radarPassword == "" {
		if saved, loadErr := store.Load(); loadErr == nil && saved.CardKey == cardKey {
			radarPassword = saved.RadarPassword
		}
	}
	if hook := a.loginBeforeJoin; hook != nil {
		hook(cardKey)
	}
	data, err := a.loginOnce(client, config.Session{CardKey: cardKey, RadarPassword: radarPassword})
	if err != nil {
		var conflictErr *auth.ConflictError
		if errors.As(err, &conflictErr) {
			a.emitStatus("conflict", 0, err.Error(), false, false)
		}
		return AuthResultDTO{}, err
	}

	// Wails 的 startup 在生产中一定先设置 ctx；纯 Go 单测没有 UI 事件通道，
	// 不应因为调用 LoginByCard 就泄漏一个永久后台探测 goroutine。
	a.mu.Lock()
	hasRuntime := a.ctx != nil
	a.mu.Unlock()
	if hasRuntime {
		a.startPortProbe()
	}
	return AuthResultDTO{AppName: data.AppName, ExpireAtStr: data.ExpireAtStr, ExpireAtUnix: data.ExpireAtUnix, TunnelStatus: "idle"}, nil
}

func (a *App) StartTunnel() error { return a.startTunnel() }

func (a *App) startTunnel() error { return a.startTunnelWithLeaseRefresh(true) }

func (a *App) startTunnelWithoutLeaseRefresh() error { return a.startTunnelWithLeaseRefresh(false) }

func (a *App) startTunnelWithLeaseRefresh(refreshLease bool) error {
	a.versionUseMu.RLock()
	defer a.versionUseMu.RUnlock()
	if a.versionBlocked() {
		return errors.New("此版本已停止使用，请更新后继续")
	}
	a.mu.Lock()
	if a.authData == nil {
		a.mu.Unlock()
		return errNotAuthenticated
	}
	if a.engine != nil && a.engine.Running() {
		a.mu.Unlock()
		return frpengine.ErrAlreadyRunning
	}
	data := *a.authData
	ctx := a.ctx
	a.mu.Unlock()

	// 休眠一夜/长时间断网后，内存里的租约可能早已过期。直接拿它启动 FRPC
	// 会得到"start proxy success + 1 秒后心跳 401"的假成功，所以先换新租约。
	if refreshLease {
		if err := a.refreshStaleLeaseBeforeStart(context.Background()); err != nil {
			a.emitStatus("error", 0, err.Error(), false, false)
			a.emitDiagnosis(err.Error())
			return err
		}
	}
	a.mu.Lock()
	if a.authData != nil {
		data = *a.authData
	}
	a.mu.Unlock()

	a.emitStatus("connecting", 0, "", false, false)
	engine, err := frpengine.NewWithExit(data.FRPConfig, a.handleEngineLog, func(err error) { a.handleEngineExit(err) })
	if err != nil {
		a.emitStatus("error", 0, err.Error(), false, false)
		a.emitDiagnosis(err.Error())
		return err
	}
	if err := engine.Start(); err != nil {
		a.emitStatus("error", 0, err.Error(), false, false)
		a.emitDiagnosis(err.Error())
		return err
	}
	client := a.deps.client
	monitorConfig := monitor.Config{
		ServerAddr:        data.FRPConfig.ServerAddr,
		ServerPort:        data.FRPConfig.ServerPort,
		LeaseID:           data.Session.LeaseID,
		HeartbeatInterval: time.Duration(data.Session.HeartbeatInterval) * time.Second,
		LeaseExpireAtUnix: data.Session.LeaseExpireAtUnix,
		ExpireAtUnix:      data.ExpireAtUnix,
		// 包一层：startTunnel 需要知道"租约还剩多久"，而 monitor 只上报成败。
		Heartbeater: &heartbeatTracker{inner: client, onResult: a.handleHeartbeatResult},
		Hooks: monitor.Hooks{
			Emit:       func(event monitor.Event) { a.handleMonitorEvent(event) },
			StopTunnel: func(error) { _ = a.stopEngineFromMonitor() },
			Log:        func(message string) { a.emitLog(message) },
			// 心跳租约失效时静默重登；被顶掉/网络失败由它自己拒绝。
			RecoverLease: a.recoverLease,
			// 网络类心跳失败先退避重试 3 次，别把 Wi-Fi 切换当成掉线。
			RetryableHeartbeat: isRetryableHeartbeat,
			// 心跳带回权威到期时间：管理员改期限后客户端据此跟上。
			UpdateExpiry: a.applyServerExpiry,
			RestartTunnel: func() {
				// 重登后配置变了（换服/换端口），必须重建 FRPC。
				go func() { _ = a.restartTunnelAfterRecovery() }()
			},
		},
	}
	mon, err := monitor.New(monitorConfig)
	if err != nil {
		_ = engine.Stop()
		return err
	}
	if err := mon.Start(); err != nil {
		_ = engine.Stop()
		return err
	}
	a.mu.Lock()
	a.engine, a.monitor = engine, mon
	a.lastStatus = "connecting"
	a.mu.Unlock()
	_ = ctx
	return nil
}

func (a *App) StopTunnel() error { return a.stopTunnel() }

func (a *App) stopTunnel() error {
	a.mu.Lock()
	mon, engine := a.monitor, a.engine
	a.monitor, a.engine = nil, nil
	a.lastStatus = "idle"
	a.mu.Unlock()
	if mon != nil && mon.Running() {
		_ = mon.Stop()
	}
	if engine != nil && engine.Running() {
		return engine.Stop()
	}
	return nil
}

func (a *App) stopEngineFromMonitor() error {
	a.mu.Lock()
	engine := a.engine
	a.engine = nil
	a.mu.Unlock()
	if engine != nil && engine.Running() {
		return engine.Stop()
	}
	return nil
}

func (a *App) handleEngineExit(err error) {
	a.mu.Lock()
	mon := a.monitor
	a.monitor = nil
	a.engine = nil
	a.lastStatus = "error"
	a.mu.Unlock()
	if mon != nil && mon.Running() {
		_ = mon.Stop()
	}
	a.emitStatus("error", 0, err.Error(), false, false)
	a.emitLog("FRP tunnel exited: " + err.Error())
	a.emitDiagnosis(err.Error())
}

func (a *App) CopyShareLink(password string) error {
	a.mu.Lock()
	data := a.authData
	clipboard, ctx := a.deps.clipboard, a.ctx
	a.mu.Unlock()
	if data == nil {
		return errNotAuthenticated
	}
	proxy, ok := findRadarProxy(data.FRPConfig.Proxies)
	if !ok {
		return errRadarProxyMissing
	}
	// 界面没传就退回已保存的密码。
	//
	// 仍然为空也照常生成链接（k= 留空）：雷达密码由用户自己决定填不填，
	// 界面只在不足 8 位时把复制按钮置灰提示，不阻止复制。
	// 之前这里直接报错，等于把"提示"做成了"拦截"。
	if strings.TrimSpace(password) == "" {
		a.mu.Lock()
		password = a.radarPassword
		a.mu.Unlock()
	}
	// 域名用固定的公网地址，不使用服务端下发的 shareBaseUrl。
	link, err := buildShareLink(radarBaseURL(), data.FRPConfig.ServerAddr, proxy.RemotePort, password)
	if err != nil {
		return err
	}
	if clipboard == nil {
		return errors.New("剪贴板不可用")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return clipboard(ctx, link)
}

// RemeasurePing 供界面点击延迟数字时重新测速。
//
// 隧道没在跑就没有 monitor，此时明确报错而不是静默无反应 ——
// 否则用户点了没变化会以为界面坏了。
func (a *App) RemeasurePing() error {
	a.mu.Lock()
	mon, ctx := a.monitor, a.ctx
	a.mu.Unlock()
	if mon == nil {
		return errors.New("隧道未运行，无法测速")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// 异步执行：测速要连 10 次，同步等待会卡住界面线程。
	// 结果通过 tunnel:status 事件推给界面；已有测速在进行时这里会
	// 同步返回 ErrPingInProgress（"测速进行中"），不会堆积协程。
	return mon.RemeasurePingAsync(ctx)
}

func (a *App) Logout() error {
	// Invalidate any in-flight login before stopping the monitor. Do not hold
	// loginMu across stopTunnel: monitor recovery can itself be waiting on login.
	a.loginMu.Lock()
	a.loginGeneration++
	a.logoutInProgress = true
	a.loginMu.Unlock()
	defer func() {
		a.loginMu.Lock()
		a.logoutInProgress = false
		a.loginMu.Unlock()
	}()
	_ = a.stopTunnel()
	a.stopPortProbe()
	a.mu.Lock()
	client, store, data := a.deps.client, a.deps.store, a.authData
	a.authData, a.cardKey, a.lastStatus = nil, "", "idle"
	a.mu.Unlock()
	var firstErr error
	if data != nil && client != nil {
		if err := client.Logout(context.Background(), data.Session.LeaseID); err != nil {
			firstErr = err
		}
	}
	if store != nil {
		if err := store.Clear(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (a *App) emitStatus(status string, pingMS int64, message string, done, timeout bool) {
	a.mu.Lock()
	a.lastStatus = status
	ctx := a.ctx
	a.mu.Unlock()
	if ctx != nil {
		runtime.EventsEmit(ctx, "tunnel:status", TunnelStatusDTO{Status: status, PingMS: pingMS, Message: message, Done: done, Timeout: timeout})
	}
}

func (a *App) handleMonitorEvent(event monitor.Event) {
	// 端口状态不再由 monitor 上报：它有自己的独立探测循环（app_port.go），
	// 隧道停着的时候也要继续刷新。
	a.emitStatus(string(event.Status), event.PingMS, event.Message, event.Done, event.Timeout)
}

// emitDiagnosis 把原始错误翻译成结构化提示推给界面。
//
// 分类在 Go 侧做（有测试覆盖），前端只负责渲染：前端改文案不该影响判定。
func (a *App) emitDiagnosis(message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	result := diagnosis.Diagnose(message)
	a.mu.Lock()
	ctx := a.ctx
	emitter := a.diagnosisEmitter
	a.mu.Unlock()
	if emitter != nil {
		emitter(result.Hint)
		return
	}
	if ctx == nil {
		return
	}
	runtime.EventsEmit(ctx, "tunnel:diagnosis", DiagnosisDTO{
		Level: result.Level,
		Title: result.Title,
		Hint:  result.Hint,
		Raw:   result.Raw,
	})
}

// handleEngineLog 转发 FRPC 日志；命中已知失败特征时额外给一条结构化提示。
//
// 端口占用这类问题 FRPC 只写日志、不退出，所以必须在日志流里识别，
// 否则用户只能自己从英文日志里找原因。
func (a *App) handleEngineLog(line string) {
	// 先剥掉 FRPC 自带的日期时间与 run id，界面只保留自己的 HH:MM:SS；
	// 命中已知的英文技术错误时换成中文业务文案。
	clean := sanitizeEngineLog(line)
	if clean == "" {
		// 纯技术噪音（loginFailExit 之类），不显示给用户。
		return
	}
	a.emitLog(clean)
	if diagnosis.IsRecognized(line) {
		a.emitDiagnosis(line)
	}
}

func (a *App) emitLog(message string) {
	a.mu.Lock()
	ctx := a.ctx
	emitter := a.logEmitter
	a.mu.Unlock()
	event := LogEventDTO{Message: message, At: logClock(time.Now())}
	if emitter != nil {
		emitter(event)
		return
	}
	if ctx != nil {
		runtime.EventsEmit(ctx, "log:append", event)
	}
}

// findRadarProxy 选出用于雷达的 TCP 代理。
//
// 两种命名都要认：
//   - radar_tcp：本地 mock（cmd/mockserver）用的名字
//   - card_<card_id>_tcp：RentForge 真实下发的名字
//
// 只认 radar_tcp 会让真实卡密永远报"未配置 radar_tcp 代理"——这是
// 线上一直复制不出链接的根因。同时仍然拒绝无关代理，避免复制出错误端口。
func findRadarProxy(proxies []auth.Proxy) (auth.Proxy, bool) {
	for _, proxy := range proxies {
		if proxy.Name == radarProxyName && proxy.Type == "tcp" {
			return proxy, true
		}
	}
	for _, proxy := range proxies {
		if proxy.Type == "tcp" && isCardProxyName(proxy.Name) {
			return proxy, true
		}
	}
	return auth.Proxy{}, false
}

// isCardProxyName 判断是否是 RentForge 的卡密代理名：card_<id>_tcp。
func isCardProxyName(name string) bool {
	return strings.HasPrefix(name, "card_") && strings.HasSuffix(name, "_tcp")
}

func buildShareLink(baseURL, serverAddr string, remotePort int, password string) (string, error) {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(serverAddr) == "" {
		return "", errors.New("分享链接配置不完整")
	}
	if remotePort < 1 || remotePort > 65535 {
		return "", errors.New("雷达远程端口无效")
	}
	base := strings.TrimRight(baseURL, "?")
	separator := "?"
	if strings.Contains(base, "?") {
		separator = "&"
	}
	return fmt.Sprintf("%s%sa=%s&k=%s", base, separator, url.QueryEscape(serverAddr+":"+strconv.Itoa(remotePort)), url.QueryEscape(password)), nil
}

// radarBaseURL 返回分享链接使用的域名。
//
// 固定为公网雷达域名；只有显式设置环境变量、且值不是回环地址时才允许覆盖
// （供本地 mock 自测）。服务端下发的 shareBaseUrl 一律不采用。
func radarBaseURL() string {
	candidate := strings.TrimSpace(os.Getenv(radarBaseURLEnv))
	if candidate == "" || isLoopbackURL(candidate) {
		return defaultRadarBaseURL
	}
	if !strings.HasPrefix(candidate, "http://") && !strings.HasPrefix(candidate, "https://") {
		return defaultRadarBaseURL
	}
	return candidate
}

// isLoopbackURL 判断是否是本机/回环地址，这类地址不能作为对外分享域名。
func isLoopbackURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return true
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "", "127.0.0.1", "localhost", "::1", "0.0.0.0":
		return true
	}
	return false
}

func maxZero(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
