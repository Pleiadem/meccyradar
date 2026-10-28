package main

import (
	"testing"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
)

func sessionStoreTestApp(store *blockingSessionStore) *App {
	app := newApp(appDependencies{store: store})
	app.mu.Lock()
	app.cardKey = "CARD-A"
	app.sessionGeneration = 1
	app.authData = ptrAuthData(testLinkAuthData("CARD-A"))
	app.radarPassword = "initial-password"
	app.radarPasswordInitialized = true
	app.pendingLinkAckInitialized = true
	app.mu.Unlock()
	return app
}

func ptrAuthData(data auth.AuthData) *auth.AuthData { return &data }

func loadStored(t *testing.T, store *blockingSessionStore) config.Session {
	t.Helper()
	session, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func waitForSave(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("credential Save did not reach its deterministic block")
	}
}

func TestPasswordChangedDuringLoginSaveIsPersistedAfterLoginCommit(t *testing.T) {
	server := sessionLifecycleServer(t)
	defer server.Close()
	client, err := auth.NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	store := newBlockingSessionStore()
	if err := store.inner.Save(config.Session{CardKey: "CARD-A", RadarPassword: "old-password"}); err != nil {
		t.Fatal(err)
	}
	app := newApp(appDependencies{client: client, store: store})
	started, release := store.blockNextSaveFor("CARD-A")
	loginDone := make(chan error, 1)
	go func() {
		_, loginErr := app.LoginByCard("CARD-A")
		loginDone <- loginErr
	}()
	waitForSave(t, started)
	if err := app.SaveRadarPassword("new-password"); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-loginDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("login did not finish after the blocked credential save was released")
	}
	got := loadStored(t, store)
	if got.RadarPassword != "new-password" {
		t.Fatalf("password changed during login must be persisted, got %q", got.RadarPassword)
	}
	if app.RadarPassword() != "new-password" {
		t.Fatalf("memory and disk password diverged: memory=%q disk=%q", app.RadarPassword(), got.RadarPassword)
	}
}

func TestBlockedPasswordSaveThenAckEnqueuePersistsLatestCompleteState(t *testing.T) {
	store := newBlockingSessionStore()
	if err := store.inner.Save(config.Session{CardKey: "CARD-A", RadarPassword: "initial-password"}); err != nil {
		t.Fatal(err)
	}
	app := sessionStoreTestApp(store)
	started, release := store.blockNextSaveFor("CARD-A")
	passwordDone := make(chan error, 1)
	go func() { passwordDone <- app.SaveRadarPassword("new-password") }()
	waitForSave(t, started)

	ackDone := make(chan struct{})
	go func() {
		app.queueLinkAck(2, "lease-CARD-A")
		close(ackDone)
	}()
	close(release)
	if err := <-passwordDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-ackDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ack enqueue did not persist after password save")
	}
	got := loadStored(t, store)
	if got.RadarPassword != "new-password" || got.PendingLinkAckVersion != 2 || got.PendingLinkAckLeaseID != "lease-CARD-A" {
		t.Fatalf("later pending ack must coexist with latest password, got %+v", got)
	}
}

func TestBlockedAckEnqueueThenClearPersistsClearedState(t *testing.T) {
	store := newBlockingSessionStore()
	if err := store.inner.Save(config.Session{CardKey: "CARD-A", RadarPassword: "pw"}); err != nil {
		t.Fatal(err)
	}
	app := sessionStoreTestApp(store)
	started, release := store.blockNextSaveFor("CARD-A")
	enqueueDone := make(chan struct{})
	go func() { app.queueLinkAck(2, "lease-CARD-A"); close(enqueueDone) }()
	waitForSave(t, started)
	clearDone := make(chan struct{})
	task := linkAckTask{generation: 1, cardKey: "CARD-A", leaseID: "lease-CARD-A", version: 2}
	go func() { app.clearLinkAck(task, true); close(clearDone) }()
	close(release)
	for name, done := range map[string]<-chan struct{}{"enqueue": enqueueDone, "clear": clearDone} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not finish", name)
		}
	}
	got := loadStored(t, store)
	if got.PendingLinkAckVersion != 0 || got.PendingLinkAckLeaseID != "" {
		t.Fatalf("accepted ack must not be resurrected by the earlier enqueue snapshot: %+v", got)
	}
}

func TestBlockedAckClearThenNewEnqueuePersistsNewTask(t *testing.T) {
	store := newBlockingSessionStore()
	if err := store.inner.Save(config.Session{CardKey: "CARD-A", RadarPassword: "pw", PendingLinkAckVersion: 2, PendingLinkAckLeaseID: "lease-CARD-A"}); err != nil {
		t.Fatal(err)
	}
	app := sessionStoreTestApp(store)
	app.mu.Lock()
	app.pendingLinkAckVersion = 2
	app.pendingLinkAckLeaseID = "lease-CARD-A"
	app.pendingLinkAckCardKey = "CARD-A"
	app.pendingLinkAckGeneration = 1
	app.mu.Unlock()
	started, release := store.blockNextSaveFor("CARD-A")
	task := linkAckTask{generation: 1, cardKey: "CARD-A", leaseID: "lease-CARD-A", version: 2}
	clearDone := make(chan struct{})
	go func() { app.clearLinkAck(task, true); close(clearDone) }()
	waitForSave(t, started)

	app.mu.Lock()
	app.authData.Server.LinkVersion = 3
	app.authData.Server.CopyPending = true
	app.mu.Unlock()
	newAckDone := make(chan struct{})
	go func() { app.queueLinkAck(3, "lease-CARD-A"); close(newAckDone) }()
	close(release)
	for name, done := range map[string]<-chan struct{}{"clear": clearDone, "new ack": newAckDone} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not finish", name)
		}
	}
	got := loadStored(t, store)
	if got.PendingLinkAckVersion != 3 || got.PendingLinkAckLeaseID != "lease-CARD-A" {
		t.Fatalf("a stale clear save must not overwrite the newer pending ack: %+v", got)
	}
}
