package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	"viewer/internal/acp"
)

type driverEvent struct {
	Provider  string
	SessionID string
	Kind      string
	Raw       json.RawMessage
	Data      map[string]any
	Text      string
}

type acpAgent struct {
	*acp.Client
}

func (a *acpAgent) OnUpdate(callback func(driverEvent)) {
	a.Client.OnUpdate(func(update acp.Update) {
		kind, _ := update.Value["sessionUpdate"].(string)
		if kind == "" {
			kind, _ = update.Value["session_update"].(string)
		}
		if kind == "" {
			kind = "unknown"
		}
		callback(driverEvent{
			Provider: "hermes", SessionID: update.SessionID, Kind: kind,
			Raw: update.Raw, Data: update.Value, Text: updateText(update.Value),
		})
	})
}

func deriveMessageBlock(event *TurnEvent, data map[string]any) (*MessageBlock, error) {
	kind, text := "other", ""
	payload := map[string]any{}
	if event.Provider == "hermes" {
		kind, text, payload = deriveACPBlock(event.Kind, data)
	} else if event.Provider == "codex-app-server" {
		kind, text, payload = deriveCodexBlock(event.Kind, data)
	} else {
		payload["provider"] = event.Provider
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode %s block payload: %w", kind, err)
	}
	return &MessageBlock{
		ID: newID(), EventID: event.ID, ChatID: event.ChatID, TurnID: event.TurnID,
		Kind: kind, Text: text, Payload: string(encoded), OccurredAt: event.OccurredAt,
	}, nil
}

func deriveACPBlock(updateKind string, data map[string]any) (string, string, map[string]any) {
	switch updateKind {
	case "agent_message_chunk":
		return "agent_text", updateText(data), map[string]any{}
	case "agent_thought_chunk":
		return "thinking", contentText(data), map[string]any{}
	case "tool_call", "tool_call_update":
		payload := selectedPayload(data, "name", "title", "arguments", "status")
		if _, ok := payload["name"]; !ok {
			if title, ok := payload["title"]; ok {
				payload["name"] = title
				delete(payload, "title")
			} else if callID := stringField(data, "toolCallId", "tool_call_id"); callID != "" {
				payload["name"] = callID
			}
		}
		return "tool_call", readableText(data), payload
	case "plan":
		return "other", readableText(data), map[string]any{"session_update": updateKind, "entries": data["entries"]}
	default:
		return "other", readableText(data), map[string]any{"session_update": updateKind}
	}
}

func deriveCodexBlock(method string, data map[string]any) (string, string, map[string]any) {
	lower := strings.ToLower(method)
	switch {
	case method == "item/agentMessage/delta":
		return "agent_text", stringField(data, "delta", "text"), map[string]any{}
	case strings.Contains(lower, "reasoning"):
		return "thinking", stringField(data, "delta", "text", "summary"), map[string]any{"method": method}
	case strings.Contains(lower, "commandexecution") || strings.Contains(lower, "/command/"):
		payload := selectedPayload(data, "command", "status", "output")
		if item, ok := data["item"].(map[string]any); ok {
			mergeMissing(payload, selectedPayload(item, "command", "status", "output"))
		}
		if _, ok := payload["output"]; !ok {
			if delta := stringField(data, "delta"); delta != "" {
				payload["output"] = delta
			}
		}
		return "command", stringField(data, "delta", "output", "command"), payload
	case strings.Contains(lower, "filechange") || strings.Contains(lower, "patch") || method == "turn/diff/updated":
		payload := selectedPayload(data, "path", "patch", "diff")
		if _, ok := payload["patch"]; !ok {
			if diff, ok := payload["diff"]; ok {
				payload["patch"] = diff
				delete(payload, "diff")
			}
		}
		if first := firstObject(data["changes"]); first != nil {
			mergeMissing(payload, selectedPayload(first, "path", "patch", "diff"))
		}
		return "file_change", stringField(data, "diff", "patch", "delta"), payload
	case strings.Contains(lower, "toolresult"):
		return "tool_result", readableText(data), selectedPayload(data, "name", "arguments", "status", "output", "result")
	case strings.Contains(lower, "toolcall"):
		payload := selectedPayload(data, "name", "arguments", "status")
		if item, ok := data["item"].(map[string]any); ok {
			mergeMissing(payload, selectedPayload(item, "name", "arguments", "status"))
		}
		return "tool_call", readableText(data), payload
	default:
		return "other", readableText(data), map[string]any{"method": method}
	}
}

func firstObject(value any) map[string]any {
	switch values := value.(type) {
	case []any:
		if len(values) > 0 {
			item, _ := values[0].(map[string]any)
			return item
		}
	case []map[string]any:
		if len(values) > 0 {
			return values[0]
		}
	}
	return nil
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

func mergeMissing(target, source map[string]any) {
	for key, value := range source {
		if _, exists := target[key]; !exists {
			target[key] = value
		}
	}
}
