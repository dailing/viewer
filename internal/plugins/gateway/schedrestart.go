package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"viewer/sdk/go/busclient"
)

// Scheduled restart: POST /api/admin/schedule-restart arms a one-shot
// deferred build+restart. Restarting without rebuilding would load the same
// old binary and make the restart meaningless, so the release build
// (web/build-release.sh) runs immediately in the background — the old
// server keeps serving meanwhile. Only a successful build arms the
// watchdog, which then polls the bus and fires the same graceful restart
// path as handleRestart once the system is idle (no chat turn in flight,
// no voice relay active). A failed build never arms: the running binary
// stays and GET /api/admin/schedule-restart reports "failed" until the next
// POST re-arms. DELETE /api/admin/schedule-restart cancels a pending
// schedule (armed or mid-build) or resets the failed state. The state is
// in-memory only: an earlier crash or restart simply drops it.
// Variable (not const) so tests can shrink the poll interval.
var schedRestartPoll = 5 * time.Second

const (
	schedNone int32 = iota
	schedBuilding
	schedArmed
	schedFailed
)

// schedRestartState is embedded in Server; atomic because the HTTP handler,
// the build goroutine, and the watchdog goroutine touch it concurrently.
type schedRestartState struct {
	state atomic.Int32
}

func (s *Server) schedStatus() string {
	switch s.sched.state.Load() {
	case schedBuilding:
		return "building"
	case schedArmed:
		return "armed"
	case schedFailed:
		return "failed"
	default:
		return "none"
	}
}

func writeSchedStatus(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"status":"` + status + `"}`))
}

// handleScheduleRestart kicks off the release build and arms the deferred
// restart on success. A call while one is already building or armed reports
// the current state instead of piling up; after a failed build a new POST
// re-arms.
func (s *Server) handleScheduleRestart(w http.ResponseWriter, _ *http.Request) {
	for {
		cur := s.sched.state.Load()
		if cur == schedBuilding || cur == schedArmed {
			status := "building"
			if cur == schedArmed {
				status = "already_scheduled"
			}
			slog.Info("scheduled-restart: already in progress", "state", s.schedStatus())
			writeSchedStatus(w, http.StatusAccepted, status)
			return
		}
		// schedNone or schedFailed → (re-)arm.
		if s.sched.state.CompareAndSwap(cur, schedBuilding) {
			break
		}
	}
	if !acquireBuild() {
		// A manual build-restart is in flight; undo and report conflict.
		s.sched.state.Store(schedNone)
		http.Error(w, "a build is already running", http.StatusConflict)
		return
	}
	go func() {
		err := runReleaseBuild()
		releaseBuild()
		// CAS (not Store): a DELETE cancel while the build was running moved
		// the state to none — the finished build must not resurrect the arm.
		if err != nil {
			slog.Error("scheduled-restart: build failed, staying on the current binary", "error", err, "log", "/tmp/viewerd-build.log")
			s.sched.state.CompareAndSwap(schedBuilding, schedFailed)
			return
		}
		if s.sched.state.CompareAndSwap(schedBuilding, schedArmed) {
			slog.Info("scheduled-restart: build succeeded; will restart once the system is idle")
		} else {
			slog.Info("scheduled-restart: build succeeded but the schedule was cancelled meanwhile")
		}
	}()
	writeSchedStatus(w, http.StatusAccepted, "building")
}

// handleScheduleRestartStatus reports the current scheduled-restart state
// ("none" | "building" | "armed" | "failed") so the frontend can tell a
// failed build apart from a long wait for idle.
func (s *Server) handleScheduleRestartStatus(w http.ResponseWriter, _ *http.Request) {
	writeSchedStatus(w, http.StatusOK, s.schedStatus())
}

// handleScheduleRestartCancel disarms a pending scheduled restart (armed or
// still building) and resets the failed state. Idempotent: cancelling when
// nothing is scheduled just reports "none". A build already in flight is not
// killed — it finishes, but its goroutine can no longer arm the watchdog
// (the building→armed transition is a CAS).
func (s *Server) handleScheduleRestartCancel(w http.ResponseWriter, _ *http.Request) {
	for {
		cur := s.sched.state.Load()
		if cur == schedNone {
			break
		}
		if s.sched.state.CompareAndSwap(cur, schedNone) {
			slog.Info("scheduled-restart: cancelled", "was", cur)
			break
		}
	}
	writeSchedStatus(w, http.StatusOK, s.schedStatus())
}

// watchScheduledRestart runs until ctx is cancelled or the armed restart
// fires. Started from Start; the gateway Shutdown context stops it.
func (s *Server) watchScheduledRestart(ctx context.Context) {
	ticker := time.NewTicker(schedRestartPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if s.sched.state.Load() != schedArmed {
			continue
		}
		if s.client == nil {
			// Assets pipeline disabled (no bus client): the idle check has
			// nothing to talk to. Keep waiting rather than restarting blind.
			continue
		}
		idle, err := systemIdle(ctx, s.client)
		if err != nil {
			slog.Warn("scheduled-restart idle check failed", "error", err)
			continue
		}
		if !idle {
			continue
		}
		if !s.sched.state.CompareAndSwap(schedArmed, schedNone) {
			continue
		}
		slog.Info("scheduled-restart: system idle, restarting")
		// Background context: the arming request is long gone by now.
		if err := restartSelf(context.Background()); err != nil {
			slog.Error("scheduled-restart: restart failed", "error", err)
		}
		return
	}
}

// systemIdle reports whether no chat turn is in flight and no voice relay is
// active. Variable so tests can inject a fake.
var systemIdle = func(ctx context.Context, client *busclient.Client) (bool, error) {
	chats, err := client.Request(ctx, "chat:_:chats:list", map[string]any{}, schedRestartPoll)
	if err != nil {
		return false, fmt.Errorf("chat running check: %w", err)
	}
	if payload, ok := chats.(map[string]any); ok {
		if running, ok := payload["running_chat_ids"].([]any); ok && len(running) > 0 {
			return false, nil
		}
	}
	voice, err := client.Request(ctx, "voice:_:sessions", map[string]any{}, schedRestartPoll)
	if err != nil {
		return false, fmt.Errorf("voice sessions check: %w", err)
	}
	if payload, ok := voice.(map[string]any); ok {
		if sessions, ok := payload["sessions"].([]any); ok && len(sessions) > 0 {
			return false, nil
		}
	}
	return true, nil
}
