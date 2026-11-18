package pirpc

import "viewer/internal/agentdriver"

// ParseTokenUsage extracts Pi's per-assistant-response usage. Pi reports
// uncached input, cacheRead and cacheWrite separately, while Viewer's common
// contract stores total input with cache fields as non-additive subsets.
func ParseTokenUsage(eventType string, data map[string]any) *agentdriver.TokenUsage {
	// message_update can repeat the in-progress response usage; message_end is
	// the single authoritative accounting point for that model response.
	if eventType != "message_end" {
		return nil
	}
	message, _ := data["message"].(map[string]any)
	if agentdriver.StringField(message, "role") != "assistant" {
		return nil
	}
	usageData, _ := message["usage"].(map[string]any)
	if usageData == nil {
		return nil
	}
	uncached := agentdriver.Int64Field(usageData, "input")
	cacheRead := agentdriver.Int64Field(usageData, "cacheRead", "cache_read")
	cacheWrite := agentdriver.Int64Field(usageData, "cacheWrite", "cache_write")
	usage := &agentdriver.TokenUsage{
		InputTokens:           agentdriver.SumInt64(uncached, cacheRead, cacheWrite),
		CachedInputTokens:     cacheRead,
		CacheWriteInputTokens: cacheWrite,
		OutputTokens:          agentdriver.Int64Field(usageData, "output"),
		ReasoningOutputTokens: agentdriver.Int64Field(usageData, "reasoning"),
		TotalTokens:           agentdriver.Int64Field(usageData, "totalTokens", "total_tokens"),
	}
	return usage.Finalize()
}
