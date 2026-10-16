package config

import "sync"

type MemoryStore struct {
	mu      sync.RWMutex
	session Session
	has     bool
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

func (s *MemoryStore) Save(session Session) error {
	if session.CardKey == "" {
		return ErrEmptyCardKey
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session, s.has = session, true
	return nil
}

func (s *MemoryStore) Load() (Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.has {
		return Session{}, ErrSessionNotFound
	}
	return s.session, nil
}

func (s *MemoryStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session, s.has = Session{}, false
	return nil
}
