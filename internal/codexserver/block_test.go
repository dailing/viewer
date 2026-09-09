package codexserver

import (
	"strings"
	"testing"

	"viewer/internal/agentdriver"
)

func TestParseBlock(t *testing.T) {
	tests := []struct {
		method string
		data   map[string]any
		kind   string
		text   string
	}{
		{"item/agentMessage/delta", map[string]any{"delta": "answer"}, agentdriver.KindAgentText, "answer"},
		{"item/reasoning/summaryTextDelta", map[string]any{"delta": "thought"}, agentdriver.KindThinking, "thought"},
		{"item/commandExecution/outputDelta", map[string]any{"command": "go test", "delta": "ok"}, agentdriver.KindCommand, "go test"},
		{"turn/diff/updated", map[string]any{"diff": "patch"}, agentdriver.KindFileChange, "patch"},
		{"item/toolCall", map[string]any{"name": "read"}, agentdriver.KindToolCall, "read"},
		{"item/toolResult", map[string]any{"result": "done"}, agentdriver.KindToolResult, "done"},
		{"error", map[string]any{"error": map[string]any{"message": "usage limit", "codexErrorInfo": "usageLimitExceeded"}, "willRetry": false}, agentdriver.KindError, "usage limit"},
		{"error", map[string]any{"error": map[string]any{"message": "Reconnecting... 2/5"}, "willRetry": true}, agentdriver.KindOther, ""},
		{"thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 13942.0}, "total": map[string]any{"totalTokens": 986390.0}, "modelContextWindow": 258400.0}}, agentdriver.KindTokenUsage, ""},
	}
	for _, test := range tests {
		block := ParseBlock(test.method, test.data)
		if block.Kind != test.kind || block.Text != test.text || block.Payload == "" {
			t.Errorf("%s: %#v", test.method, block)
		}
		if test.kind == agentdriver.KindOther && !strings.Contains(block.Payload, test.method) {
			t.Errorf("unknown method missing from payload: %s", block.Payload)
		}
		if test.kind == agentdriver.KindError && !strings.Contains(block.Payload, `"codexErrorInfo":"usageLimitExceeded"`) {
			t.Errorf("error payload missing codexErrorInfo: %s", block.Payload)
		}
		if test.kind == agentdriver.KindTokenUsage && (!strings.Contains(block.Payload, `"total_tokens":13942`) || !strings.Contains(block.Payload, `"model_context_window":258400`)) {
			t.Errorf("usage payload not normalized: %s", block.Payload)
		}
	}
}

func TestTokenUsagePrefersLastOverCumulativeTotal(t *testing.T) {
	// last.totalTokens is the current context fill; total is cumulative and
	// must not leak into the ctx percentage even when hugely larger.
	block := ParseBlock("thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{
		"last":               map[string]any{"totalTokens": 120000.0},
		"total":              map[string]any{"totalTokens": 42334994.0},
		"modelContextWindow": 258400.0,
	}})
	if !strings.Contains(block.Payload, `"total_tokens":120000`) || strings.Contains(block.Payload, "42334994") {
		t.Fatalf("last not preferred: %s", block.Payload)
	}
	// Servers without last fall back to total.
	fallback := ParseBlock("thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{
		"total":              map[string]any{"totalTokens": 5000.0},
		"modelContextWindow": 258400.0,
	}})
	if !strings.Contains(fallback.Payload, `"total_tokens":5000`) {
		t.Fatalf("total fallback missing: %s", fallback.Payload)
	}
}

func TestParseCommandLifecycleCarriesStableActivityID(t *testing.T) {
	started := ParseBlock("item/started", map[string]any{"item": map[string]any{
		"type": "commandExecution", "id": "exec-1", "command": "go test ./...", "cwd": "/repo", "status": "inProgress",
	}})
	if started.Kind != agentdriver.KindCommand || started.Text != "go test ./..." || !strings.Contains(started.Payload, `"activity_id":"exec-1"`) || !strings.Contains(started.Payload, `"cwd":"/repo"`) {
		t.Fatalf("started=%#v", started)
	}
	delta := ParseBlock("item/commandExecution/outputDelta", map[string]any{"itemId": "exec-1", "delta": "ok\n"})
	if delta.Kind != agentdriver.KindCommand || delta.Text != "" || !strings.Contains(delta.Payload, `"activity_id":"exec-1"`) || !strings.Contains(delta.Payload, `"output":"ok\n"`) {
		t.Fatalf("delta=%#v", delta)
	}
	completed := ParseBlock("item/completed", map[string]any{"item": map[string]any{
		"type": "commandExecution", "id": "exec-1", "command": "go test ./...", "status": "completed", "aggregatedOutput": "ok\n", "exitCode": 0.0,
	}})
	if !strings.Contains(completed.Payload, `"output_complete":true`) || !strings.Contains(completed.Payload, `"status":"completed"`) {
		t.Fatalf("completed=%#v", completed)
	}
}
