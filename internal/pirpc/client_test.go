package pirpc

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func mockScript(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "scripts", "mock_pi_rpc.py")
}

func TestProtocolSubset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := New(ctx, ProcessConfig{Command: "python3", Arguments: []string{mockScript(t)}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	events := make(chan Event, 64)
	client.OnEvent(func(event Event) { events <- event })

	state, err := client.GetState(ctx)
	if err != nil || state.SessionFile == "" || state.Model == nil || state.Model.ContextWindow != 128000 {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	if err := client.SwitchSession(ctx, "/tmp/restored-pi-session.jsonl"); err != nil {
		t.Fatal(err)
	}
	state, err = client.GetState(ctx)
	if err != nil || state.SessionFile != "/tmp/restored-pi-session.jsonl" {
		t.Fatalf("resumed state=%#v err=%v", state, err)
	}
	models, err := client.GetAvailableModels(ctx)
	if err != nil || len(models) != 3 || models[1].Provider != "mockprov" || models[1].ID != "model-b" {
		t.Fatalf("models=%#v err=%v", models, err)
	}
	if err := client.SetModel(ctx, "mockprov", "model-b"); err != nil {
		t.Fatal(err)
	}
	if err := client.SetModel(ctx, "mockprov", "missing"); err == nil {
		t.Fatal("invalid model must fail")
	}
	if err := client.SetThinkingLevel(ctx, "high"); err != nil {
		t.Fatal(err)
	}

	result, err := client.PromptAndWait(ctx, "hello")
	if err != nil || result.Aborted || result.Disposition != "started" {
		t.Fatalf("prompt result=%#v err=%v", result, err)
	}
	types := map[string]bool{}
	for len(events) > 0 {
		event := <-events
		types[event.Type] = true
		var raw map[string]any
		if json.Unmarshal(event.Raw, &raw) != nil || raw["type"] != event.Type {
			t.Errorf("raw event was not preserved for %s: %q", event.Type, event.Raw)
		}
	}
	for _, eventType := range []string{"extension_ui_request", "message_update", "tool_execution_start", "tool_execution_update", "tool_execution_end", "message_end", "agent_end", "agent_settled"} {
		if !types[eventType] {
			t.Errorf("missing event %s", eventType)
		}
	}

	stats, raw, err := client.GetSessionStats(ctx)
	if err != nil || len(raw) == 0 {
		t.Fatalf("stats=%#v err=%v", stats, err)
	}
	if _, ok := SessionStatsBlock(stats); !ok {
		t.Fatalf("context usage missing: %#v", stats)
	}

	handled, err := client.PromptAndWait(ctx, "handled")
	if err != nil || handled.Disposition != "handled" {
		t.Fatalf("handled=%#v err=%v", handled, err)
	}
}

func TestPromptCancellationAndFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := New(ctx, ProcessConfig{Command: "python3", Arguments: []string{mockScript(t)}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	events := make(chan Event, 32)
	client.OnEvent(func(event Event) { events <- event })

	promptResult := make(chan PromptResult, 1)
	promptErr := make(chan error, 1)
	go func() {
		result, err := client.PromptAndWait(ctx, "long")
		promptResult <- result
		promptErr <- err
	}()
	select {
	case event := <-events:
		if event.Type != "agent_start" {
			t.Fatalf("first event=%#v", event)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := client.Abort(ctx); err != nil {
		t.Fatal(err)
	}
	if result := <-promptResult; !result.Aborted {
		t.Fatalf("cancel result=%#v", result)
	}
	if err := <-promptErr; err != nil {
		t.Fatal(err)
	}

	if _, err := client.PromptAndWait(ctx, "error"); err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("provider failure err=%v", err)
	}
	if _, err := client.PromptAndWait(ctx, "deferred"); err == nil || !strings.Contains(err.Error(), "deferred") {
		t.Fatalf("deferred failure err=%v", err)
	}
}
