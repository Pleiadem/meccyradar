package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"meccyradar/internal/config"
)

// 开机自启。
//
// 实现方式是**启动目录里的 .lnk 快捷方式**，不写注册表、不做 Windows 服务：
//   - 注册表 Run 键在部分精简版/受管环境会被安全软件拦掉
//   - 服务需要管理员权限，而且和"绿色便携版"的定位冲突
//   - 快捷方式用户能自己看到、自己删，出问题好排查
//
// 快捷方式带 `--autostart` 参数，程序据此在启动时隐藏窗口直接连隧道。

const (
	// autostartFlag 是开机自启时传入的参数。
	autostartFlag = "--autostart"
	// autostartShortcutName 是启动目录里的快捷方式文件名。
	autostartShortcutName = "MeccyRadar.lnk"
)

// autostartManager 抽象"系统启动项"的操作。
//
// 抽成接口是为了让 reconcileAutostart 的逻辑能在任何平台上单测：
// 真正的快捷方式读写只有 Windows 能验，但对齐规则（设置 vs 快捷方式）是
// 纯逻辑，不该因为"跑在 Mac 上"就完全没法测。
type autostartManager interface {
	// ShortcutExists 快捷方式是否存在。
	ShortcutExists() (bool, error)
	// ShortcutTarget 快捷方式指向的 exe 路径（不存在时返回空串）。
	ShortcutTarget() (string, error)
	// CreateShortcut 创建/覆盖指向 exePath 的快捷方式。
	CreateShortcut(exePath string) error
	// RemoveShortcut 删除快捷方式（不存在不算错误）。
	RemoveShortcut() error
}

// autostartOutcome 描述一次对齐做了什么，便于测试断言与日志。
type autostartOutcome struct {
	// Created 本次新建了快捷方式。
	Created bool
	// Removed 本次删除了残留快捷方式。
	Removed bool
	// Updated 本次因 exe 路径变化而重写了快捷方式。
	Updated bool
	// Error 非致命错误（记录到日志，不打断启动）。
	Error string
}

// reconcileAutostart 把"用户意图"和"系统实际状态"对齐。
//
// 三种不一致都要处理，缺一种就会留下用户看不懂的状态：
//   - 设置开启、快捷方式缺失  → 重建（用户重装/手动删过）
//   - 设置关闭、快捷方式残留  → 删除（用户关了但上次没删干净）
//   - 设置开启、exe 路径变了  → 更新（绿色版换目录后旧快捷方式指向不存在的文件）
func reconcileAutostart(settings config.Settings, manager autostartManager, exePath string) autostartOutcome {
	var outcome autostartOutcome
	if manager == nil {
		outcome.Error = "启动项管理器不可用"
		return outcome
	}

	exists, err := manager.ShortcutExists()
	if err != nil {
		outcome.Error = err.Error()
		return outcome
	}

	if !settings.AutoStart {
		if !exists {
			return outcome
		}
		if err := manager.RemoveShortcut(); err != nil {
			outcome.Error = err.Error()
			return outcome
		}
		outcome.Removed = true
		return outcome
	}

	// 设置开启
	if !exists {
		if err := manager.CreateShortcut(exePath); err != nil {
			outcome.Error = err.Error()
			return outcome
		}
		outcome.Created = true
		return outcome
	}

	// 已存在：检查指向是否还是当前 exe
	target, err := manager.ShortcutTarget()
	if err != nil {
		outcome.Error = err.Error()
		return outcome
	}
	if samePath(target, exePath) {
		return outcome
	}
	if err := manager.CreateShortcut(exePath); err != nil {
		outcome.Error = err.Error()
		return outcome
	}
	outcome.Updated = true
	return outcome
}

// samePath 比较两个 exe 路径是否指向同一个文件。
// Windows 路径大小写不敏感，且快捷方式里可能带引号。
func samePath(left, right string) bool {
	normalize := func(value string) string {
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"`)
		return strings.ToLower(strings.ReplaceAll(value, "/", `\`))
	}
	if normalize(left) == "" || normalize(right) == "" {
		return false
	}
	return normalize(left) == normalize(right)
}

// currentExecutablePath 返回当前 exe 的绝对路径。
func currentExecutablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", errors.New("无法确定程序路径，开机自启不可用")
	}
	return path, nil
}

// autostartStore 是设置存储的接口，便于测试注入。
type autostartStore interface {
	Load() config.Settings
	Save(config.Settings) error
}

// AutostartEnabled 返回用户是否开启了开机自启。
func (a *App) AutostartEnabled() bool {
	a.mu.Lock()
	store := a.deps.settings
	a.mu.Unlock()
	if store == nil {
		return false
	}
	return store.Load().AutoStart
}

// AutostartSupported 表示当前平台是否支持开机自启。
// 前端据此把开关置灰并给出说明，而不是让用户点了才报错。
func (a *App) AutostartSupported() bool {
	a.mu.Lock()
	manager := a.deps.autostart
	a.mu.Unlock()
	return manager != nil
}

// SetAutostart 打开/关闭开机自启。
//
// 顺序：先落设置，再动快捷方式。反过来的话快捷方式建好了、设置没写进去，
// 下次启动自检会把它当成"残留"删掉，用户会觉得开关不生效。
func (a *App) SetAutostart(enabled bool) error {
	a.mu.Lock()
	store, manager := a.deps.settings, a.deps.autostart
	a.mu.Unlock()
	if store == nil {
		return errors.New("设置存储不可用")
	}
	if manager == nil {
		return errors.New("当前系统不支持开机自启")
	}

	if err := store.Save(config.Settings{AutoStart: enabled}); err != nil {
		return errors.New("保存设置失败，请检查磁盘权限")
	}

	exePath, err := currentExecutablePath()
	if err != nil {
		return err
	}
	outcome := reconcileAutostart(config.Settings{AutoStart: enabled}, manager, exePath)
	if outcome.Error != "" {
		// 设置已保存但系统侧没生效：回滚设置，避免"开关是开的但其实没生效"。
		_ = store.Save(config.Settings{AutoStart: !enabled})
		return fmt.Errorf("设置开机自启失败：%s", outcome.Error)
	}
	return nil
}

// syncAutostart 在启动时对齐一次设置与系统状态。
func (a *App) syncAutostart() {
	a.mu.Lock()
	store, manager := a.deps.settings, a.deps.autostart
	a.mu.Unlock()
	if store == nil || manager == nil {
		return
	}
	exePath, err := currentExecutablePath()
	if err != nil {
		return
	}
	outcome := reconcileAutostart(store.Load(), manager, exePath)
	switch {
	case outcome.Error != "":
		a.logf("开机自启自检失败：%s", outcome.Error)
	case outcome.Created:
		a.emitLog("已重建开机自启快捷方式")
	case outcome.Removed:
		a.emitLog("已清理残留的开机自启快捷方式")
	case outcome.Updated:
		a.emitLog("已更新开机自启快捷方式指向的程序路径")
	}
}
