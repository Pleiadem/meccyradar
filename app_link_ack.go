package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"meccyradar/internal/auth"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type linkAckTask struct {
	generation uint64
	cardKey    string
	leaseID    string
	version    int
}

type LinkAckEventDTO struct {
	Accepted    bool   `json:"accepted"`
	LeaseID     string `json:"leaseId"`
	LinkVersion int    `json:"linkVersion"`
}

// CopyLinkForDelivery validates the exact configuration version, builds the link,
// then initiates one clipboard operation. It never logs or emits the link itself.
func (a *App) CopyLinkForDelivery(password string, version int) (ShareLinkAttemptDTO, error) {
	a.mu.Lock()
	data := a.authData
	clipboard, ctx := a.deps.clipboard, a.ctx
	storedPassword := a.radarPassword
	a.mu.Unlock()
	if data == nil || data.Session.LeaseID == "" {
		return ShareLinkAttemptDTO{}, errNotAuthenticated
	}
	if version <= 0 || data.Server.LinkVersion != version {
		return ShareLinkAttemptDTO{}, errors.New("链接配置版本已变化，请重新登录后再复制")
	}
	if password == "" {
		password = storedPassword
	}
	proxy, ok := findRadarProxy(data.FRPConfig.Proxies)
	if !ok {
		return ShareLinkAttemptDTO{}, errRadarProxyMissing
	}
	link, err := buildShareLink(radarBaseURL(), data.FRPConfig.ServerAddr, proxy.RemotePort, password)
	if err != nil {
		return ShareLinkAttemptDTO{}, err
	}
	if clipboard == nil {
		return ShareLinkAttemptDTO{}, errors.New("剪贴板不可用；尚未发起复制尝试")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	copyErr := clipboard(ctx, link)
	result := ShareLinkAttemptDTO{Attempted: true, Copied: copyErr == nil, Link: link}
	if copyErr != nil {
		safeDetail := strings.TrimSpace(strings.ReplaceAll(copyErr.Error(), link, "[链接已脱敏]"))
		if password != "" {
			safeDetail = strings.ReplaceAll(safeDetail, password, "[密码已脱敏]")
		}
		if len(safeDetail) > 160 {
			safeDetail = safeDetail[:160]
		}
		if safeDetail == "" {
			safeDetail = "系统未提供可安全展示的错误详情"
		}
		result.Error = "剪贴板写入失败：" + safeDetail + "；你可以手动选择下方链接复制"
	}
	if result.Copied {
		a.emitLog("已成功复制链接")
	} else {
		a.emitLog("复制链接失败，请手动复制")
	}
	a.queueLinkAck(version, data.Session.LeaseID)
	return result, nil
}

func (a *App) queueLinkAck(version int, leaseID string) {
	a.mu.Lock()
	if a.authData == nil || a.authData.Server.LinkVersion != version || a.authData.Session.LeaseID != leaseID || a.cardKey == "" {
		a.mu.Unlock()
		return
	}
	task := linkAckTask{generation: a.sessionGeneration, cardKey: a.cardKey, leaseID: leaseID, version: version}
	a.setPendingLinkAckLocked(task)
	store := a.deps.store
	a.mu.Unlock()
	if store != nil {
		if err := a.saveSessionForCurrent(task.generation, task.cardKey); err != nil && !errors.Is(err, errSessionGenerationChanged) {
			a.emitLog("链接复制尝试确认暂未保存，将在本次运行期间重试")
		}
	}
	a.startLinkAckRetry(task)
}

func (a *App) resumePendingLinkAck(data auth.AuthData) {
	a.mu.Lock()
	if a.pendingLinkAckVersion <= 0 || a.authData == nil ||
		a.authData.Session.LeaseID != data.Session.LeaseID || a.cardKey == "" {
		a.mu.Unlock()
		return
	}
	if data.Server.LinkVersion <= 0 || a.pendingLinkAckVersion != data.Server.LinkVersion {
		task := linkAckTask{generation: a.sessionGeneration, cardKey: a.cardKey}
		a.clearPendingLinkAckLocked()
		a.mu.Unlock()
		_ = a.saveSessionForCurrent(task.generation, task.cardKey)
		return
	}
	task := linkAckTask{generation: a.sessionGeneration, cardKey: a.cardKey, leaseID: data.Session.LeaseID, version: a.pendingLinkAckVersion}
	a.setPendingLinkAckLocked(task)
	a.mu.Unlock()
	_ = a.saveSessionForCurrent(task.generation, task.cardKey)
	a.startLinkAckRetry(task)
}

func (a *App) setPendingLinkAckLocked(task linkAckTask) {
	a.pendingLinkAckInitialized = true
	a.pendingLinkAckVersion = task.version
	a.pendingLinkAckLeaseID = task.leaseID
	a.pendingLinkAckCardKey = task.cardKey
	a.pendingLinkAckGeneration = task.generation
}

func (a *App) clearPendingLinkAckLocked() {
	a.pendingLinkAckInitialized = true
	a.pendingLinkAckVersion = 0
	a.pendingLinkAckLeaseID = ""
	a.pendingLinkAckCardKey = ""
	a.pendingLinkAckGeneration = 0
}

func (a *App) pendingLinkAckLocked() linkAckTask {
	return linkAckTask{
		generation: a.pendingLinkAckGeneration,
		cardKey:    a.pendingLinkAckCardKey,
		leaseID:    a.pendingLinkAckLeaseID,
		version:    a.pendingLinkAckVersion,
	}
}

func sameLinkAckTask(left, right linkAckTask) bool {
	return left.generation == right.generation && left.cardKey == right.cardKey &&
		left.leaseID == right.leaseID && left.version == right.version
}

func (a *App) startLinkAckRetry(task linkAckTask) {
	a.linkAckMu.Lock()
	if a.linkAckRunning {
		a.linkAckMu.Unlock()
		return
	}
	a.linkAckRunning = true
	a.linkAckTask = task
	a.linkAckMu.Unlock()
	go func() {
		defer func() {
			a.linkAckMu.Lock()
			a.linkAckRunning = false
			a.linkAckTask = linkAckTask{}
			a.linkAckMu.Unlock()
			a.mu.Lock()
			next := a.pendingLinkAckLocked()
			a.mu.Unlock()
			if next.version > 0 && !sameLinkAckTask(next, task) {
				a.startLinkAckRetry(next)
			}
		}()
		a.retryLinkAck(task)
	}()
}

func (a *App) retryLinkAck(task linkAckTask) {
	backoff := time.Second
	for {
		a.mu.Lock()
		if !sameLinkAckTask(a.pendingLinkAckLocked(), task) || a.sessionGeneration != task.generation ||
			a.cardKey != task.cardKey || a.authData == nil || a.authData.Server.LinkVersion != task.version ||
			a.authData.Session.LeaseID != task.leaseID {
			a.mu.Unlock()
			return
		}
		client := a.deps.client
		store := a.deps.store
		a.mu.Unlock()
		if client == nil || store == nil {
			return
		}
		if err := a.saveSessionForCurrent(task.generation, task.cardKey); err != nil {
			if errors.Is(err, errSessionGenerationChanged) {
				return
			}
			if !a.waitLinkAckRetry(backoff) {
				return
			}
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		accepted, err := client.AcknowledgeLinkAttempt(ctx, task.leaseID, task.version)
		cancel()
		if err == nil {
			a.clearLinkAck(task, accepted)
			return
		}
		if !a.waitLinkAckRetry(backoff) {
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (a *App) waitLinkAckRetry(backoff time.Duration) bool {
	timer := time.NewTimer(backoff)
	select {
	case <-timer.C:
		return true
	case <-a.ctxDone():
		timer.Stop()
		return false
	}
}

func (a *App) ctxDone() <-chan struct{} {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		return make(chan struct{})
	}
	return ctx.Done()
}

func (a *App) clearLinkAck(task linkAckTask, accepted bool) {
	a.mu.Lock()
	if !sameLinkAckTask(a.pendingLinkAckLocked(), task) || a.sessionGeneration != task.generation ||
		a.cardKey != task.cardKey || a.authData == nil || a.authData.Session.LeaseID != task.leaseID ||
		a.authData.Server.LinkVersion != task.version {
		a.mu.Unlock()
		return
	}
	a.clearPendingLinkAckLocked()
	if accepted {
		a.authData.Server.CopyPending = false
	}
	a.mu.Unlock()
	_ = a.saveSessionForCurrent(task.generation, task.cardKey)
	if accepted {
		a.emitLinkAckEvent(LinkAckEventDTO{Accepted: true, LeaseID: task.leaseID, LinkVersion: task.version})
	}
}

func (a *App) emitLinkAckEvent(event LinkAckEventDTO) {
	a.mu.Lock()
	ctx := a.ctx
	emitter := a.linkAckEmitter
	a.mu.Unlock()
	if emitter != nil {
		emitter(event)
		return
	}
	if ctx != nil {
		runtime.EventsEmit(ctx, "link:acknowledged", event)
	}
}
