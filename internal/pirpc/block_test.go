package pirpc

import (
	"encoding/json"
	"strings"
	"testing"

	"viewer/internal/agentdriver"
)

func payloadObject(t *testing.T, block agentdriver.Block) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(block.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestMessageBlocksDoNotDuplicateFinalContent(t *testing.T) {
	delta := ParseBlock("message_update", map[string]any{"assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "hello"}})
	if delta.Kind != agentdriver.KindAgentText || delta.Text != "hello" {
		t.Fatalf("delta=%#v", delta)
	}
	end := ParseBlock("message_update", map[string]any{"assistantMessageEvent": map[string]any{"type": "text_end", "content": "hello"}})
	if end.Kind != agentdriver.KindOther || end.Text != "" {
		t.Fatalf("authoritative text end must not duplicate deltas: %#v", end)
	}
	thinking := ParseBlock("message_update", map[string]any{"assistantMessageEvent": map[string]any{"type": "thinking_delta", "delta": "reason"}})
	if thinking.Kind != agentdriver.KindThinking || thinking.Text != "reason" {
		t.Fatalf("thinking=%#v", thinking)
	}
}

func TestToolBlocksUseExistingLifecycleContracts(t *testing.T) {
	call := ParseBlock("message_update", map[string]any{"assistantMessageEvent": map[string]any{
		"type":     "toolcall_end",
		"toolCall": map[string]any{"id": "call-1", "name": "read", "arguments": map[string]any{"path": "/tmp/a"}},
	}})
	if call.Kind != agentdriver.KindToolCall || payloadObject(t, call)["tool_call_id"] != "call-1" || payloadObject(t, call)["path"] != "/tmp/a" {
		t.Fatalf("call=%#v payload=%s", call, call.Payload)
	}
	bashModelCall := ParseBlock("message_update", map[string]any{"assistantMessageEvent": map[string]any{
		"type": "toolcall_end", "toolCall": map[string]any{"id": "bash-1", "name": "bash", "arguments": map[string]any{}},
	}})
	if bashModelCall.Kind != agentdriver.KindOther {
		t.Fatalf("bash model call would duplicate command lifecycle: %#v", bashModelCall)
	}
	start := ParseBlock("tool_execution_start", map[string]any{"toolCallId": "bash-1", "toolName": "bash", "args": map[string]any{"command": "pwd"}})
	if start.Kind != agentdriver.KindCommand || start.Text != "pwd" || payloadObject(t, start)["tool_call_id"] != "bash-1" {
		t.Fatalf("command start=%#v payload=%s", start, start.Payload)
	}
	update := ParseBlock("tool_execution_update", map[string]any{
		"toolCallId": "bash-1", "toolName": "bash", "args": map[string]any{"command": "pwd"},
		"partialResult": map[string]any{"content": []any{map[string]any{"type": "text", "text": "/tmp"}}},
	})
	if update.Kind != agentdriver.KindCommand || payloadObject(t, update)["output"] != "/tmp" || payloadObject(t, update)["output_complete"] != true {
		t.Fatalf("command update=%#v payload=%s", update, update.Payload)
	}
	end := ParseBlock("tool_execution_end", map[string]any{
		"toolCallId": "call-1", "toolName": "read", "args": map[string]any{"path": "/tmp/a"},
		"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "contents"}}}, "isError": false, "durationMs": 8,
	})
	if end.Kind != agentdriver.KindToolResult || payloadObject(t, end)["output"] != "contents" || payloadObject(t, end)["status"] != "completed" {
		t.Fatalf("tool result=%#v payload=%s", end, end.Payload)
	}
}

func TestTerminalErrorsAndContextUsage(t *testing.T) {
	providerError := ParseBlock("message_end", map[string]any{"message": map[string]any{"role": "assistant", "stopReason": "error", "errorMessage": "boom"}})
	if providerError.Kind != agentdriver.KindError || providerError.Text != "boom" {
		t.Fatalf("provider error=%#v", providerError)
	}
	deferred := ParseBlock("message_end", map[string]any{"message": map[string]any{"role": "assistant", "stopReason": "deferred"}})
	if deferred.Kind != agentdriver.KindError || !strings.Contains(deferred.Text, "deferred") {
		t.Fatalf("deferred=%#v", deferred)
	}
	retry := ParseBlock("auto_retry_end", map[string]any{"success": false, "finalError": "exhausted"})
	if retry.Kind != agentdriver.KindError || retry.Text != "exhausted" {
		t.Fatalf("retry=%#v", retry)
	}
	block, ok := SessionStatsBlock(map[string]any{"contextUsage": map[string]any{"tokens": float64(100), "contextWindow": float64(1000)}})
	payload := payloadObject(t, block)
	if !ok || block.Kind != agentdriver.KindTokenUsage || payload["total_tokens"] != float64(100) || payload["model_context_window"] != float64(1000) {
		t.Fatalf("stats=%#v payload=%s ok=%v", block, block.Payload, ok)
	}
}
