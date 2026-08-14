package acp

import (
	"encoding/json"
	"fmt"

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
		kind, text, payload = agentdriver.KindToolCall, readableText(data), selectedPayload(data, "name", "title", "arguments", "status")
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
