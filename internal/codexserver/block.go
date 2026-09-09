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
	item, _ := data["item"].(map[string]any)
	itemType := strings.ToLower(stringField(item, "type"))
	switch {
	case method == "error" && !boolField(data, "willRetry"):
		// Terminal codex errors (usage limit, overload, stream failure)
		// precede a failed turn/completed and trigger chat failover; surface
		// them as visible error rows so a silent provider swap is explicable.
		// Transient reconnects (willRetry true) stay hidden as other blocks.
		kind = agentdriver.KindError
		if errObj, ok := data["error"].(map[string]any); ok {
			text = stringField(errObj, "message")
			mergeMissing(payload, selectedPayload(errObj, "codexErrorInfo"))
		}
		payload["willRetry"] = false
	case method == "item/agentMessage/delta":
		kind, text, payload = agentdriver.KindAgentText, stringField(data, "delta", "text"), map[string]any{}
	case strings.Contains(lower, "reasoning"):
		kind, text, payload = agentdriver.KindThinking, stringField(data, "delta", "text", "summary"), map[string]any{"method": method}
	case strings.Contains(lower, "commandexecution") || strings.Contains(lower, "/command/") || itemType == "commandexecution":
		// A Codex command is reported as item/started, zero or more outputDelta
		// notifications, and item/completed. Preserve the item id on every
		// frame so chat can append the deltas into one visible command block.
		kind, payload = agentdriver.KindCommand, selectedPayload(data, "command", "status", "output", "cwd", "exitCode", "durationMs")
		mergeMissing(payload, selectedPayload(item, "command", "status", "output", "cwd", "exitCode", "durationMs"))
		if aggregate := stringField(item, "aggregatedOutput"); aggregate != "" {
			payload["output"] = aggregate
			payload["output_complete"] = true
		} else if delta := stringField(data, "delta"); delta != "" {
			payload["output"] = delta
		}
		activityID := stringField(data, "itemId")
		if activityID == "" {
			activityID = stringField(item, "id")
		}
		if activityID != "" {
			payload["activity_id"] = activityID
		}
		// Text is the stable one-line label. Output belongs only in payload;
		// otherwise every stdout delta becomes both a title and a detail body.
		text = stringField(data, "command")
		if text == "" {
			text = stringField(item, "command")
		}
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
		// Codex reports per-request fill under tokenUsage.last and cumulative
		// thread usage under tokenUsage.total; the ctx indicator wants the
		// current fill, so prefer last and normalize into the shared payload.
		kind, payload = agentdriver.KindTokenUsage, tokenUsagePayload(data["tokenUsage"])
	case strings.Contains(lower, "toolresult"):
		kind, text, payload = agentdriver.KindToolResult, readableText(data), selectedPayload(data, "name", "arguments", "status", "output", "result")
	case strings.Contains(lower, "toolcall"):
		kind, text, payload = agentdriver.KindToolCall, readableText(data), selectedPayload(data, "name", "arguments", "status")
		if item, ok := data["item"].(map[string]any); ok {
			mergeMissing(payload, selectedPayload(item, "name", "arguments", "status"))
		}
		// The item id lets the chat plugin merge status updates for the same
		// call into one block.
		if callID := stringField(data, "toolCallId", "tool_call_id", "itemId"); callID != "" {
			payload["tool_call_id"] = callID
		} else if item, ok := data["item"].(map[string]any); ok {
			if callID := stringField(item, "id"); callID != "" {
				payload["tool_call_id"] = callID
			}
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

func boolField(data map[string]any, key string) bool {
	value, _ := data[key].(bool)
	return value
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
// total_tokens is the current context fill: tokenUsage.last covers the most
// recent request, while tokenUsage.total is cumulative thread usage that
// grows monotonically across compaction and must not drive the percentage.
func tokenUsagePayload(value any) map[string]any {
	usage, _ := value.(map[string]any)
	payload := map[string]any{}
	last, _ := usage["last"].(map[string]any)
	total, _ := usage["total"].(map[string]any)
	// Older servers may omit last; fall back to total rather than nothing.
	for _, bucket := range []map[string]any{last, total} {
		if tokens, ok := bucket["totalTokens"].(float64); ok {
			payload["total_tokens"] = int64(tokens)
			break
		}
	}
	if window, ok := usage["modelContextWindow"].(float64); ok {
		payload["model_context_window"] = int64(window)
	}
	return payload
}
