package acp

import "viewer/internal/agentdriver"

// ParseTokenUsage reads optional actual-usage extensions on usage_update.
// The ACP used/size fields are context-window fill and are never accounting
// input. Implementations without real per-response usage simply return nil.
func ParseTokenUsage(updateKind string, data map[string]any) *agentdriver.TokenUsage {
	if updateKind != "usage_update" {
		return nil
	}
	if nested, ok := data["usage"].(map[string]any); ok {
		data = nested
	}
	usage := &agentdriver.TokenUsage{
		InputTokens:           agentdriver.Int64Field(data, "prompt_tokens", "promptTokens", "input_tokens", "inputTokens"),
		CachedInputTokens:     agentdriver.Int64Field(data, "cached_input_tokens", "cachedInputTokens", "cached_tokens", "cachedTokens"),
		CacheWriteInputTokens: agentdriver.Int64Field(data, "cache_write_input_tokens", "cacheWriteInputTokens"),
		OutputTokens:          agentdriver.Int64Field(data, "completion_tokens", "completionTokens", "output_tokens", "outputTokens"),
		ReasoningOutputTokens: agentdriver.Int64Field(data, "reasoning_output_tokens", "reasoningOutputTokens"),
		TotalTokens:           agentdriver.Int64Field(data, "total_tokens", "totalTokens"),
	}
	return usage.Finalize()
}
