package frontendlog

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"viewer/sdk/go/busclient"
)

func TestLogWritesStructuredBackendEntry(t *testing.T) {
	var output bytes.Buffer
	plugin := New()
	plugin.logger = slog.New(slog.NewJSONHandler(&output, nil))
	plugin.log(busclient.Frame{
		Value: map[string]any{
			"level": "warn", "component": "chat", "message": "dispatch dropped stale roles",
			"context": map[string]any{"chatId": "chat-1", "droppedRoleIds": []any{"old-role"}},
			"ts":      float64(1234),
		},
		Origin: &busclient.Origin{Plugin: "gateway", Instance: "browser-1"},
	})

	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["level"] != "WARN" || entry["msg"] != "dispatch dropped stale roles" {
		t.Fatalf("unexpected log entry: %#v", entry)
	}
	if entry["source"] != "frontend" || entry["component"] != "chat" || entry["browser_conn"] != "browser-1" {
		t.Fatalf("missing frontend attribution: %#v", entry)
	}
	detail, ok := entry["context"].(map[string]any)
	if !ok || detail["chatId"] != "chat-1" {
		t.Fatalf("missing structured context: %#v", entry)
	}
	if entry["client_ts"] != float64(1234) {
		t.Fatalf("missing client timestamp: %#v", entry)
	}
}

func TestLogRejectsMalformedEventsWithBackendWarning(t *testing.T) {
	for _, value := range []any{
		"not an object",
		map[string]any{"level": "verbose", "message": "bad level"},
		map[string]any{"level": "warn"},
	} {
		var output bytes.Buffer
		plugin := New()
		plugin.logger = slog.New(slog.NewJSONHandler(&output, nil))
		plugin.log(busclient.Frame{Value: value})

		var entry map[string]any
		if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		if entry["level"] != "WARN" || entry["msg"] != "invalid frontend log event" {
			t.Fatalf("value %#v produced %#v", value, entry)
		}
	}
}
