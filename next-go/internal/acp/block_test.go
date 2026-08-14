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
}
