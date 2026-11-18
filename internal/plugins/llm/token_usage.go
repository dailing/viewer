package llm

import (
	"bytes"
	"encoding/json"

	"viewer/internal/agentdriver"
)

// parseOpenAIUsage normalizes OpenAI-compatible chat-completion usage.
// prompt_tokens already includes cached input; cached/reasoning fields are
// subsets and are never added again.
func parseOpenAIUsage(data map[string]any) *agentdriver.TokenUsage {
	if data == nil {
		return nil
	}
	details, _ := data["prompt_tokens_details"].(map[string]any)
	completionDetails, _ := data["completion_tokens_details"].(map[string]any)
	usage := &agentdriver.TokenUsage{
		InputTokens:           agentdriver.Int64Field(data, "prompt_tokens", "promptTokens"),
		CachedInputTokens:     agentdriver.Int64Field(data, "cached_input_tokens", "cachedInputTokens"),
		CacheWriteInputTokens: agentdriver.Int64Field(data, "cache_write_input_tokens", "cacheWriteInputTokens"),
		OutputTokens:          agentdriver.Int64Field(data, "completion_tokens", "completionTokens"),
		ReasoningOutputTokens: agentdriver.Int64Field(data, "reasoning_output_tokens", "reasoningOutputTokens"),
		TotalTokens:           agentdriver.Int64Field(data, "total_tokens", "totalTokens"),
	}
	if usage.InputTokens == nil {
		usage.InputTokens = agentdriver.Int64Field(data, "input_tokens", "inputTokens")
	}
	if usage.OutputTokens == nil {
		usage.OutputTokens = agentdriver.Int64Field(data, "output_tokens", "outputTokens")
	}
	if usage.CachedInputTokens == nil {
		usage.CachedInputTokens = agentdriver.Int64Field(details, "cached_tokens", "cachedTokens")
	}
	if usage.CachedInputTokens == nil {
		usage.CachedInputTokens = agentdriver.Int64Field(data, "cache_read_input_tokens", "cacheReadInputTokens")
	}
	if usage.CacheWriteInputTokens == nil {
		usage.CacheWriteInputTokens = agentdriver.Int64Field(details, "cache_write_tokens", "cacheWriteTokens")
	}
	if usage.CacheWriteInputTokens == nil {
		usage.CacheWriteInputTokens = agentdriver.Int64Field(data, "cache_creation_input_tokens", "cacheCreationInputTokens")
	}
	if usage.ReasoningOutputTokens == nil {
		usage.ReasoningOutputTokens = agentdriver.Int64Field(completionDetails, "reasoning_tokens", "reasoningTokens")
	}
	return usage.Finalize()
}

// usageFromResponseBytes finds the final usage object in either a JSON body or
// an SSE tail. The capture is bounded, so an upstream without a final usage
// chunk remains explicitly unreported rather than estimated.
func usageFromResponseBytes(data []byte) *agentdriver.TokenUsage {
	search := []byte(`"usage"`)
	for offset := len(data); offset > 0; {
		index := bytes.LastIndex(data[:offset], search)
		if index < 0 {
			return nil
		}
		brace := bytes.IndexByte(data[index+len(search):], '{')
		if brace >= 0 {
			start := index + len(search) + brace
			var value map[string]any
			decoder := json.NewDecoder(bytes.NewReader(data[start:]))
			if decoder.Decode(&value) == nil {
				if usage := parseOpenAIUsage(value); usage != nil {
					return usage
				}
			}
		}
		offset = index
	}
	return nil
}

const responseUsageTailBytes = 128 << 10

type responseUsageCapture struct{ tail []byte }

func (c *responseUsageCapture) Write(data []byte) (int, error) {
	original := len(data)
	if original >= responseUsageTailBytes {
		c.tail = append(c.tail[:0], data[original-responseUsageTailBytes:]...)
		return original, nil
	}
	c.tail = append(c.tail, data...)
	if len(c.tail) > responseUsageTailBytes {
		copy(c.tail, c.tail[len(c.tail)-responseUsageTailBytes:])
		c.tail = c.tail[:responseUsageTailBytes]
	}
	return original, nil
}

func (c *responseUsageCapture) Bytes() []byte { return c.tail }
