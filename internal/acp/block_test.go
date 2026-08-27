package acp

import (
	"strings"
	"testing"
)

func TestParseBlock(t *testing.T) {
	block := ParseBlock("tool_call", map[string]any{"title": "Read", "status": "completed", "arguments": map[string]any{"path": "a.txt"}})
	if block.Kind != "tool_call" || block.Text != "Read" || !strings.Contains(block.Payload, `"name":"Read"`) {
		t.Fatalf("block=%#v", block)
	}
	text := ParseBlock("agent_message_chunk", map[string]any{"content": map[string]any{"text": "hello"}})
	if text.Kind != "agent_text" || text.Text != "hello" || text.Payload != "{}" {
		t.Fatalf("text block=%#v", text)
	}
	usage := ParseBlock("usage_update", map[string]any{"used": 13942.0, "size": 258400.0})
	if usage.Kind != "token_usage" || !strings.Contains(usage.Payload, `"total_tokens":13942`) || !strings.Contains(usage.Payload, `"model_context_window":258400`) {
		t.Fatalf("usage block=%#v", usage)
	}
}

func TestToolContentIsNormalizedAsInputAndOutput(t *testing.T) {
	content := func(text string) []any {
		return []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": text}}}
	}
	call := ParseBlock("tool_call", map[string]any{
		"title": "terminal: go test ./...", "kind": "execute", "toolCallId": "tc-1", "content": content("$ go test ./..."),
	})
	if call.Text != "terminal: go test ./..." || !strings.Contains(call.Payload, `"input":"$ go test ./..."`) || !strings.Contains(call.Payload, `"kind":"execute"`) {
		t.Fatalf("call=%#v", call)
	}
	result := ParseBlock("tool_call_update", map[string]any{
		"kind": "execute", "status": "completed", "toolCallId": "tc-1", "content": content("terminal result\n- output: ok\n- exit_code: 0"),
	})
	if result.Text != "" || !strings.Contains(result.Payload, `"output":"terminal result\n- output: ok\n- exit_code: 0"`) {
		t.Fatalf("result=%#v", result)
	}
}
