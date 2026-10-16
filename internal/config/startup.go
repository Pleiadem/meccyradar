package config

import "errors"

type LaunchMode string

const (
	LaunchManual LaunchMode = "manual"
	LaunchCached LaunchMode = "cached"
)

var ErrNoCachedSession = errors.New("no cached session")

func LoadCachedSession(store CredentialStore) (Session, error) {
	if store == nil {
		return Session{}, errors.New("credential store is required")
	}
	session, err := store.Load()
	if err != nil {
		return Session{}, ErrNoCachedSession
	}
	return session, nil
}
