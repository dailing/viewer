package codexserver

import (
	"encoding/json"
	"fmt"
	"strings"

	"viewer/internal/agentdriver"
)

// ParseBlock normalizes one Codex App Server notification into the shared
// agent-driver block contract. Unknown methods are retained as other blocks.
func ParseBlock(method string, data map[string]any) agentdriver.Block {
	kind, text, payload := agentdriver.KindOther, "", map[string]any{"method": method}
	lower := strings.ToLower(method)
	switch {
	case method == "item/agentMessage/delta":
		kind, text, payload = agentdriver.KindAgentText, stringField(data, "delta", "text"), map[string]any{}
	case strings.Contains(lower, "reasoning"):
		kind, text, payload = agentdriver.KindThinking, stringField(data, "delta", "text", "summary"), map[string]any{"method": method}
	case strings.Contains(lower, "commandexecution") || strings.Contains(lower, "/command/"):
		kind, payload = agentdriver.KindCommand, selectedPayload(data, "command", "status", "output")
		if item, ok := data["item"].(map[string]any); ok {
			mergeMissing(payload, selectedPayload(item, "command", "status", "output"))
		}
		if _, ok := payload["output"]; !ok {
			if delta := stringField(data, "delta"); delta != "" {
				payload["output"] = delta
			}
		}
		text = stringField(data, "delta", "output", "command")
	case strings.Contains(lower, "filechange") || strings.Contains(lower, "patch") || method == "turn/diff/updated":
		kind, payload = agentdriver.KindFileChange, selectedPayload(data, "path", "patch", "diff")
		if _, ok := payload["patch"]; !ok {
			if diff, ok := payload["diff"]; ok {
				payload["patch"] = diff
				delete(payload, "diff")
			}
		}
		if first := firstObject(data["changes"]); first != nil {
			mergeMissing(payload, selectedPayload(first, "path", "patch", "diff"))
		}
		text = stringField(data, "diff", "patch", "delta")
	case strings.Contains(lower, "tokenusage"):
		// Codex reports cumulative thread fill under tokenUsage.total plus the
		// window size; normalize both into the shared token_usage payload.
		kind, payload = agentdriver.KindTokenUsage, tokenUsagePayload(data["tokenUsage"])
	case strings.Contains(lower, "toolresult"):
		kind, text, payload = agentdriver.KindToolResult, readableText(data), selectedPayload(data, "name", "arguments", "status", "output", "result")
	case strings.Contains(lower, "toolcall"):
		kind, text, payload = agentdriver.KindToolCall, readableText(data), selectedPayload(data, "name", "arguments", "status")
		if item, ok := data["item"].(map[string]any); ok {
			mergeMissing(payload, selectedPayload(item, "name", "arguments", "status"))
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = []byte(`{"method":"` + method + `"}`)
	}
	return agentdriver.Block{Kind: kind, Text: text, Payload: string(encoded)}
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

func readableText(data map[string]any) string {
	if content, ok := data["content"].(map[string]any); ok {
		if text := stringField(content, "text"); text != "" {
			return text
		}
	}
	if text := stringField(data, "text", "delta"); text != "" {
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

// tokenUsagePayload normalizes a Codex tokenUsage object into the shared
// {total_tokens, model_context_window} shape consumed by the ctx indicator.
func tokenUsagePayload(value any) map[string]any {
	usage, _ := value.(map[string]any)
	payload := map[string]any{}
	if total, ok := usage["total"].(map[string]any); ok {
		if tokens, ok := total["totalTokens"].(float64); ok {
			payload["total_tokens"] = int64(tokens)
		}
	}
	if window, ok := usage["modelContextWindow"].(float64); ok {
		payload["model_context_window"] = int64(window)
	}
	return payload
}
