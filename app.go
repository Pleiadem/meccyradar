package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"meccyradar/internal/auth"
	"meccyradar/internal/config"
	"meccyradar/internal/frpengine"
	"meccyradar/internal/monitor"
)

const (
	managementURL        = "MECCYRADAR_MANAGEMENT_URL"
	// 指向真实 RentForge。mock 服务器（cmd/mockserver）只用于自测，
	// 需要时用 MECCYRADAR_MANAGEMENT_URL 显式覆盖，绝不能做默认值：
	// 连到 mock 会把所有真实卡密都判成无效，报错还长得像卡密本身有问题。
	defaultManagementURL = "http://127.0.0.1:8766"
	radarProxyName       = "radar_tcp"
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
}

type TunnelStatusDTO struct {
	Status    string `json:"status"`
	PingMS    int64  `json:"pingMs,omitempty"`
	ExpiresIn int64  `json:"expiresIn,omitempty"`
	Message   string `json:"message,omitempty"`
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

	mu         sync.Mutex
	deps       appDependencies
	authData   *auth.AuthData
	cardKey    string
	engine     *frpengine.Engine
	monitor    *monitor.Monitor
	lastStatus string
	initErr    error
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
	go a.restoreCachedSession()
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
		a.emitStatus("error", 0, err.Error())
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
			a.emitStatus("conflict", 0, err.Error())
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

	a.emitStatus("connecting", 0, "")
	engine, err := frpengine.NewWithExit(data.FRPConfig, func(line string) { a.emitLog(line) }, func(err error) { a.handleEngineExit(err) })
	if err != nil {
		a.emitStatus("error", 0, err.Error())
		return err
	}
	if err := engine.Start(); err != nil {
		a.emitStatus("error", 0, err.Error())
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
	a.emitStatus("error", 0, err.Error())
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
	link, err := buildShareLink(data.ShareBaseURL, data.FRPConfig.ServerAddr, proxy.RemotePort, password)
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

func (a *App) emitStatus(status string, pingMS int64, message string) {
	a.mu.Lock()
	a.lastStatus = status
	ctx := a.ctx
	a.mu.Unlock()
	if ctx != nil {
		runtime.EventsEmit(ctx, "tunnel:status", TunnelStatusDTO{Status: status, PingMS: pingMS, Message: message})
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
	a.emitStatus(string(event.Status), event.PingMS, event.Message)
}

func (a *App) emitLog(message string) {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx != nil {
		runtime.EventsEmit(ctx, "log:append", LogEventDTO{Message: message, At: time.Now().Format(time.RFC3339)})
	}
}

func findRadarProxy(proxies []auth.Proxy) (auth.Proxy, bool) {
	for _, proxy := range proxies {
		if proxy.Name == radarProxyName {
			return proxy, true
		}
	}
	return auth.Proxy{}, false
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

func maxZero(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
