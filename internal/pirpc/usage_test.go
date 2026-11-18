package pirpc

import "testing"

func TestParseTokenUsageNormalizesPiCachesOnce(t *testing.T) {
	data := map[string]any{"message": map[string]any{"role": "assistant", "usage": map[string]any{
		"input": 40.0, "cacheRead": 50.0, "cacheWrite": 10.0, "output": 20.0, "reasoning": 5.0, "totalTokens": 120.0,
	}}}
	usage := ParseTokenUsage("message_end", data)
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 100 || usage.CachedInputTokens == nil || *usage.CachedInputTokens != 50 || usage.CacheWriteInputTokens == nil || *usage.CacheWriteInputTokens != 10 || usage.OutputTokens == nil || *usage.OutputTokens != 20 || usage.TotalTokens == nil || *usage.TotalTokens != 120 {
		t.Fatalf("usage=%#v", usage)
	}
	if repeated := ParseTokenUsage("message_update", map[string]any{"usage": data}); repeated != nil {
		t.Fatalf("in-progress usage must not be counted together with message_end: %#v", repeated)
	}
}
