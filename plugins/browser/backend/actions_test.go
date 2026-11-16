package main

import (
	"testing"
	"time"
)

func TestParseWaitPayload(t *testing.T) {
	tests := []struct {
		name      string
		payload   map[string]any
		predicate string
		value     string
		timeout   time.Duration
	}{
		{name: "fixed default", payload: map[string]any{}, timeout: time.Second},
		{name: "text", payload: map[string]any{"text": "Ready"}, predicate: "text", value: "Ready", timeout: 10 * time.Second},
		{name: "url", payload: map[string]any{"url_contains": "/done"}, predicate: "url_contains", value: "/done", timeout: 10 * time.Second},
		{name: "ready", payload: map[string]any{"ready_state": "complete"}, predicate: "ready_state", value: "complete", timeout: 10 * time.Second},
		{name: "bounded", payload: map[string]any{"timeout_ms": float64(40000)}, timeout: 30 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, err := parseWaitPayload(test.payload)
			if err != nil {
				t.Fatal(err)
			}
			if options.predicate != test.predicate || options.value != test.value || options.timeout != test.timeout {
				t.Fatalf("options = %+v", options)
			}
		})
	}
}

func TestParseWaitPayloadRejectsInvalidOptions(t *testing.T) {
	for _, payload := range []map[string]any{
		{"text": "ready", "url_contains": "ready"},
		{"ready_state": "sometimes"},
		{"text": "   "},
	} {
		if _, err := parseWaitPayload(payload); err == nil {
			t.Fatalf("payload unexpectedly valid: %v", payload)
		}
	}
}
