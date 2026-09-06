package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"viewer/internal/agentdriver"
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

// ---------------------------------------------------------------------------
// Retain: every visible message (user input + role final replies) lands in
// the chat's Hindsight bank, so the recall layer covers all roles/agents/
// providers uniformly — agent-side memory (e.g. Hermes's own) only sees one
// agent's sessions. SQLite stays the source of truth; the bank is a derived,
// rebuildable index, so retain failures log and never fail the dispatch.
// ---------------------------------------------------------------------------

// retainChatMessage stores one item in the chat's bank. Extraction is
// synchronous server-side (async:true retains never land on the local
// single-worker deployment), so this blocks for several seconds — callers
// must run it off the dispatch path.
func retainChatMessage(parent context.Context, client *http.Client, config HindsightConfig, chatID string, item map[string]any) error {
	body, _ := json.Marshal(map[string]any{"items": []any{item}, "async": false})
	endpoint := strings.TrimRight(config.Endpoint, "/") + "/v1/default/banks/" + url.PathEscape(hindsightBankID(config.BankPrefix, chatID)) + "/memories"
	timeout := config.RetainTimeoutSeconds
	if timeout <= 0 {
		timeout = 30
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeout)*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "viewer-chat-memory/1")
	if config.Token != "" {
		request.Header.Set("Authorization", "Bearer "+config.Token)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("hindsight retain: status %d: %s", response.StatusCode, strings.TrimSpace(string(detail)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return nil
}

// ensureBankConfig disables auto-consolidation on the chat bank, once per
// process per bank (consolidation prompts can stall the single-worker
// daemon; retain/extraction/recall are unaffected). Best-effort.
func (p *Plugin) ensureBankConfig(config HindsightConfig, bank string) {
	p.mu.Lock()
	if p.patchedBanks[bank] {
		p.mu.Unlock()
		return
	}
	p.patchedBanks[bank] = true
	p.mu.Unlock()
	body, _ := json.Marshal(map[string]any{"updates": map[string]any{"enable_auto_consolidation": false}})
	endpoint := strings.TrimRight(config.Endpoint, "/") + "/v1/default/banks/" + url.PathEscape(bank) + "/config"
	ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	if config.Token != "" {
		request.Header.Set("Authorization", "Bearer "+config.Token)
	}
	response, err := p.httpClient.Do(request)
	if err != nil {
		slog.Warn("chat memory bank config patch failed", "bank", bank, "error", err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		slog.Warn("chat memory bank config patch rejected", "bank", bank, "status", response.StatusCode)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
}

// retainVisibleMessage asynchronously retains one visible chat message. The
// config lookup rides the goroutine too, so the dispatch path adds zero
// latency.
func (p *Plugin) retainVisibleMessage(message *Message, agent, provider string) {
	if message == nil || strings.TrimSpace(message.Text) == "" {
		return
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		config := p.hindsightConfig(p.ctx)
		if !config.RetainEnabled || strings.TrimSpace(config.Endpoint) == "" {
			return
		}
		speaker := "User"
		if message.Role == "assistant" {
			speaker = fallback(message.RoleName, "Agent")
		}
		item := map[string]any{
			"content":   speaker + ": " + message.Text,
			"timestamp": time.UnixMilli(message.CreatedAt).UTC().Format(time.RFC3339Nano),
			"context":   "viewer super-workspace chat conversation",
			"metadata": map[string]string{
				"message_id": message.ID, "turn_id": message.TurnID, "chat_id": message.ChatID,
				"role": message.Role, "role_id": message.RoleID, "agent": agent, "provider": provider,
			},
		}
		bank := hindsightBankID(config.BankPrefix, message.ChatID)
		p.ensureBankConfig(config, bank)
		if err := retainChatMessage(p.ctx, p.httpClient, config, message.ChatID, item); err != nil {
			slog.Warn("chat memory retain failed", "chat_id", message.ChatID, "message_id", message.ID, "error", err)
		}
	}()
}

// retainTurnMessages retains every visible assistant message a turn produced
// (the triggering user message was already retained at dispatch time).
// Cancelled turns retain their partial content — it is real conversation.
func (p *Plugin) retainTurnMessages(turnID string, target agentdriver.Target) {
	messages, err := p.store.turnMessages(turnID)
	if err != nil {
		slog.Warn("chat memory retain: turn messages lookup failed", "turn_id", turnID, "error", err)
		return
	}
	for index := range messages {
		if messages[index].Role != "assistant" {
			continue
		}
		p.retainVisibleMessage(&messages[index], target.Agent, target.Provider)
	}
}
