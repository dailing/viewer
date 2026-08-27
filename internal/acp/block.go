package acp

import (
	"encoding/json"
	"fmt"
	"strings"

	"viewer/internal/agentdriver"
)

// ParseBlock converts one ACP session update into the shared normalized block.
func ParseBlock(updateKind string, data map[string]any) agentdriver.Block {
	kind, text, payload := agentdriver.KindOther, "", map[string]any{}
	switch updateKind {
	case "agent_message_chunk":
		kind, text = agentdriver.KindAgentText, updateText(data)
	case "agent_thought_chunk":
		kind, text = agentdriver.KindThinking, contentText(data)
	case "tool_call", "tool_call_update":
		kind, payload = agentdriver.KindToolCall, selectedPayload(data, "name", "title", "arguments", "status", "kind", "locations")
		content := contentListText(data)
		if updateKind == "tool_call" {
			text = stringField(data, "title", "name")
			if content != "" {
				payload["input"] = content
			}
		} else if content != "" {
			// ACP completion details (shell stdout/exit code, edit summaries,
			// read previews) live in nested content[]. Keep them as output
			// instead of losing them or replacing the stable call title.
			payload["output"] = content
		}
		// The lifecycle id lets the chat plugin merge pending → in_progress →
		// completed updates into a single block.
		if callID := stringField(data, "toolCallId", "tool_call_id"); callID != "" {
			payload["tool_call_id"] = callID
		}
		if _, ok := payload["name"]; !ok {
			if title, ok := payload["title"]; ok {
				payload["name"] = title
				delete(payload, "title")
			} else if callID := stringField(data, "toolCallId", "tool_call_id"); callID != "" {
				payload["name"] = callID
			}
		}
	case "plan":
		text, payload = readableText(data), map[string]any{"session_update": updateKind, "entries": data["entries"]}
	case "usage_update":
		// ACP reports context fill as used/size; normalize to the shared
		// token_usage payload shape (same keys as the codex driver).
		kind, payload = agentdriver.KindTokenUsage, map[string]any{}
		if used, ok := data["used"].(float64); ok {
			payload["total_tokens"] = int64(used)
		}
		if size, ok := data["size"].(float64); ok {
			payload["model_context_window"] = int64(size)
		}
	default:
		text, payload = readableText(data), map[string]any{"session_update": updateKind}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = []byte(`{"parse_error":"payload encoding failed"}`)
	}
	return agentdriver.Block{Kind: kind, Text: text, Payload: string(encoded)}
}

func updateText(data map[string]any) string {
	if content, ok := data["content"].(map[string]any); ok {
		return stringField(content, "text")
	}
	return stringField(data, "text", "delta")
}

func contentText(data map[string]any) string {
	if content, ok := data["content"].(map[string]any); ok {
		return stringField(content, "text")
	}
	return stringField(data, "text", "delta")
}

// contentListText flattens ACP's content blocks. Hermes uses
// content: [{content: {type:"text", text:"..."}, type:"content"}]
// for both tool inputs and results, while some ACP implementations use a
// direct object. Non-text content remains represented by the normalized
// arguments/locations fields.
func contentListText(data map[string]any) string {
	if text := contentText(data); text != "" {
		return text
	}
	values, _ := data["content"].([]any)
	parts := make([]string, 0, len(values))
	for _, value := range values {
		entry, _ := value.(map[string]any)
		if nested, ok := entry["content"].(map[string]any); ok {
			entry = nested
		}
		if text := stringField(entry, "text"); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func readableText(data map[string]any) string {
	if text := contentText(data); text != "" {
		return text
	}
	for _, key := range []string{"output", "result", "rawOutput", "title", "name", "command"} {
		if text := stringify(data[key]); text != "" {
			return text
		}
	}
	return ""
}

func stringField(data map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := data[key].(string); ok {
			return text
		}
	}
	return ""
}

func stringify(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case nil:
		return ""
	default:
		encoded, err := json.Marshal(value)
		if err == nil {
			return string(encoded)
		}
		return fmt.Sprint(value)
	}
}

func selectedPayload(data map[string]any, keys ...string) map[string]any {
	payload := map[string]any{}
	for _, key := range keys {
		if value, ok := data[key]; ok && value != nil && stringify(value) != "" {
			payload[key] = value
		}
	}
	return payload
}
