package acp

import "testing"

func TestParseTokenUsageRequiresActualUsage(t *testing.T) {
	if usage := ParseTokenUsage("usage_update", map[string]any{"used": 13942.0, "size": 258400.0}); usage != nil {
		t.Fatalf("context fill must not become consumption: %#v", usage)
	}
	usage := ParseTokenUsage("usage_update", map[string]any{"usage": map[string]any{"prompt_tokens": 100.0, "completion_tokens": 20.0, "cached_tokens": 60.0}})
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 100 || usage.TotalTokens == nil || *usage.TotalTokens != 120 || usage.CachedInputTokens == nil || *usage.CachedInputTokens != 60 {
		t.Fatalf("usage=%#v", usage)
	}
}
