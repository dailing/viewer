package codexserver

import "testing"

func TestParseTokenUsageUsesActualLastRequest(t *testing.T) {
	usage := ParseTokenUsage("thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{
		"last":  map[string]any{"inputTokens": 100.0, "cachedInputTokens": 60.0, "outputTokens": 20.0, "reasoningOutputTokens": 5.0, "totalTokens": 120.0},
		"total": map[string]any{"inputTokens": 9000.0, "outputTokens": 1000.0, "totalTokens": 10000.0},
	}})
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 100 || usage.CachedInputTokens == nil || *usage.CachedInputTokens != 60 || usage.OutputTokens == nil || *usage.OutputTokens != 20 || usage.TotalTokens == nil || *usage.TotalTokens != 120 {
		t.Fatalf("usage=%#v", usage)
	}
}

func TestParseTokenUsageIgnoresContextOnlyUpdate(t *testing.T) {
	if usage := ParseTokenUsage("thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{"total": map[string]any{"totalTokens": 10000.0}}}); usage != nil {
		t.Fatalf("cumulative-only usage must not become per-request accounting: %#v", usage)
	}
}
