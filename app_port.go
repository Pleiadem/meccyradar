package main

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"meccyradar/internal/auth"
)

// 本地端口探测。
//
// 为什么从 monitor 里搬出来：原来只有隧道在跑时才探端口（monitor 的 3 秒
// ticker），隧道一停就没人探了，界面永远停在停止前的旧状态。而"本地端口
// 有没有在监听"跟隧道跑不跑根本是两件事 —— 用户完全可能在隧道停着的时候
// 去启动或关闭 Fast 雷达。现在只要已登录就一直探，与隧道/monitor 无关。

const (
	// portProbeTimeout 单次探测超时。只判断本地端口能否连上，短超时即可。
	portProbeTimeout = 500 * time.Millisecond
	// defaultLocalPortAddr 取不到雷达代理配置时的兜底地址。
	defaultLocalPortAddr = "127.0.0.1:13133"
)

// defaultPortProbeInterval 是默认探测周期。2 秒足够跟手，连本机回环几乎不耗资源。
const defaultPortProbeInterval = 2 * time.Second

// portProbeCycle 返回当前 App 的探测周期。
func (a *App) portProbeCycle() time.Duration {
	a.mu.Lock()
	interval := a.portProbeInterval
	a.mu.Unlock()
	if interval <= 0 {
		return defaultPortProbeInterval
	}
	return interval
}

// radarLocalAddr 从雷达代理取 localIP:localPort，取不到退回默认值。
//
// 必须用服务端下发的值而不是写死 13133：服务端一旦给别的端口，FRP 会正确
// 代理新端口，而写死的探测会一直报"未监听"，用户会以为坏了。
func radarLocalAddr(data *auth.AuthData) string {
	if data == nil {
		return defaultLocalPortAddr
	}
	proxy, ok := findRadarProxy(data.FRPConfig.Proxies)
	if !ok {
		return defaultLocalPortAddr
	}
	if proxy.LocalPort < 1 || proxy.LocalPort > 65535 {
		return defaultLocalPortAddr
	}
	host := proxy.LocalIP
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(proxy.LocalPort))
}

// portProbeAddress 返回当前该探测的地址。
func (a *App) portProbeAddress() string {
	a.mu.Lock()
	data := a.authData
	a.mu.Unlock()
	return radarLocalAddr(data)
}

// startPortProbe 启动探测循环。已登录时调用，重复调用无副作用。
func (a *App) startPortProbe() {
	a.mu.Lock()
	if a.portProbeCancel != nil {
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	a.portProbeCancel = cancel
	a.portProbeDone = done
	a.mu.Unlock()

	go a.runPortProbe(ctx, done)
}

// stopPortProbe 停止探测循环，并把状态复位成"未监听"。
//
// 复位这一步很关键：退出登录后如果不上报一次 false，界面会一直挂着
// 上一个会话的"已监听"。
func (a *App) stopPortProbe() {
	a.mu.Lock()
	cancel, done := a.portProbeCancel, a.portProbeDone
	a.portProbeCancel, a.portProbeDone = nil, nil
	a.mu.Unlock()

	if cancel != nil {
		cancel()
		<-done
	}
	a.emitPortStatus(false)
}

func (a *App) runPortProbe(ctx context.Context, done chan struct{}) {
	defer close(done)
	// 先立刻探一次：登录后界面马上就有状态，不用等第一个 tick。
	a.probePortOnce(ctx)
	ticker := time.NewTicker(a.portProbeCycle())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.probePortOnce(ctx)
		}
	}
}

// probePortOnce 探一次本地端口并把结果推给界面。
func (a *App) probePortOnce(ctx context.Context) {
	dialer := &net.Dialer{Timeout: portProbeTimeout}
	dialCtx, cancel := context.WithTimeout(ctx, portProbeTimeout)
	conn, err := dialer.DialContext(dialCtx, "tcp", a.portProbeAddress())
	cancel()
	if err == nil {
		_ = conn.Close()
	}
	a.emitPortStatus(err == nil)
}

// emitPortStatus 推送端口状态。
//
// 只在状态变化（或首次上报）时才推：每 2 秒往界面发一条一模一样的消息
// 只会让前端白白重渲染。
func (a *App) emitPortStatus(open bool) {
	a.mu.Lock()
	ctx := a.ctx
	changed := !a.portProbeKnown || a.portProbeOpen != open
	a.portProbeKnown = true
	a.portProbeOpen = open
	a.mu.Unlock()

	if !changed || ctx == nil {
		return
	}
	runtime.EventsEmit(ctx, "port:status", PortStatusDTO{Open: open})
}

// portProbeState 供测试断言：最近一次上报的端口状态。
func (a *App) portProbeState() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.portProbeOpen
}
