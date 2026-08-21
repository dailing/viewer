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

	// Successful build: POST → building, then the goroutine arms.
	runReleaseBuild = func() error { return nil }
	rec := httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/schedule-restart", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("schedule-restart status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if body := rec.Body.String(); body != `{"status":"building"}` {
		t.Fatalf("schedule-restart body = %q", body)
	}
	waitSchedState(t, server, schedArmed)

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
	original := runReleaseBuild
	defer func() { runReleaseBuild = original }()

	server := New(DefaultConfig())

	// Failed build: state lands on failed, never arms; a new POST re-arms.
	var fail atomic.Bool
	fail.Store(true)
	runReleaseBuild = func() error {
		if fail.Load() {
			return errors.New("boom")
		}
		return nil
	}
	rec := httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/schedule-restart", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("schedule-restart status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	waitSchedState(t, server, schedFailed)

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
	waitSchedState(t, server, schedArmed)
}

func TestScheduleRestartCancel(t *testing.T) {
	original := runReleaseBuild
	defer func() { runReleaseBuild = original }()

	server := New(DefaultConfig())

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

	// Cancel mid-build: the finished build must not arm the watchdog.
	release := make(chan struct{})
	runReleaseBuild = func() error { <-release; return nil }
	rec = httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/schedule-restart", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("schedule-restart status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	rec = httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/admin/schedule-restart", nil))
	if rec.Body.String() != `{"status":"none"}` {
		t.Fatalf("cancel (building) body = %q", rec.Body.String())
	}
	close(release)
	// Give the build goroutine a chance to (wrongly) re-arm.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if s := server.sched.state.Load(); s != schedNone {
			t.Fatalf("state after cancelled build = %d, want none", s)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWatchScheduledRestart(t *testing.T) {
	originalIdle, originalRestart, originalPoll := systemIdle, restartSelf, schedRestartPoll
	schedRestartPoll = 10 * time.Millisecond
	defer func() {
		systemIdle, restartSelf, schedRestartPoll = originalIdle, originalRestart, originalPoll
	}()

	server := New(DefaultConfig())
	// Non-nil placeholder: the faked systemIdle never dereferences it.
	server.client = &busclient.Client{}

	var busy atomic.Bool
	busy.Store(true)
	systemIdle = func(context.Context, *busclient.Client) (bool, error) { return !busy.Load(), nil }
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
	if spawned.Load() {
		t.Fatal("restart fired while the system was busy")
	}

	busy.Store(false)
	deadline := time.Now().Add(2 * time.Second)
	for !spawned.Load() {
		if time.Now().After(deadline) {
			t.Fatal("restart did not fire once the system went idle")
		}
		time.Sleep(5 * time.Millisecond)
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
