package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func hindsightBankID(prefix, chatID string) string {
	return fallback(strings.TrimSpace(prefix), "super-workspace") + "::dailing::default::chat::" + chatID
}

func (p *Plugin) recallChatMemories(chatID, query, recentTail string, occurredAt int64) []string {
	config := p.hindsightConfig(p.ctx)
	return recallChatMemories(p.ctx, p.httpClient, config, chatID, query, recentTail, occurredAt)
}

func recallChatMemories(parent context.Context, client *http.Client, config HindsightConfig, chatID, query, recentTail string, occurredAt int64) []string {
	if strings.TrimSpace(config.Endpoint) == "" || strings.TrimSpace(query) == "" {
		return nil
	}
	tail := []rune(recentTail)
	if len(tail) > 450 {
		tail = tail[len(tail)-450:]
	}
	retrieval := "Find earlier chat facts that are useful for the current request. The immediate timeline is included to resolve references such as 'this plan', 'continue', or 'that change'.\n\nCURRENT REQUEST:\n" + strings.TrimSpace(query) + "\n\nIMMEDIATE TIMELINE TAIL:\n" + string(tail)
	body := map[string]any{"query": retrieval, "types": []string{"world", "experience", "observation"}, "budget": "mid", "max_tokens": config.MaxTokens, "query_timestamp": time.UnixMilli(occurredAt).UTC().Format(time.RFC3339Nano)}
	encoded, _ := json.Marshal(body)
	endpoint := strings.TrimRight(config.Endpoint, "/") + "/v1/default/banks/" + url.PathEscape(hindsightBankID(config.BankPrefix, chatID)) + "/memories/recall"
	ctx, cancel := context.WithTimeout(parent, time.Duration(config.TimeoutSeconds)*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "viewer-chat-memory/1")
	if config.Token != "" {
		request.Header.Set("Authorization", "Bearer "+config.Token)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil
	}
	var result map[string]any
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result) != nil {
		return nil
	}
	var raw []any
	for _, key := range []string{"results", "memories", "items"} {
		if values, ok := result[key].([]any); ok {
			raw = values
			break
		}
	}
	snippets := []string{}
	for _, value := range raw {
		item, ok := value.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"text", "content", "memory"} {
			if text, ok := item[key].(string); ok && strings.TrimSpace(text) != "" {
				snippets = append(snippets, strings.TrimSpace(text))
				break
			}
		}
		if len(snippets) >= config.Limit {
			break
		}
	}
	return snippets
}

func (p *Plugin) buildHindsightRecallSection(chatID, query, recentTail string, occurredAt int64) string {
	snippets := p.recallChatMemories(chatID, query, recentTail, occurredAt)
	if len(snippets) == 0 {
		return ""
	}
	lines := []string{"Relevant long-term memories for this request (candidate evidence, verify against recent history):"}
	for _, snippet := range snippets {
		lines = append(lines, fmt.Sprintf("- %s", snippet))
	}
	return strings.Join(lines, "\n")
}
