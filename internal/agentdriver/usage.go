package agentdriver

import (
	"strconv"
)

// TokenUsage is the provider-neutral accounting for one actual model response.
// InputTokens includes cached and cache-write input. CachedInputTokens,
// CacheWriteInputTokens and ReasoningOutputTokens are subsets, never addends.
// Nil means the provider did not report that field; it does not mean zero.
type TokenUsage struct {
	InputTokens           *int64 `json:"input_tokens,omitempty"`
	CachedInputTokens     *int64 `json:"cached_input_tokens,omitempty"`
	CacheWriteInputTokens *int64 `json:"cache_write_input_tokens,omitempty"`
	OutputTokens          *int64 `json:"output_tokens,omitempty"`
	ReasoningOutputTokens *int64 `json:"reasoning_output_tokens,omitempty"`
	TotalTokens           *int64 `json:"total_tokens,omitempty"`
}

func (u *TokenUsage) Finalize() *TokenUsage {
	if u == nil {
		return nil
	}
	if u.TotalTokens == nil && u.InputTokens != nil && u.OutputTokens != nil {
		u.TotalTokens = Int64(*u.InputTokens + *u.OutputTokens)
	}
	if u.InputTokens == nil && u.CachedInputTokens == nil && u.CacheWriteInputTokens == nil && u.OutputTokens == nil && u.ReasoningOutputTokens == nil && u.TotalTokens == nil {
		return nil
	}
	return u
}

func Int64(value int64) *int64 { return &value }

func Int64Field(data map[string]any, keys ...string) *int64 {
	for _, key := range keys {
		value, exists := data[key]
		if !exists {
			continue
		}
		switch typed := value.(type) {
		case float64:
			converted := int64(typed)
			return &converted
		case float32:
			converted := int64(typed)
			return &converted
		case int:
			converted := int64(typed)
			return &converted
		case int64:
			converted := typed
			return &converted
		case int32:
			converted := int64(typed)
			return &converted
		case string:
			if parsed, err := strconv.ParseInt(typed, 10, 64); err == nil {
				return &parsed
			}
		}
	}
	return nil
}

func SumInt64(values ...*int64) *int64 {
	var sum int64
	found := false
	for _, value := range values {
		if value == nil {
			continue
		}
		found = true
		sum += *value
	}
	if !found {
		return nil
	}
	return &sum
}
