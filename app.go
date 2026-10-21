package main

import (
	"context"
	"errors"
	"fmt"
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
)

var (
	errNotAuthenticated      = errors.New("尚未登录")
	errRadarProxyMissing     = errors.New("未配置 radar_tcp 代理")
	errRadarPasswordRequired = errors.New("请先填写雷达密码")
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

type LogEventDTO struct {
	Message string `json:"message"`
	At      string `json:"at"`
}

type appDependencies struct {
	store     config.CredentialStore
	client    *auth.Client
	clipboard func(context.Context, string) error
}

type App struct {
	ctx context.Context

	mu       sync.Mutex
	deps     appDependencies
	authData *auth.AuthData
	cardKey  string
	// 雷达密码：来自界面输入，随会话持久化，重启后自动带出。
	radarPassword string
	engine        *frpengine.Engine
	monitor       *monitor.Monitor
	lastStatus    string
	initErr       error
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
	app := newApp(appDependencies{store: store, client: client, clipboard: runtime.ClipboardSetText})
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
	// Windows 需要在启动时就挂上托盘：窗口关闭后是隐藏而非退出，
	// 没有托盘就再也点不回来了。
	a.startTray()
	a.handleProcessSignals()
	go a.restoreCachedSession()
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
	stopErr := a.stopTunnel()

	a.mu.Lock()
	client, data := a.deps.client, a.authData
	a.authData, a.cardKey, a.lastStatus = nil, "", "idle"
	a.mu.Unlock()

	if client != nil && data != nil && data.Session.LeaseID != "" {
		if err := client.Logout(context.Background(), data.Session.LeaseID); err != nil && stopErr == nil {
			return err
		}
	}
	return stopErr
}

// shutdown 在应用真正退出时执行：断开连接、释放租约。
// 关闭窗口只是隐藏（HideWindowOnClose），不会走到这里，隧道保持在线。
func (a *App) shutdown(_ context.Context) {
	_ = a.disconnect()
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
	_ = a.disconnect()
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

func (a *App) restoreCachedSession() {
	a.mu.Lock()
	store, client, initErr := a.deps.store, a.deps.client, a.initErr
	a.mu.Unlock()
	if initErr != nil || store == nil || client == nil {
		return
	}
	session, err := store.Load()
	if err != nil {
		return
	}
	data, err := client.Login(context.Background(), session.CardKey)
	if err != nil {
		a.emitStatus("error", 0, err.Error(), false, false)
		return
	}
	a.mu.Lock()
	a.cardKey = session.CardKey
	a.authData = &data
	a.mu.Unlock()
	_ = a.startTunnel()
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

func (a *App) LoginByCard(cardKey string) (AuthResultDTO, error) {
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
	data, err := client.Login(context.Background(), cardKey)
	if err != nil {
		var conflictErr *auth.ConflictError
		if errors.As(err, &conflictErr) {
			a.emitStatus("conflict", 0, err.Error(), false, false)
		}
		return AuthResultDTO{}, err
	}
	if err := store.Save(config.Session{CardKey: cardKey}); err != nil {
		return AuthResultDTO{}, err
	}
	a.mu.Lock()
	a.cardKey = cardKey
	a.authData = &data
	a.lastStatus = "idle"
	a.mu.Unlock()
	return AuthResultDTO{AppName: data.AppName, ExpireAtStr: data.ExpireAtStr, ExpireAtUnix: data.ExpireAtUnix, TunnelStatus: "idle"}, nil
}

func (a *App) StartTunnel() error { return a.startTunnel() }

func (a *App) startTunnel() error {
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

	a.emitStatus("connecting", 0, "", false, false)
	engine, err := frpengine.NewWithExit(data.FRPConfig, func(line string) { a.emitLog(line) }, func(err error) { a.handleEngineExit(err) })
	if err != nil {
		a.emitStatus("error", 0, err.Error(), false, false)
		return err
	}
	if err := engine.Start(); err != nil {
		a.emitStatus("error", 0, err.Error(), false, false)
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
		Heartbeater:       client,
		Hooks: monitor.Hooks{
			Emit:       func(event monitor.Event) { a.handleMonitorEvent(event) },
			StopTunnel: func(error) { _ = a.stopEngineFromMonitor() },
			Log:        func(message string) { a.emitLog(message) },
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
	// 界面没传就退回已保存的密码；仍然为空则明确报错，
	// 绝不生成 k= 这种缺参数的链接（用户拿到手是打不开的）。
	if strings.TrimSpace(password) == "" {
		a.mu.Lock()
		password = a.radarPassword
		a.mu.Unlock()
	}
	if strings.TrimSpace(password) == "" {
		return errRadarPasswordRequired
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
	_ = a.stopTunnel()
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
	if event.Kind == "port:status" {
		a.mu.Lock()
		ctx := a.ctx
		a.mu.Unlock()
		if ctx != nil {
			runtime.EventsEmit(ctx, "port:status", PortStatusDTO{Open: event.PortOpen})
		}
		return
	}
	a.emitStatus(string(event.Status), event.PingMS, event.Message, event.Done, event.Timeout)
}

func (a *App) emitLog(message string) {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx != nil {
		runtime.EventsEmit(ctx, "log:append", LogEventDTO{Message: message, At: time.Now().Format(time.RFC3339)})
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
