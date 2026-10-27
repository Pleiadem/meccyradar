package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"meccyradar/internal/auth"
	"meccyradar/internal/buildinfo"
)

type versionGateState struct {
	RequiredVersion string `json:"requiredVersion"`
	DownloadURL     string `json:"downloadUrl"`
	FailClosed      bool   `json:"failClosed,omitempty"`
}

type VersionSnapshot struct {
	Info            auth.VersionInfo `json:"info"`
	CurrentVersion  string           `json:"currentVersion"`
	RequiredVersion string           `json:"requiredVersion,omitempty"`
	DownloadURL     string           `json:"downloadUrl,omitempty"`
	Blocked         bool             `json:"blocked"`
	Checking        bool             `json:"checking"`
	CheckError      string           `json:"checkError,omitempty"`
}

type versionFileState struct {
	gate  versionGateState
	found bool
}

type versionFileRecord struct {
	Configured      *bool  `json:"configured"`
	Blocked         bool   `json:"blocked,omitempty"`
	RequiredVersion string `json:"requiredVersion"`
	DownloadURL     string `json:"downloadUrl"`
}

func versionStatePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "MeccyRadar", "version-gate.json"), nil
}

func readVersionStateAt(path string) (versionFileState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return versionFileState{}, nil
	}
	if err != nil {
		return versionFileState{}, fmt.Errorf("读取版本门禁状态失败: %w", err)
	}
	var record versionFileRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil || record.Configured == nil {
		return versionFileState{found: true}, errors.New("本地版本门禁状态损坏")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return versionFileState{found: true}, errors.New("本地版本门禁状态含有无效尾随数据")
	}
	if !*record.Configured && (record.RequiredVersion != "" || record.DownloadURL != "") {
		return versionFileState{found: true}, errors.New("本地版本门禁状态无效")
	}
	state := versionGateState{RequiredVersion: record.RequiredVersion, DownloadURL: record.DownloadURL, FailClosed: record.Blocked}
	if !*record.Configured {
		return versionFileState{gate: state, found: true}, nil
	}
	if !auth.ValidReleaseVersion(state.RequiredVersion) || !auth.ValidDownloadURL(state.DownloadURL) {
		return versionFileState{found: true}, errors.New("本地版本门禁状态无效")
	}
	return versionFileState{gate: state, found: true}, nil
}

func writeVersionStateAt(path string, state versionGateState) error {
	if (state.RequiredVersion == "" && state.DownloadURL != "") || (state.RequiredVersion != "" && (!auth.ValidReleaseVersion(state.RequiredVersion) || !auth.ValidDownloadURL(state.DownloadURL))) {
		return errors.New("拒绝保存无效版本门禁状态")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("创建版本状态目录失败: %w", err)
	}
	configured := state.RequiredVersion != ""
	data, err := json.Marshal(versionFileRecord{Configured: &configured, Blocked: state.FailClosed, RequiredVersion: state.RequiredVersion, DownloadURL: state.DownloadURL})
	if err != nil {
		return fmt.Errorf("编码版本状态失败: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".version-gate-*.tmp")
	if err != nil {
		return fmt.Errorf("创建版本状态临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置版本状态权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入版本状态失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("刷新版本状态失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭版本状态失败: %w", err)
	}
	if err := atomicReplaceFile(tmpName, path); err != nil {
		return fmt.Errorf("原子替换版本状态失败: %w", err)
	}
	return nil
}

func (a *App) versionPath() (string, error) {
	if a.versionStateFile != "" {
		return a.versionStateFile, nil
	}
	return versionStatePath()
}

// loadLocalVersionGate reads only the tiny local state file. Startup calls this
// synchronously before session restoration; it never performs network I/O.
func (a *App) loadLocalVersionGate() {
	path, pathErr := a.versionPath()
	var disk versionFileState
	var err error
	if pathErr != nil {
		err = pathErr
	} else {
		disk, err = readVersionStateAt(path)
	}
	a.versionUseMu.Lock()
	a.mu.Lock()
	if err != nil {
		a.versionGate = true
		a.versionCheckError = "本机版本门禁状态无法读取或已损坏；已阻止登录和连接，请联网重试"
	} else {
		a.versionGateState = disk.gate
		a.applyVersionGateLocked(disk.gate)
		a.versionCheckError = ""
	}
	snapshot, ctx := a.versionSnapshotLocked(), a.ctx
	a.mu.Unlock()
	a.versionUseMu.Unlock()
	if err != nil {
		log.Printf("MeccyRadar local version gate state could not be trusted; access will fail closed")
	}
	a.emitVersionSnapshot(ctx, snapshot)
}

func (a *App) CheckVersion() error {
	a.versionCheckMu.Lock()
	err := a.performVersionCheck()
	a.versionCheckMu.Unlock()
	if a.versionBlocked() {
		if stopErr := a.stopTunnel(); stopErr != nil {
			log.Printf("MeccyRadar mandatory version gate could not stop the active tunnel: %v", stopErr)
		}
	}
	return err
}

func (a *App) restoreAfterVersionCheck(restore func()) {
	// Kept for tests and compatibility: restore immediately under the current
	// locally-known gate rather than waiting for the background network request.
	if a.versionBlocked() {
		return
	}
	if restore != nil {
		restore()
	}
}

func (a *App) performVersionCheck() error {
	a.mu.Lock()
	client, ctx := a.deps.client, a.ctx
	a.versionCheckPending = true
	a.versionCheckError = ""
	a.versionInfo = auth.VersionInfo{}
	a.versionGate = a.versionGate || !auth.ValidReleaseVersion(buildinfo.Version)
	pendingSnapshot := a.versionSnapshotLocked()
	a.mu.Unlock()
	a.emitVersionSnapshot(ctx, pendingSnapshot)

	path, pathErr := a.versionPath()
	var disk versionFileState
	var diskErr error
	if pathErr == nil {
		disk, diskErr = readVersionStateAt(path)
	} else {
		diskErr = pathErr
	}
	a.versionUseMu.Lock()
	a.mu.Lock()
	if diskErr == nil && (disk.gate.RequiredVersion != "" || disk.gate.FailClosed) {
		a.versionGateState = disk.gate
		a.applyVersionGateLocked(disk.gate)
	}
	if diskErr != nil {
		a.versionGate = true
		a.versionCheckError = diskErr.Error()
		log.Printf("MeccyRadar local version gate state could not be trusted; access will fail closed: %v", diskErr)
	}
	knownGate := a.versionGateState
	a.mu.Unlock()
	a.versionUseMu.Unlock()

	if !auth.ValidReleaseVersion(buildinfo.Version) {
		err := errors.New("客户端构建版本无效，拒绝登录和连接")
		a.finishVersionCheck(err)
		return err
	}
	if client == nil {
		err := errors.New("版本检查失败，当前无法连接更新服务；未知强制要求时允许继续")
		if diskErr != nil {
			err = errors.New("本机版本门禁状态无法读取或已损坏；已阻止登录和连接，请联网重试")
		} else if knownGate.FailClosed {
			err = errors.New("上次收到的版本数据无效；离线检查失败，仍保持拦截")
		} else if knownGate.RequiredVersion != "" {
			err = errors.New("必须更新到新版才能继续；离线检查失败，仍保持拦截")
		}
		a.finishVersionCheck(err)
		return err
	}
	info, err := client.MeccyRadarVersion(context.Background())
	if err != nil {
		if errors.Is(err, auth.ErrInvalidResponse) {
			quarantine := knownGate
			quarantine.FailClosed = true
			if quarantine.RequiredVersion == "" {
				quarantine.DownloadURL = ""
			}
			a.versionUseMu.Lock()
			if pathErr == nil {
				if persistErr := writeVersionStateAt(path, quarantine); persistErr != nil {
					log.Printf("MeccyRadar invalid version response quarantine could not be persisted; access remains blocked")
				}
			}
			a.mu.Lock()
			a.versionGateState = quarantine
			a.versionGate = true
			a.versionCheckError = "服务端返回无效版本数据，已暂时阻止登录和连接；请重试"
			a.versionCheckPending = false
			snapshot := a.versionSnapshotLocked()
			a.mu.Unlock()
			a.emitVersionSnapshot(ctx, snapshot)
			a.versionUseMu.Unlock()
			return err
		}
		message := "版本检查失败，当前为离线状态；没有已知强制更新，允许继续使用"
		if diskErr != nil {
			message = "本机版本门禁状态无法读取或已损坏；已阻止登录和连接，请联网重试"
		} else if knownGate.FailClosed {
			message = "上次收到的版本数据无效；离线检查失败，仍保持拦截"
		} else if knownGate.RequiredVersion != "" {
			message = "必须更新到新版才能继续；离线检查失败，仍保持拦截"
		}
		a.finishVersionCheck(errors.New(message))
		return errors.New(message)
	}

	next := versionGateState{}
	if info.Configured && info.Force && auth.CompareVersions(info.Version, buildinfo.Version) > 0 {
		next = versionGateState{RequiredVersion: info.Version, DownloadURL: info.DownloadURL}
	}
	// Publish a newly required gate immediately when a valid response arrives so
	// in-flight login/restore operations can reject their late response. The write
	// lock below only waits for those local operations and is never held over HTTP.
	if next.RequiredVersion != "" {
		a.mu.Lock()
		a.versionGateState = next
		a.versionGate = true
		a.versionInfo = info
		a.versionCheckError = "此版本已停止使用，请更新后继续"
		snapshot := a.versionSnapshotLocked()
		a.mu.Unlock()
		a.emitVersionSnapshot(ctx, snapshot)
		if stopErr := a.stopTunnel(); stopErr != nil {
			log.Printf("MeccyRadar mandatory version gate could not stop the active tunnel: %v", stopErr)
		}
	}
	// A successful, valid response is authoritative; serialize only the short local
	// commit against in-flight login/tunnel setup (never the remote request).
	a.versionUseMu.Lock()
	if pathErr != nil {
		err = pathErr
	} else {
		err = writeVersionStateAt(path, next)
	}
	if err != nil {
		log.Printf("MeccyRadar version gate state persistence failed (version=%s); access remains blocked", next.RequiredVersion)
		a.mu.Lock()
		a.versionInfo = info
		a.versionGateState = next
		a.versionGate = true
		a.versionCheckError = "无法安全保存版本门禁状态，已暂时阻止登录和连接；请检查本机存储权限后重试"
		a.versionCheckPending = false
		snapshot := a.versionSnapshotLocked()
		a.mu.Unlock()
		a.emitVersionSnapshot(ctx, snapshot)
		a.versionUseMu.Unlock()
		return errors.New("版本门禁状态无法持久保存")
	}

	a.mu.Lock()
	a.versionInfo = info
	a.versionGateState = next
	a.versionCheckError = ""
	a.applyVersionGateLocked(next)
	a.versionCheckPending = false
	snapshot := a.versionSnapshotLocked()
	a.mu.Unlock()
	a.emitVersionSnapshot(ctx, snapshot)
	a.versionUseMu.Unlock()
	if snapshot.Blocked {
		return errors.New("此版本已停止使用，请更新后继续")
	}
	return nil
}

func (a *App) finishVersionCheck(err error) {
	a.mu.Lock()
	if err != nil {
		a.versionCheckError = err.Error()
	}
	a.versionCheckPending = false
	snapshot := a.versionSnapshotLocked()
	ctx := a.ctx
	a.mu.Unlock()
	a.emitVersionSnapshot(ctx, snapshot)
}

func (a *App) emitVersionSnapshot(ctx context.Context, snapshot VersionSnapshot) {
	if ctx != nil {
		runtime.EventsEmit(ctx, "version:checked", snapshot)
	}
}

func optionalVersionDownloadURL(info auth.VersionInfo) string {
	if !info.Configured || info.Force || !auth.ValidReleaseVersion(info.Version) || auth.CompareVersions(info.Version, buildinfo.Version) <= 0 || !auth.ValidDownloadURL(info.DownloadURL) {
		return ""
	}
	return info.DownloadURL
}

func (a *App) applyVersionGateLocked(state versionGateState) {
	a.versionGate = !auth.ValidReleaseVersion(buildinfo.Version) || state.FailClosed || (state.RequiredVersion != "" && auth.CompareVersions(buildinfo.Version, state.RequiredVersion) < 0)
}

func (a *App) versionDownloadURLLocked() string {
	if a.versionGate && a.versionGateState.RequiredVersion != "" && auth.ValidDownloadURL(a.versionGateState.DownloadURL) {
		return a.versionGateState.DownloadURL
	}
	if a.versionCheckPending {
		return ""
	}
	return optionalVersionDownloadURL(a.versionInfo)
}

func (a *App) versionSnapshotLocked() VersionSnapshot {
	return VersionSnapshot{Info: a.versionInfo, CurrentVersion: buildinfo.Version, RequiredVersion: a.versionGateState.RequiredVersion, DownloadURL: a.versionDownloadURLLocked(), Blocked: a.versionGate, Checking: a.versionCheckPending, CheckError: a.versionCheckError}
}

func (a *App) VersionState() VersionSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.versionSnapshotLocked()
}
func (a *App) IsVersionBlocked() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.versionGate
}
func (a *App) VersionCheckPending() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.versionCheckPending
}

func (a *App) OpenVersionDownload() error {
	return a.openVersionDownload(func(ctx context.Context, url string) { runtime.BrowserOpenURL(ctx, url) })
}

func (a *App) openVersionDownload(open func(context.Context, string)) error {
	a.mu.Lock()
	url, ctx, checking := a.versionDownloadURLLocked(), a.ctx, a.versionCheckPending
	knownForced := a.versionGate && a.versionGateState.RequiredVersion != ""
	a.mu.Unlock()
	if checking && !knownForced {
		return errors.New("版本检查进行中，暂不能打开下载页面")
	}
	if !auth.ValidDownloadURL(url) {
		return errors.New("未保存有效的新版下载链接")
	}
	if ctx == nil || open == nil {
		return errors.New("无法打开新版下载页面")
	}
	open(ctx, url)
	return nil
}

func (a *App) versionBlocked() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.versionGate
}
