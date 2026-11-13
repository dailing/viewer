package pirpc

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestRealPiRPCProbe is an opt-in compatibility probe for the installed Pi
// binary. It performs read-only discovery and never sends a model prompt.
func TestRealPiRPCProbe(t *testing.T) {
	if os.Getenv("VIEWER_PI_REAL_PROBE") != "1" {
		t.Skip("set VIEWER_PI_REAL_PROBE=1 to probe the installed Pi binary")
	}
	command := os.Getenv("VIEWER_PI_COMMAND")
	if command == "" {
		command = "pi"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client, err := New(ctx, ProcessConfig{Command: command, Arguments: []string{"--mode", "rpc", "--no-session"}, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	state, err := client.GetState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionID == "" {
		t.Fatalf("real Pi returned no session id: %#v", state)
	}
	models, err := client.GetAvailableModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real Pi state: sessionID=%s sessionFile=%q model=%v thinking=%q; available models=%d", state.SessionID, state.SessionFile, state.Model != nil, state.ThinkingLevel, len(models))
	for index, model := range models {
		if index == 5 {
			break
		}
		t.Logf("model[%d]=%s/%s context=%d", index, model.Provider, model.ID, model.ContextWindow)
	}
}
