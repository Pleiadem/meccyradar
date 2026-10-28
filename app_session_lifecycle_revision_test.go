package main

import (
	"errors"
	"testing"
	"time"

	"meccyradar/internal/auth"
	"meccyradar/internal/config"
)

func TestLateChangeResponseCannotLogoutReplacementSession(t *testing.T) {
	m := newChangeServerMock(t)
	m.changeStarted = make(chan struct{})
	m.releaseChange = make(chan struct{})
	app := newChangeServerApp(t, m)
	t.Cleanup(func() { _ = app.stopTunnel() })

	changeDone := make(chan error, 1)
	go func() {
		_, err := app.ChangeServer()
		changeDone <- err
	}()
	select {
	case <-m.changeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("A's change request did not block at the controlled response")
	}
	if err := app.Logout(); err != nil {
		t.Fatalf("logout A: %v", err)
	}
	if _, err := app.LoginByCard("CARD-B"); err != nil {
		t.Fatalf("login B: %v", err)
	}
	app.mu.Lock()
	leaseB, cardB := app.authData.Session.LeaseID, app.cardKey
	app.mu.Unlock()
	if cardB != "CARD-B" || leaseB != "lease-2" {
		t.Fatalf("precondition: expected B current, card=%q lease=%q", cardB, leaseB)
	}
	close(m.releaseChange)
	select {
	case err := <-changeDone:
		if err == nil || !errors.Is(err, errRefreshedAuthNotAdopted) {
			t.Fatalf("stale A response must fail adoption: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old change request did not finish")
	}
	app.mu.Lock()
	stillB := app.cardKey == "CARD-B" && app.authData != nil && app.authData.Session.LeaseID == leaseB
	app.mu.Unlock()
	if !stillB {
		t.Fatal("cleanup for A must not log out or replace B")
	}
	stored, err := app.deps.store.Load()
	if err != nil || stored.CardKey != "CARD-B" {
		t.Fatalf("A's cleanup must not clear B credentials: card=%q err=%v", stored.CardKey, err)
	}
	m.logoutMu.Lock()
	for _, lease := range m.logoutLeases {
		if lease == leaseB {
			m.logoutMu.Unlock()
			t.Fatal("stale A cleanup sent Logout for B's lease")
		}
	}
	m.logoutMu.Unlock()
}

func TestOverlappingLogoutCallsKeepLoginBarrierUntilAllFinish(t *testing.T) {
	m := newChangeServerMock(t)
	m.logoutStarted = make(chan struct{})
	m.releaseLogout = make(chan struct{})
	app := newChangeServerApp(t, m)

	firstDone := make(chan error, 1)
	go func() { firstDone <- app.Logout() }()
	select {
	case <-m.logoutStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first logout did not reach the controlled server response")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- app.Logout() }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		app.loginMu.Lock()
		requests := app.logoutRequests
		blocked := app.logoutInProgress
		app.loginMu.Unlock()
		if requests >= 2 && blocked {
			break
		}
		time.Sleep(time.Millisecond)
	}
	app.loginMu.Lock()
	requests, blocked := app.logoutRequests, app.logoutInProgress
	app.loginMu.Unlock()
	if requests < 2 || !blocked {
		t.Fatal("second logout must join the barrier before waiting for the first")
	}
	if _, err := app.LoginByCard("CARD-B"); err == nil {
		t.Fatal("login must remain rejected while either logout call is active")
	}
	if got := m.loginCalls.Load(); got != 1 {
		t.Fatalf("a blocked login must not create a server lease, calls=%d", got)
	}
	close(m.releaseLogout)
	for name, done := range map[string]<-chan error{"first": firstDone, "second": secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s logout: %v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s logout did not finish", name)
		}
	}
	app.loginMu.Lock()
	barrierStillActive := app.logoutInProgress || app.logoutRequests != 0
	app.loginMu.Unlock()
	if barrierStillActive {
		t.Fatal("logout barrier must release after every serialized cleanup is complete")
	}
	if _, err := app.LoginByCard("CARD-B"); err != nil {
		t.Fatalf("login should succeed after both exits complete: %v", err)
	}
	stored, err := app.deps.store.Load()
	if err != nil || stored.CardKey != "CARD-B" {
		t.Fatalf("new login credentials must survive completed exits: card=%q err=%v", stored.CardKey, err)
	}
}

func TestReconcileFenceInvalidationReleasesVersionWriteLock(t *testing.T) {
	app := &App{}
	fence := sessionFence{generation: 1, cardKey: "CARD-A", leaseID: "lease-A"}
	started := make(chan struct{})
	reconcileDone := make(chan error, 1)

	// Hold the writer lock until the fence is stale. The reconciliation must
	// acquire its read lock, take the stale-fence return, and release that lock.
	app.versionUseMu.Lock()
	go func() {
		close(started)
		_, err := app.reconcileChangeFailure(nil, "CARD-A", nil, fence, &auth.TransportError{Message: "test transport failure"})
		reconcileDone <- err
	}()
	<-started
	app.mu.Lock()
	app.sessionGeneration++
	app.mu.Unlock()
	app.versionUseMu.Unlock()

	select {
	case err := <-reconcileDone:
		if !errors.Is(err, errSessionGenerationChanged) {
			t.Fatalf("stale reconciliation should stop before login: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reconciliation did not return after the version lock was released")
	}

	writeLockDone := make(chan struct{})
	go func() {
		app.versionUseMu.Lock()
		app.versionUseMu.Unlock()
		close(writeLockDone)
	}()
	select {
	case <-writeLockDone:
	case <-time.After(time.Second):
		t.Fatal("stale-fence return leaked versionUseMu.RLock and blocked a version writer")
	}
}

func TestFailedLateAdoptionDoesNotClearStorageOfNewGeneration(t *testing.T) {
	store := newBlockingSessionStore()
	if err := store.inner.Save(config.Session{CardKey: "CARD-A", RadarPassword: "A"}); err != nil {
		t.Fatal(err)
	}
	app := sessionStoreTestApp(store)
	app.mu.Lock()
	fenceA := app.sessionFenceLocked()
	app.mu.Unlock()
	app.mu.Lock()
	app.sessionGeneration++
	app.cardKey = "CARD-B"
	app.authData = ptrAuthData(testLinkAuthData("CARD-B"))
	app.mu.Unlock()
	if err := app.logoutIfSessionCurrent(fenceA); err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	stillB := app.cardKey == "CARD-B" && app.authData != nil
	app.mu.Unlock()
	if !stillB {
		t.Fatal("fenced logout for A must be a no-op after session B is current")
	}
}
