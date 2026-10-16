package config

import (
	"errors"
	"os"
	"testing"
)

type testProtector struct{}

func (testProtector) Protect(data []byte) ([]byte, error) { return append([]byte("p:"), data...), nil }
func (testProtector) Unprotect(data []byte) ([]byte, error) {
	if len(data) < 2 || string(data[:2]) != "p:" {
		return nil, errors.New("bad data")
	}
	return data[2:], nil
}

func TestFileStoreRoundTripAndClear(t *testing.T) {
	path := t.TempDir() + "/session.dat"
	store, err := NewFileStore(path, testProtector{})
	if err != nil {
		t.Fatal(err)
	}
	want := Session{CardKey: "MECCY-TEST"}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.CardKey != want.CardKey || got.SavedAtUnix == 0 {
		t.Fatalf("unexpected session: %+v", got)
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestFileStoreRejectsInvalidSession(t *testing.T) {
	store, err := NewFileStore(t.TempDir()+"/session.dat", testProtector{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(Session{}); !errors.Is(err, ErrEmptyCardKey) {
		t.Fatalf("expected empty key, got %v", err)
	}
}

func TestFileStoreRejectsTampering(t *testing.T) {
	path := t.TempDir() + "/session.dat"
	store, err := NewFileStore(path, testProtector{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(Session{CardKey: "MECCY-TEST"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("expected tampered session error")
	}
}

func TestMemoryStoreLifecycle(t *testing.T) {
	store := NewMemoryStore()
	if _, err := store.Load(); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal(err)
	}
	if err := store.Save(Session{CardKey: "MECCY-TEST"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || got.CardKey != "MECCY-TEST" {
		t.Fatalf("unexpected session: %+v, %v", got, err)
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
}
