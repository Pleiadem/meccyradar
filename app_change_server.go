package main

import (
	"context"
	"errors"
	"strings"

	"meccyradar/internal/auth"
)

// 客户端自助换服。
//
// 流程：停隧道 → 调服务端 → 用返回的 envelope 替换 authData（含新租约）
// → 自动重新连接。
//
// 为什么要先停隧道：换服会释放旧租约、签发新租约，旧 FRPC 连接指向的是
// 旧服务器的 user/token，继续跑只会一直报鉴权失败。先停干净再换。
//
// 失败时必须保留原配置并尽量恢复原来的连接状态 —— 用户点一次"换服务器"
// 不该把正在用的隧道弄断。

// tunnelActive 表示当前是否有隧道在跑。
func (a *App) tunnelActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.engine != nil || a.monitor != nil
}

// ChangeServer 换到另一台服务器，成功后自动重连。
func (a *App) ChangeServer() (AuthResultDTO, error) {
	if a.versionBlocked() {
		return AuthResultDTO{}, errors.New("此版本已停止使用，请更新后继续")
	}
	a.changeMu.Lock()
	defer a.changeMu.Unlock()

	a.mu.Lock()
	client, data, cardKey := a.deps.client, a.authData, a.cardKey
	fence := a.sessionFenceLocked()
	a.mu.Unlock()

	if client == nil || data == nil {
		return AuthResultDTO{}, errNotAuthenticated
	}
	if strings.TrimSpace(cardKey) == "" {
		return AuthResultDTO{}, errors.New("缺少卡密，请重新登录")
	}

	wasRunning := a.tunnelActive()
	if err := a.stopTunnel(); err != nil {
		// 停不干净就不要换：换服会作废旧租约，旧隧道还挂着只会变成幽灵连接。
		return AuthResultDTO{}, errors.New("无法停止当前隧道，请稍后重试")
	}

	a.versionUseMu.RLock()
	if a.versionBlocked() {
		a.versionUseMu.RUnlock()
		return AuthResultDTO{}, errors.New("此版本已停止使用，请更新后继续")
	}
	if !a.sessionFenceActive(fence) {
		a.versionUseMu.RUnlock()
		return AuthResultDTO{}, errSessionGenerationChanged
	}
	refreshed, err := client.ChangeServer(context.Background(), cardKey, data.Session.LeaseID)
	changeAttempted := true
	// 只有服务端明确确认旧租约超时或因迁移失效，才自动登录并重试一次。
	// 不凭本地 TTL 猜测，避免被其他设备顶掉时自动把对方再顶下线。
	var statusErr *auth.StatusError
	if errors.As(err, &statusErr) && statusErr.Status == 401 &&
		(statusErr.Reason == auth.LeaseReasonTTL || statusErr.Reason == auth.LeaseReasonMigrate) {
		if !a.sessionFenceActive(fence) {
			err = errSessionGenerationChanged
		} else {
			a.emitLog("登录状态已超时，正在恢复后更换服务器")
			var renewed auth.AuthData
			changeAttempted = false
			renewed, err = client.Login(context.Background(), cardKey)
			if err == nil {
				if _, adoptErr := a.adoptRefreshedAuth(renewed, data, fence); adoptErr != nil {
					a.versionUseMu.RUnlock()
					_ = a.logoutIfSessionCurrent(fence)
					return AuthResultDTO{}, adoptErr
				}
				fence = a.currentSessionFence()
				a.emitSessionRestored()
				data = &renewed
				changeAttempted = true
				if !a.sessionFenceActive(fence) {
					err = errSessionGenerationChanged
				} else {
					refreshed, err = client.ChangeServer(context.Background(), cardKey, data.Session.LeaseID)
				}
			}
		}
	}
	a.versionUseMu.RUnlock()
	if a.versionBlocked() {
		return AuthResultDTO{}, errors.New("此版本已停止使用，请更新后继续")
	}
	if err != nil {
		if errors.Is(err, errSessionGenerationChanged) || !a.sessionFenceActive(fence) {
			return AuthResultDTO{}, errSessionGenerationChanged
		}
		if errors.Is(err, errRefreshedAuthNotAdopted) {
			_ = a.logoutIfSessionCurrent(fence)
			return AuthResultDTO{}, err
		}
		// 请求结果未知时，先用一次普通登录向服务端核对当前权威配置。
		// 这覆盖“服务端已提交但 HTTP 响应丢失”的窗口，避免盲目重启旧 token。
		if wasRunning {
			if changeAttempted {
				if reconciled, reconcileErr := a.reconcileChangeFailure(client, cardKey, data, fence, err); reconcileErr == nil {
					return reconciled, nil
				} else if errors.Is(reconcileErr, errRefreshedAuthNotAdopted) {
					return AuthResultDTO{}, reconcileErr
				} else if !a.sessionFenceActive(fence) {
					return AuthResultDTO{}, errSessionGenerationChanged
				}
			}
			if !a.sessionFenceActive(fence) {
				return AuthResultDTO{}, errSessionGenerationChanged
			}
			if restartErr := a.startTunnelForSession(fence, false); restartErr != nil {
				a.emitLog("换服失败，且恢复原连接也失败：" + restartErr.Error())
			}
		}
		return AuthResultDTO{}, err
	}

	// 服务端已经签发新租约；本地未能安全采用时释放该租约、清除旧登录态，
	// 不能让旧隧道或旧 lease 继续运行，也不能向前端返回换服成功。
	if _, adoptErr := a.adoptRefreshedAuth(refreshed, data, fence); adoptErr != nil {
		_ = a.logoutIfSessionCurrent(fence)
		return AuthResultDTO{}, adoptErr
	}
	a.emitSessionRestored()
	a.emitLog("已更换服务器，正在重新连接")

	if err := a.startTunnelForSession(fence, true); err != nil {
		// 换服本身成功了，只是新连接没起来。如实上报，不谎报成功。
		result := authResultDTO(refreshed, "error")
		result.Warning = refreshed.Warning
		return result, err
	}

	result := authResultDTO(refreshed, "connecting")
	result.Warning = refreshed.Warning
	return result, nil
}

func (a *App) reconcileChangeFailure(client *auth.Client, cardKey string, previous *auth.AuthData, fence sessionFence, cause error) (AuthResultDTO, error) {
	// 只有传输层错误代表“请求结果未知”；业务 4xx/5xx 已经是服务端明确答复，
	// 不要再额外登录改变服务端状态。
	var transportErr *auth.TransportError
	if !errors.As(cause, &transportErr) {
		return AuthResultDTO{}, errors.New("换服结果已明确失败")
	}
	a.versionUseMu.RLock()
	if a.versionBlocked() {
		a.versionUseMu.RUnlock()
		return AuthResultDTO{}, errors.New("此版本已停止使用，请更新后继续")
	}
	if !a.sessionFenceActive(fence) {
		a.versionUseMu.RUnlock()
		return AuthResultDTO{}, errSessionGenerationChanged
	}
	refreshed, err := client.Login(context.Background(), cardKey)
	a.versionUseMu.RUnlock()
	if a.versionBlocked() {
		return AuthResultDTO{}, errors.New("此版本已停止使用，请更新后继续")
	}
	if err != nil {
		return AuthResultDTO{}, err
	}
	if _, adoptErr := a.adoptRefreshedAuth(refreshed, previous, fence); adoptErr != nil {
		_ = a.logoutIfSessionCurrent(fence)
		return AuthResultDTO{}, adoptErr
	}
	if err := a.startTunnelForSession(fence, false); err != nil {
		return AuthResultDTO{}, err
	}
	result := authResultDTO(refreshed, "connecting")
	result.Warning = refreshed.Warning
	return result, nil
}

// 编译期确认：换服解析复用的是登录那套结构。
var _ = (*auth.AuthData)(nil)
