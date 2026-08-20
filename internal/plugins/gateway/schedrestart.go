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
// deferred restart. A watchdog polls the bus and, once the system is idle —
// no chat turn in flight and no voice relay active — fires the same graceful
// restart path as handleRestart. The armed flag is in-memory only: an
// earlier crash or restart simply drops it.
// Variable (not const) so tests can shrink the poll interval.
var schedRestartPoll = 5 * time.Second

// schedRestartState is embedded in Server; atomic because the HTTP handler
// and the watchdog goroutine touch it concurrently.
type schedRestartState struct {
	armed atomic.Bool
}

// handleScheduleRestart arms the deferred restart. A second call while one
// is already armed reports already_scheduled instead of piling up.
func (s *Server) handleScheduleRestart(w http.ResponseWriter, _ *http.Request) {
	status := "scheduled"
	if !s.sched.armed.CompareAndSwap(false, true) {
		status = "already_scheduled"
	}
	slog.Info("restart scheduled; will fire once the system is idle")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"status":"` + status + `"}`))
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
		if !s.sched.armed.Load() {
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
		if !s.sched.armed.CompareAndSwap(true, false) {
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
