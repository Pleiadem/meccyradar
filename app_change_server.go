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
	a.mu.Lock()
	client, data, cardKey := a.deps.client, a.authData, a.cardKey
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

	refreshed, err := client.ChangeServer(context.Background(), cardKey, data.Session.LeaseID)
	if err != nil {
		// 保留原配置。原来在跑就重新连回去，别让用户白断一次。
		if wasRunning {
			if restartErr := a.startTunnel(); restartErr != nil {
				a.emitLog("换服失败，且恢复原连接也失败：" + restartErr.Error())
			}
		}
		return AuthResultDTO{}, err
	}

	// adoptRefreshedAuth 会把新 leaseId、新 frpConfig、新到期时间写回内存与磁盘，
	// 同时刷新 leaseGuard —— 否则下一次心跳还会拿旧租约。
	a.adoptRefreshedAuth(refreshed, data)
	a.emitSessionRestored()
	a.emitLog("已更换服务器，正在重新连接")

	if err := a.startTunnel(); err != nil {
		// 换服本身成功了，只是新连接没起来。如实上报，不谎报成功。
		return AuthResultDTO{
			AppName:      refreshed.AppName,
			ExpireAtStr:  refreshed.ExpireAtStr,
			ExpireAtUnix: refreshed.ExpireAtUnix,
			TunnelStatus: "error",
		}, err
	}

	return AuthResultDTO{
		AppName:      refreshed.AppName,
		ExpireAtStr:  refreshed.ExpireAtStr,
		ExpireAtUnix: refreshed.ExpireAtUnix,
		TunnelStatus: "connecting",
	}, nil
}

// 编译期确认：换服解析复用的是登录那套结构。
var _ = (*auth.AuthData)(nil)
