package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type completionResult struct {
	Content string
	Model   string
}

func chatCompletion(ctx context.Context, client *http.Client, config LLMConfig, messages []map[string]string, jsonMode bool) (completionResult, error) {
	if strings.TrimSpace(config.Endpoint) == "" || strings.TrimSpace(config.Model) == "" {
		return completionResult{}, fmt.Errorf("LLM is not configured: set plugins.viewer-chat.llm.endpoint and model")
	}
	body := map[string]any{"model": config.Model, "messages": messages}
	if jsonMode {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return completionResult{}, err
	}
	endpoint := strings.TrimRight(config.Endpoint, "/")
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return completionResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if config.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+config.APIKey)
	}
	response, err := client.Do(request)
	if err != nil {
		return completionResult{}, err
	}
	defer response.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return completionResult{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return completionResult{}, fmt.Errorf("LLM returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(limited)))
	}
	var envelope struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(limited, &envelope) != nil || len(envelope.Choices) == 0 || strings.TrimSpace(envelope.Choices[0].Message.Content) == "" {
		return completionResult{}, fmt.Errorf("LLM returned a malformed completion")
	}
	model := envelope.Model
	if model == "" {
		model = config.Model
	}
	return completionResult{Content: strings.TrimSpace(envelope.Choices[0].Message.Content), Model: model}, nil
}
