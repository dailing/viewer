package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"viewer/sdk/go/busclient"
)

func TestScheduleRestartHandler(t *testing.T) {
	server := New(DefaultConfig())

	rec := httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/schedule-restart", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("schedule-restart status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if body := rec.Body.String(); body != `{"status":"scheduled"}` {
		t.Fatalf("schedule-restart body = %q", body)
	}

	rec = httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/schedule-restart", nil))
	if body := rec.Body.String(); body != `{"status":"already_scheduled"}` {
		t.Fatalf("second schedule-restart body = %q", body)
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

	server.sched.armed.Store(true)
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
	if server.sched.armed.Load() {
		t.Fatal("armed flag must be cleared after firing")
	}
}
