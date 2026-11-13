package pirpc

import (
	"encoding/json"
	"strings"

	"viewer/internal/agentdriver"
)

// ParseBlock normalizes one Pi session event into the shared block contract.
// The original event is still carried in EventFrame.RawJSON; unknown and
// low-level lifecycle events deliberately remain hidden as other blocks.
func ParseBlock(eventType string, data map[string]any) agentdriver.Block {
	kind, text, payload := agentdriver.KindOther, "", map[string]any{"pi_event": eventType}
	switch eventType {
	case "message_update":
		update, _ := data["assistantMessageEvent"].(map[string]any)
		updateType := StringField(update, "type")
		switch updateType {
		case "text_delta":
			kind, text, payload = agentdriver.KindAgentText, StringField(update, "delta"), map[string]any{}
		case "thinking_delta":
			kind, text, payload = agentdriver.KindThinking, StringField(update, "delta"), map[string]any{}
		case "toolcall_end":
			call, _ := update["toolCall"].(map[string]any)
			// bash is rendered as one command lifecycle beginning at
			// tool_execution_start; all other calls can begin here and merge
			// with their execution start by toolCallId.
			if StringField(call, "name") != "bash" {
				kind, payload = agentdriver.KindToolCall, toolCallPayload(call)
				text = StringField(call, "name")
			}
		}
	case "message_end":
		message, _ := data["message"].(map[string]any)
		if StringField(message, "role") == "assistant" {
			stopReason := StringField(message, "stopReason")
			if stopReason == "error" || stopReason == "deferred" {
				kind = agentdriver.KindError
				text = StringField(message, "errorMessage")
				if text == "" && stopReason == "deferred" {
					text = "Pi returned a deferred response; Viewer cannot resume deferred provider handles"
				}
				payload = agentdriver.SelectedPayload(message, "stopReason", "errorMessage", "provider", "model")
			}
		}
	case "auto_retry_end":
		if success, _ := data["success"].(bool); !success {
			kind, text = agentdriver.KindError, StringField(data, "finalError")
			payload = agentdriver.SelectedPayload(data, "success", "attempt", "finalError")
		}
	case "compaction_end":
		if errorText := StringField(data, "errorMessage"); errorText != "" {
			kind, text = agentdriver.KindError, errorText
			payload = agentdriver.SelectedPayload(data, "reason", "errorMessage")
		}
	case "tool_execution_start":
		name := StringField(data, "toolName")
		payload = toolExecutionPayload(data)
		payload["status"] = "in_progress"
		if name == "bash" {
			kind = agentdriver.KindCommand
			text = StringField(payload, "command")
		} else {
			kind = agentdriver.KindToolCall
			text = name
		}
	case "tool_execution_update", "tool_execution_end":
		name := StringField(data, "toolName")
		result, _ := data["partialResult"].(map[string]any)
		if eventType == "tool_execution_end" {
			result, _ = data["result"].(map[string]any)
		}
		payload = toolExecutionPayload(data)
		payload["output"] = toolResultText(result)
		// Pi exposes partialResult as the latest snapshot, not a guaranteed
		// delta. Replace prior output instead of using Codex-style append.
		payload["output_complete"] = true
		if eventType == "tool_execution_end" {
			payload["status"] = "completed"
			if isError, _ := data["isError"].(bool); isError {
				payload["status"] = "error"
			}
			mergeMissing(payload, agentdriver.SelectedPayload(data, "isError", "durationMs"))
		} else {
			payload["status"] = "in_progress"
		}
		if details, ok := result["details"].(map[string]any); ok {
			mergeMissing(payload, agentdriver.SelectedPayload(details, "diff", "patch"))
		}
		if name == "bash" {
			kind = agentdriver.KindCommand
			text = StringField(payload, "command")
		} else {
			kind = agentdriver.KindToolResult
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = []byte(`{"parse_error":"payload encoding failed"}`)
	}
	return agentdriver.Block{Kind: kind, Text: text, Payload: string(encoded)}
}

func toolCallPayload(call map[string]any) map[string]any {
	payload := agentdriver.SelectedPayload(call, "name", "arguments")
	if id := StringField(call, "id"); id != "" {
		payload["tool_call_id"] = id
	}
	arguments, _ := call["arguments"].(map[string]any)
	mergeMissing(payload, toolArgumentPayload(arguments))
	return payload
}

func toolExecutionPayload(data map[string]any) map[string]any {
	payload := map[string]any{
		"name":         StringField(data, "toolName"),
		"tool_call_id": StringField(data, "toolCallId"),
	}
	arguments, _ := data["args"].(map[string]any)
	if len(arguments) > 0 {
		payload["arguments"] = arguments
	}
	mergeMissing(payload, toolArgumentPayload(arguments))
	return payload
}

func toolArgumentPayload(arguments map[string]any) map[string]any {
	payload := map[string]any{}
	if command := StringField(arguments, "command"); command != "" {
		payload["command"] = command
	}
	for _, key := range []string{"path", "filePath", "file_path"} {
		if path := StringField(arguments, key); path != "" {
			payload["path"] = path
			break
		}
	}
	return payload
}

// toolResultText extracts human-readable content without copying base64 image
// payloads into the normalized bus block. Non-text content remains available
// in RawJSON, which is bounded by Viewer's existing frame-size contract.
func toolResultText(result map[string]any) string {
	if result == nil {
		return ""
	}
	switch content := result["content"].(type) {
	case string:
		return content
	case []any:
		parts := make([]string, 0, len(content))
		for _, item := range content {
			entry, _ := item.(map[string]any)
			if StringField(entry, "type") == "text" {
				if text := StringField(entry, "text"); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func mergeMissing(target, source map[string]any) {
	for key, value := range source {
		if _, exists := target[key]; !exists {
			target[key] = value
		}
	}
}

// SessionStatsBlock converts Pi's actual post-run context estimate into the
// shared token_usage payload used by the Turn header's context indicator.
func SessionStatsBlock(stats map[string]any) (agentdriver.Block, bool) {
	usage, _ := stats["contextUsage"].(map[string]any)
	tokens, tokensOK := usage["tokens"].(float64)
	window, windowOK := usage["contextWindow"].(float64)
	if !tokensOK || !windowOK || window <= 0 {
		return agentdriver.Block{}, false
	}
	payload, _ := json.Marshal(map[string]any{
		"total_tokens":         int64(tokens),
		"model_context_window": int64(window),
	})
	return agentdriver.Block{Kind: agentdriver.KindTokenUsage, Payload: string(payload)}, true
}
