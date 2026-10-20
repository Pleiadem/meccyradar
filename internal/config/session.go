package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const defaultServiceName = "meccyradar"

var (
	ErrEmptyCardKey    = errors.New("请输入卡密")
	ErrSessionNotFound = os.ErrNotExist
)

type Session struct {
	CardKey     string `json:"cardKey"`
	SavedAtUnix int64  `json:"savedAtUnix"`
	// 雷达密码由用户填写，需要跨重启保留：否则每次启动后复制出的链接
	// 都会缺 k= 参数（生产上表现为 "...&k="）。
	RadarPassword string `json:"radarPassword,omitempty"`
}

type CredentialStore interface {
	Save(Session) error
	Load() (Session, error)
	Clear() error
}

type Protector interface {
	Protect([]byte) ([]byte, error)
	Unprotect([]byte) ([]byte, error)
}

type FileStore struct {
	path      string
	protector Protector
}

func NewFileStore(path string, protector Protector) (*FileStore, error) {
	if path == "" {
		return nil, errors.New("缺少会话文件路径")
	}
	if protector == nil {
		return nil, errors.New("缺少会话保护器")
	}
	return &FileStore{path: path, protector: protector}, nil
}

func (s *FileStore) Save(session Session) error {
	if session.CardKey == "" {
		return ErrEmptyCardKey
	}
	if session.SavedAtUnix == 0 {
		session.SavedAtUnix = time.Now().Unix()
	}
	plain, err := json.Marshal(session)
	if err != nil {
		return err
	}
	protected, err := s.protector.Protect(plain)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".session-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(protected); err != nil {
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

func (s *FileStore) Load() (Session, error) {
	protected, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Session{}, os.ErrNotExist
		}
		return Session{}, err
	}
	plain, err := s.protector.Unprotect(protected)
	if err != nil {
		return Session{}, errors.New("本地会话无法解密")
	}
	var session Session
	if err := json.Unmarshal(plain, &session); err != nil {
		return Session{}, errors.New("本地会话无效")
	}
	if session.CardKey == "" {
		return Session{}, errors.New("本地会话中的卡密为空")
	}
	return session, nil
}

func (s *FileStore) Clear() error {
	err := os.Remove(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func DefaultSessionPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, defaultServiceName, "session.dat"), nil
}

func NewDefaultStore() (CredentialStore, error) {
	path, err := DefaultSessionPath()
	if err != nil {
		return nil, err
	}
	protector, err := NewPlatformProtector(defaultServiceName)
	if err != nil {
		return nil, err
	}
	return NewFileStore(path, protector)
}
