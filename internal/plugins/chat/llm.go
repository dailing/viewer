package chat

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"viewer/sdk/go/busclient"
)

// completionResult mirrors the global llm plugin's reply payload.
type completionResult struct {
	Content string `json:"content"`
	Model   string `json:"model"`
}

// llmCompleter is the plugin's path to the global llm forwarding plugin
// (framework: global LLM layer); injectable in tests.
type llmCompleter func(ctx context.Context, messages []map[string]string, jsonMode bool, timeoutSeconds int) (completionResult, error)

// llmRouteCompleter additionally selects a model and per-request options for
// the latency-sensitive Auto Route call. Other completions keep the simpler
// llmCompleter contract and continue following the global active model.
type llmRouteCompleter func(ctx context.Context, messages []map[string]string, jsonMode bool, timeoutSeconds int, model string, extraBody map[string]any) (completionResult, error)

// llmCompleteViaBus calls the global llm plugin over the bus. The RPC
// deadline exceeds the LLM-side timeout so a slow completion surfaces the
// llm plugin's own error instead of an opaque bus timeout.
func (p *Plugin) llmCompleteViaBus(ctx context.Context, messages []map[string]string, jsonMode bool, timeoutSeconds int) (completionResult, error) {
	return p.llmCompleteRouteViaBus(ctx, messages, jsonMode, timeoutSeconds, "", nil)
}

func (p *Plugin) llmCompleteRouteViaBus(ctx context.Context, messages []map[string]string, jsonMode bool, timeoutSeconds int, model string, extraBody map[string]any) (completionResult, error) {
	if p.client == nil {
		return completionResult{}, errors.New("bus unavailable")
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	payload := map[string]any{
		"messages": messages, "json_mode": jsonMode, "timeout_seconds": timeoutSeconds,
	}
	if model != "" {
		payload["model"] = model
	}
	if extraBody != nil {
		payload["extra_body"] = extraBody
	}
	value, err := p.client.Request(ctx, "llm:_:complete", payload, time.Duration(timeoutSeconds+15)*time.Second)
	if err != nil {
		return completionResult{}, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return completionResult{}, err
	}
	var result completionResult
	if err := json.Unmarshal(encoded, &result); err != nil || result.Content == "" {
		return completionResult{}, errors.New("malformed llm reply")
	}
	return result, nil
}

// llmNotConfigured reports whether err is the llm plugin's not_configured
// RPC error, so callers can surface a "configure the model first" message.
func llmNotConfigured(err error) bool {
	var rpcErr *busclient.RPCError
	return errors.As(err, &rpcErr) && rpcErr.Code == "not_configured"
}
