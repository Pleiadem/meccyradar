package main

import (
	"errors"
	"time"

	"meccyradar/internal/config"
)

var errSessionGenerationChanged = errors.New("会话已变化，忽略过期凭据操作")

type sessionFence struct {
	generation uint64
	cardKey    string
	leaseID    string
}

func (a *App) sessionFenceLocked() sessionFence {
	fence := sessionFence{generation: a.sessionGeneration, cardKey: a.cardKey}
	if a.authData != nil {
		fence.leaseID = a.authData.Session.LeaseID
	}
	return fence
}

func (a *App) currentSessionFence() sessionFence {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessionFenceLocked()
}

func (a *App) sessionFenceActive(expected sessionFence) bool {
	a.loginMu.Lock()
	if a.logoutInProgress {
		a.loginMu.Unlock()
		return false
	}
	a.mu.Lock()
	matches := sameSessionFence(a.sessionFenceLocked(), expected)
	a.mu.Unlock()
	a.loginMu.Unlock()
	return matches
}

func sameSessionFence(left, right sessionFence) bool {
	return left.generation == right.generation && left.cardKey == right.cardKey && left.leaseID == right.leaseID
}

// loadStoredSession serializes reads with writes and Logout's Clear. The lock is
// always acquired before App.mu, and is never held across network/monitor work.
func (a *App) loadStoredSession() (config.Session, error) {
	a.sessionStoreMu.Lock()
	defer a.sessionStoreMu.Unlock()
	a.mu.Lock()
	store := a.deps.store
	a.mu.Unlock()
	if store == nil {
		return config.Session{}, errors.New("凭据存储不可用")
	}
	return store.Load()
}

// currentSessionLocked builds a fresh complete session snapshot. Caller holds
// App.mu. Pending ack data is included only when it belongs to this card and
// current generation; no caller-provided snapshot may roll it backwards.
func (a *App) currentSessionLocked(cardKey string, stored config.Session) config.Session {
	password := a.radarPassword
	if !a.radarPasswordInitialized && stored.CardKey == cardKey {
		password = stored.RadarPassword
	}
	session := config.Session{CardKey: cardKey, SavedAtUnix: time.Now().Unix(), RadarPassword: password}
	if a.pendingLinkAckInitialized {
		if a.pendingLinkAckVersion > 0 && a.pendingLinkAckLeaseID != "" &&
			a.pendingLinkAckCardKey == cardKey && a.pendingLinkAckGeneration == a.sessionGeneration {
			session.PendingLinkAckVersion = a.pendingLinkAckVersion
			session.PendingLinkAckLeaseID = a.pendingLinkAckLeaseID
		}
	} else if stored.CardKey == cardKey {
		session.PendingLinkAckVersion = stored.PendingLinkAckVersion
		session.PendingLinkAckLeaseID = stored.PendingLinkAckLeaseID
	}
	return session
}

func storedSessionLocked(store config.CredentialStore) config.Session {
	if store == nil {
		return config.Session{}
	}
	session, err := store.Load()
	if err != nil {
		return config.Session{}
	}
	return session
}

// saveSessionForCurrent takes the serialized storage lock first, then reads the
// latest full in-memory session under App.mu and writes that state. This avoids
// stale same-generation snapshots overwriting a newer password or ack update.
func (a *App) saveSessionForCurrent(generation uint64, cardKey string) error {
	a.sessionStoreMu.Lock()
	defer a.sessionStoreMu.Unlock()
	a.mu.Lock()
	store := a.deps.store
	a.mu.Unlock()
	stored := storedSessionLocked(store)
	a.mu.Lock()
	if generation != a.sessionGeneration || cardKey == "" || cardKey != a.cardKey {
		a.mu.Unlock()
		return errSessionGenerationChanged
	}
	session := a.currentSessionLocked(cardKey, stored)
	a.mu.Unlock()
	if store == nil {
		return errors.New("凭据存储不可用")
	}
	return store.Save(session)
}

// saveLoginSession commits the latest in-memory password/pending state for the
// login card. Caller holds loginMu for the whole login commit; sessionStoreMu
// orders this write with every ack/password Save and Logout Clear.
func (a *App) saveLoginSession(expectedLoginGeneration uint64, cardKey string) (config.Session, uint64, error) {
	a.sessionStoreMu.Lock()
	defer a.sessionStoreMu.Unlock()
	a.mu.Lock()
	store := a.deps.store
	a.mu.Unlock()
	stored := storedSessionLocked(store)
	a.mu.Lock()
	session := a.currentSessionLocked(cardKey, stored)
	passwordRevision := a.radarPasswordRevision
	if a.cardKey != "" && a.cardKey != cardKey {
		// A card switch must not carry the previous card's pending ack marker.
		session.PendingLinkAckVersion = 0
		session.PendingLinkAckLeaseID = ""
	}
	a.mu.Unlock()
	// Caller holds loginMu, which protects these login lifecycle flags.
	if a.loginGeneration != expectedLoginGeneration || a.logoutInProgress {
		return config.Session{}, 0, errSessionGenerationChanged
	}
	if store == nil {
		return config.Session{}, 0, errors.New("凭据存储不可用")
	}
	if err := store.Save(session); err != nil {
		return config.Session{}, 0, err
	}
	return session, passwordRevision, nil
}

// clearStoredSession is called only after Logout invalidates the current
// generation. All older writers either finish before this Clear or observe the
// new generation when constructing their state; no stale Save can resurrect it.
func (a *App) clearStoredSession() error {
	a.sessionStoreMu.Lock()
	defer a.sessionStoreMu.Unlock()
	a.mu.Lock()
	store := a.deps.store
	a.mu.Unlock()
	if store == nil {
		return nil
	}
	return store.Clear()
}
