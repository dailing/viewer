package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"viewer/sdk/go/busclient"
)

func waitSchedState(t *testing.T, server *Server, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for server.sched.state.Load() != want {
		if time.Now().After(deadline) {
			t.Fatalf("scheduled-restart state = %d, want %d", server.sched.state.Load(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestScheduleRestartHandler(t *testing.T) {
	original := runReleaseBuild
	defer func() { runReleaseBuild = original }()

	server := New(DefaultConfig())

	// POST only arms the schedule; the watchdog builds after the system is idle.
	var built atomic.Bool
	runReleaseBuild = func() error { built.Store(true); return nil }
	rec := httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/schedule-restart", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("schedule-restart status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if body := rec.Body.String(); body != `{"status":"armed"}` {
		t.Fatalf("schedule-restart body = %q", body)
	}
	if built.Load() {
		t.Fatal("POST started a build before the system was idle")
	}

	rec = httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/schedule-restart", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status endpoint code = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != `{"status":"armed"}` {
		t.Fatalf("status endpoint body = %q", body)
	}

	rec = httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/schedule-restart", nil))
	if body := rec.Body.String(); body != `{"status":"already_scheduled"}` {
		t.Fatalf("second schedule-restart body = %q", body)
	}
}

func TestScheduleRestartBuildFailure(t *testing.T) {
	originalBuild, originalIdle, originalRestart, originalPoll := runReleaseBuild, systemIdle, restartSelf, schedRestartPoll
	schedRestartPoll = 10 * time.Millisecond
	defer func() {
		runReleaseBuild, systemIdle, restartSelf, schedRestartPoll = originalBuild, originalIdle, originalRestart, originalPoll
	}()

	server := New(DefaultConfig())
	server.client = &busclient.Client{}
	systemIdle = func(context.Context, *busclient.Client) (bool, error) { return true, nil }
	var spawned atomic.Bool
	restartSelf = func(context.Context) error { spawned.Store(true); return nil }

	// A failed build lands on failed; a new POST re-arms and retries on idle.
	var fail atomic.Bool
	fail.Store(true)
	runReleaseBuild = func() error {
		if fail.Load() {
			return errors.New("boom")
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		server.watchScheduledRestart(ctx)
		close(done)
	}()

	rec := httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/schedule-restart", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("schedule-restart status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	waitSchedState(t, server, schedFailed)
	if spawned.Load() {
		t.Fatal("restart fired after a failed build")
	}

	rec = httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/schedule-restart", nil))
	if body := rec.Body.String(); body != `{"status":"failed"}` {
		t.Fatalf("status endpoint body = %q", body)
	}

	fail.Store(false)
	rec = httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/schedule-restart", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("re-arm schedule-restart status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !spawned.Load() {
		if time.Now().After(deadline) {
			t.Fatal("restart did not fire after the retry build succeeded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	<-done
	if server.sched.state.Load() != schedNone {
		t.Fatal("scheduled-restart state must return to none after firing")
	}
}

func TestScheduleRestartCancel(t *testing.T) {
	originalBuild, originalIdle, originalRestart, originalPoll := runReleaseBuild, systemIdle, restartSelf, schedRestartPoll
	schedRestartPoll = 10 * time.Millisecond
	defer func() {
		runReleaseBuild, systemIdle, restartSelf, schedRestartPoll = originalBuild, originalIdle, originalRestart, originalPoll
	}()

	server := New(DefaultConfig())
	server.client = &busclient.Client{}
	systemIdle = func(context.Context, *busclient.Client) (bool, error) { return true, nil }
	var spawned atomic.Bool
	restartSelf = func(context.Context) error { spawned.Store(true); return nil }

	// Cancel while armed: state drops to none, idempotent on repeat.
	server.sched.state.Store(schedArmed)
	rec := httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/admin/schedule-restart", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"none"}` {
		t.Fatalf("cancel (armed) = %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/admin/schedule-restart", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"none"}` {
		t.Fatalf("cancel (none) = %d %q", rec.Code, rec.Body.String())
	}

	// Cancel mid-build: the finished build must not restart or resurrect state.
	release := make(chan struct{})
	buildReturned := make(chan struct{})
	runReleaseBuild = func() error {
		<-release
		close(buildReturned)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		server.watchScheduledRestart(ctx)
		close(done)
	}()
	rec = httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/schedule-restart", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("schedule-restart status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	waitSchedState(t, server, schedBuilding)
	rec = httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/admin/schedule-restart", nil))
	if rec.Body.String() != `{"status":"none"}` {
		t.Fatalf("cancel (building) body = %q", rec.Body.String())
	}
	close(release)
	<-buildReturned // the watchdog must stop reading the global hook before the test restores it
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if s := server.sched.state.Load(); s != schedNone {
			t.Fatalf("state after cancelled build = %d, want none", s)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if spawned.Load() {
		t.Fatal("restart fired after the building schedule was cancelled")
	}
	cancel()
	<-done
}

func TestWatchScheduledRestart(t *testing.T) {
	originalBuild, originalIdle, originalRestart, originalPoll := runReleaseBuild, systemIdle, restartSelf, schedRestartPoll
	schedRestartPoll = 10 * time.Millisecond
	defer func() {
		runReleaseBuild, systemIdle, restartSelf, schedRestartPoll = originalBuild, originalIdle, originalRestart, originalPoll
	}()

	server := New(DefaultConfig())
	// Non-nil placeholder: the faked systemIdle never dereferences it.
	server.client = &busclient.Client{}

	var busy atomic.Bool
	busy.Store(true)
	systemIdle = func(context.Context, *busclient.Client) (bool, error) { return !busy.Load(), nil }
	var built atomic.Bool
	runReleaseBuild = func() error { built.Store(true); return nil }
	var spawned atomic.Bool
	restartSelf = func(context.Context) error { spawned.Store(true); return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		server.watchScheduledRestart(ctx)
		close(done)
	}()

	server.sched.state.Store(schedArmed)
	select {
	case <-time.After(200 * time.Millisecond):
	case <-done:
		t.Fatal("watchdog exited while the system was still busy")
	}
	if built.Load() {
		t.Fatal("build started while the system was busy")
	}
	if spawned.Load() {
		t.Fatal("restart fired while the system was busy")
	}

	// A concurrent manual build keeps the schedule armed for a later retry.
	if !acquireBuild() {
		t.Fatal("could not acquire the test build guard")
	}
	locked := true
	defer func() {
		if locked {
			releaseBuild()
		}
	}()
	busy.Store(false)
	time.Sleep(50 * time.Millisecond)
	if built.Load() {
		t.Fatal("scheduled build started while the build guard was occupied")
	}
	if state := server.sched.state.Load(); state != schedArmed {
		t.Fatalf("state with occupied build guard = %d, want armed", state)
	}
	releaseBuild()
	locked = false

	deadline := time.Now().Add(2 * time.Second)
	for !spawned.Load() {
		if time.Now().After(deadline) {
			t.Fatal("restart did not fire once the system went idle")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !built.Load() {
		t.Fatal("restart fired without a release build")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not exit after firing the restart")
	}
	if server.sched.state.Load() != schedNone {
		t.Fatal("scheduled-restart state must return to none after firing")
	}
}
