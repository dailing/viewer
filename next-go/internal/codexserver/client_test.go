package codexserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestProtocolSubset(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(file), "..", "..", "scripts", "mock_codex_server.py")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := New(ctx, ProcessConfig{Command: "python3", Arguments: []string{script}, YOLO: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	updates := make(chan Update, 10)
	client.OnUpdate(func(update Update) { updates <- update })
	thread, err := client.ThreadStart(ctx, "/tmp", "gpt-test")
	if err != nil || thread != "mock-thread" {
		t.Fatalf("thread start: %q %v", thread, err)
	}
	if err := client.ThreadResume(ctx, "restored-thread", "/tmp"); err != nil {
		t.Fatalf("thread resume: %v", err)
	}
	turn, err := client.TurnStart(ctx, thread, "hello", "gpt-test")
	if err != nil || stringValue(turn, "status") != "completed" {
		t.Fatalf("turn: %#v %v", turn, err)
	}
	methods := map[string]bool{}
	for len(updates) > 0 {
		update := <-updates
		methods[update.Method] = true
		var raw map[string]any
		if json.Unmarshal(update.Raw, &raw) != nil || raw["method"] != update.Method {
			t.Errorf("raw update was not preserved for %s: %q", update.Method, update.Raw)
		}
	}
	for _, method := range []string{"item/agentMessage/delta", "item/reasoning/summaryTextDelta", "item/commandExecution/outputDelta", "turn/diff/updated", "thread/tokenUsage/updated", "turn/completed"} {
		if !methods[method] {
			t.Errorf("missing update %s", method)
		}
	}
	if err := client.TurnInterrupt(ctx, thread); err != nil {
		t.Fatal(err)
	}
}
