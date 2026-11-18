package codexserver

import (
	"strings"

	"viewer/internal/agentdriver"
)

// ParseTokenUsage extracts Codex's actual per-request usage. tokenUsage.total
// is cumulative thread history and is deliberately not used for accounting.
func ParseTokenUsage(method string, data map[string]any) *agentdriver.TokenUsage {
	if !strings.Contains(strings.ToLower(method), "tokenusage") {
		return nil
	}
	root, _ := data["tokenUsage"].(map[string]any)
	last, _ := root["last"].(map[string]any)
	if last == nil {
		return nil
	}
	usage := &agentdriver.TokenUsage{
		InputTokens:           agentdriver.Int64Field(last, "inputTokens", "input_tokens"),
		CachedInputTokens:     agentdriver.Int64Field(last, "cachedInputTokens", "cached_input_tokens"),
		CacheWriteInputTokens: agentdriver.Int64Field(last, "cacheWriteInputTokens", "cache_write_input_tokens"),
		OutputTokens:          agentdriver.Int64Field(last, "outputTokens", "output_tokens"),
		ReasoningOutputTokens: agentdriver.Int64Field(last, "reasoningOutputTokens", "reasoning_output_tokens"),
		TotalTokens:           agentdriver.Int64Field(last, "totalTokens", "total_tokens"),
	}
	return usage.Finalize()
}
