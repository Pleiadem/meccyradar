//go:build !windows

package main

// startTray 在非 Windows 平台是空实现。
// macOS 关闭窗口后应用仍在 Dock 里，点 Dock 图标或 Cmd+Tab 即可还原，
// 不需要额外的托盘；这里保持与 Windows 相同的调用点，避免上层分叉。
func (a *App) startTray() {}
