package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// 客户端本地设置。
//
// 与 Session 分开存：Session 是凭据（加密、退出登录要清），设置是用户偏好
// （明文、退出登录必须保留）。混在一起会导致"退出登录后开机自启也丢了"。

type Settings struct {
	// AutoStart 表示用户是否希望开机自动启动。
	//
	// 这只是**意图**：真正的状态由系统启动目录里的快捷方式决定，
	// 两者由 reconcileAutostart 在启动时对齐。
	AutoStart bool `json:"autoStart"`
}

type SettingsStore struct {
	path string
}

func NewFileSettingsStore(path string) (*SettingsStore, error) {
	if path == "" {
		return nil, errors.New("缺少设置文件路径")
	}
	return &SettingsStore{path: path}, nil
}

func DefaultSettingsPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, defaultServiceName, "settings.json"), nil
}

func NewDefaultSettingsStore() (*SettingsStore, error) {
	path, err := DefaultSettingsPath()
	if err != nil {
		return nil, err
	}
	return NewFileSettingsStore(path)
}

// Load 读取设置。文件不存在或损坏时返回零值而不是报错：
// 设置读不出来最多是"开机不自启"，不该拦住用户登录。
func (s *SettingsStore) Load() Settings {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return Settings{}
	}
	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return Settings{}
	}
	return settings
}

func (s *SettingsStore) Save(settings Settings) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	// 与 session.dat 一样走"临时文件 + rename"，避免写一半损坏。
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".settings-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}
