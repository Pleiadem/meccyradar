//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// Windows 开机自启：在启动目录写一个 .lnk。
//
// 用 PowerShell 的 WScript.Shell COM 对象创建快捷方式：
//   - 不引入新依赖（项目里没有 go-ole，为一个快捷方式加依赖不值）
//   - 比手写 .lnk 二进制格式可靠得多
//
// 必须隐藏窗口：开机时闪一个黑框，用户会以为是病毒。
// 双保险：-WindowStyle Hidden 参数 + SysProcAttr.HideWindow + CREATE_NO_WINDOW。

const createNoWindow = 0x08000000

type windowsAutostart struct {
	shortcutPath string
}

func newPlatformAutostart() (autostartManager, error) {
	appData := strings.TrimSpace(os.Getenv("APPDATA"))
	if appData == "" {
		return nil, fmt.Errorf("无法定位 AppData 目录")
	}
	return &windowsAutostart{
		shortcutPath: filepath.Join(
			appData,
			"Microsoft", "Windows", "Start Menu", "Programs", "Startup",
			autostartShortcutName,
		),
	}, nil
}

func (w *windowsAutostart) ShortcutExists() (bool, error) {
	_, err := os.Stat(w.shortcutPath)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (w *windowsAutostart) ShortcutTarget() (string, error) {
	script := fmt.Sprintf(
		`$s = (New-Object -ComObject WScript.Shell).CreateShortcut('%s'); Write-Output $s.TargetPath`,
		psSingleQuote(w.shortcutPath),
	)
	out, err := runPowerShellHidden(script)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (w *windowsAutostart) CreateShortcut(exePath string) error {
	script := fmt.Sprintf(
		`$s = (New-Object -ComObject WScript.Shell).CreateShortcut('%s'); `+
			`$s.TargetPath='%s'; $s.Arguments='%s'; $s.WorkingDirectory='%s'; $s.Save()`,
		psSingleQuote(w.shortcutPath),
		psSingleQuote(exePath),
		psSingleQuote(autostartFlag),
		psSingleQuote(filepath.Dir(exePath)),
	)
	_, err := runPowerShellHidden(script)
	return err
}

func (w *windowsAutostart) RemoveShortcut() error {
	if err := os.Remove(w.shortcutPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// psSingleQuote 把值安全地塞进 PowerShell 单引号字符串。
// PowerShell 单引号串里的单引号要写两遍。
func psSingleQuote(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

// runPowerShellHidden 执行一段 PowerShell，全程不显示窗口。
func runPowerShellHidden(script string) (string, error) {
	cmd := exec.Command(
		"powershell.exe",
		"-NoProfile",
		"-NonInteractive",
		"-WindowStyle", "Hidden",
		"-Command", script,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			return "", fmt.Errorf("系统调用失败")
		}
		// 只回传最后一行：PowerShell 的错误前缀带脚本内容，对用户没意义。
		lines := strings.Split(message, "\n")
		return "", fmt.Errorf("%s", strings.TrimSpace(lines[len(lines)-1]))
	}
	return string(out), nil
}
