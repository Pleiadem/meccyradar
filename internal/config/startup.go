package config

import "errors"

type LaunchMode string

const (
	LaunchManual LaunchMode = "manual"
	LaunchCached LaunchMode = "cached"
)

var ErrNoCachedSession = errors.New("没有可用的本地会话")

func LoadCachedSession(store CredentialStore) (Session, error) {
	if store == nil {
		return Session{}, errors.New("缺少凭据存储")
	}
	session, err := store.Load()
	if err != nil {
		return Session{}, ErrNoCachedSession
	}
	return session, nil
}
