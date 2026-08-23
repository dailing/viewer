// Package llm implements the global LLM forwarding plugin: a single
// OpenAI-compatible chat-completions endpoint behind `llm:_:complete` so
// plugins never know which model/endpoint serves them. The active config
// lives in config-store under `plugins.llm.active` and is re-read on every
// call, so edits in the LLM pane take effect immediately.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

var Manifest = busclient.Manifest{
	ID: "llm", Version: "0.1.0",
	Slots: map[string]any{
		"llm:_:complete": map[string]any{"summary": "OpenAI-compatible chat completion; RPC {messages, json_mode?, timeout_seconds?, extra_body?} -> {content, model}; extra_body is merged verbatim into the request body (endpoint-specific)"},
	},
	Emits: map[string]any{},
}

const (
	configNamespace = "plugins.llm"
	// legacyNamespace and its keys hold the pre-global chat-owned LLM config;
	// migrated once on start when the new namespace is still empty.
	legacyNamespace  = "plugins.viewer-chat"
	defaultTimeout   = 60
	rpcBudget        = 5 * time.Second
	maxResponseBytes = 1 << 20
)

// Config is one active LLM endpoint configuration (`plugins.llm.active`).
type Config struct {
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"key"`
	Model    string `json:"model"`
	// TimeoutSeconds bounds one completion call; <=0 means the default (60s).
	// Local servers with few parallel slots queue under load, so the budget
	// must cover queueing, not just generation.
	TimeoutSeconds int `json:"timeout_seconds"`
}

// CompletionResult is the RPC reply payload.
type CompletionResult struct {
	Content string `json:"content"`
	Model   string `json:"model"`
}

type Plugin struct {
	client     *busclient.Client
	httpClient *http.Client
}

func New() *Plugin {
	// No client-level Timeout: callers bound their own contexts (per-request
	// timeout_seconds or the active config's), like chat's llm.go did.
	return &Plugin{httpClient: &http.Client{}}
}

func (p *Plugin) Start(ctx context.Context, kernelWS string, managed bool) error {
	p.client = busclient.New(kernelWS, Manifest, busclient.WithManaged(managed))
	if _, err := p.client.Subscribe("llm:_:complete", p.handleComplete); err != nil {
		return fmt.Errorf("subscribe llm:_:complete: %w", err)
	}
	if err := p.client.Connect(ctx); err != nil {
		return fmt.Errorf("connect llm plugin: %w", err)
	}
	if err := p.migrateLegacyConfig(ctx); err != nil {
		return fmt.Errorf("migrate legacy llm config: %w", err)
	}
	return nil
}

func (p *Plugin) Close(context.Context) error {
	if p.client != nil {
		return p.client.Close()
	}
	return nil
}

// migrateLegacyConfig copies the retired chat-owned LLM config
// (plugins.viewer-chat.llm / llm_profiles) into plugins.llm (active /
// profiles) exactly once: keys that already exist in the new namespace are
// never overwritten.
func (p *Plugin) migrateLegacyConfig(ctx context.Context) error {
	return migrateLegacy(func(namespace, key string) (json.RawMessage, bool, error) {
		value, err := p.client.Request(ctx, "config:_:get", map[string]any{"plugin": namespace, "key": key}, rpcBudget)
		if err != nil {
			return nil, false, fmt.Errorf("read %s.%s: %w", namespace, key, err)
		}
		if value == nil {
			return nil, false, nil
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, false, err
		}
		if string(encoded) == "null" || string(encoded) == "{}" {
			return nil, false, nil
		}
		return encoded, true, nil
	}, func(namespace, key string, value json.RawMessage) error {
		var decoded any
		if err := json.Unmarshal(value, &decoded); err != nil {
			return err
		}
		_, err := p.client.Request(ctx, "config:_:set", map[string]any{"plugin": namespace, "key": key, "value": decoded}, rpcBudget)
		return err
	})
}

// migrateLegacy is the bus-free core of the one-time config move, factored
// out for tests.
func migrateLegacy(get func(namespace, key string) (json.RawMessage, bool, error), set func(namespace, key string, value json.RawMessage) error) error {
	for _, pair := range [][2]string{{"llm", "active"}, {"llm_profiles", "profiles"}} {
		legacyKey, newKey := pair[0], pair[1]
		if _, exists, err := get(configNamespace, newKey); err != nil {
			return err
		} else if exists {
			continue
		}
		value, exists, err := get(legacyNamespace, legacyKey)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if err := set(configNamespace, newKey, value); err != nil {
			return fmt.Errorf("write %s.%s: %w", configNamespace, newKey, err)
		}
	}
	return nil
}

// activeConfig re-reads the active endpoint config on every call.
func (p *Plugin) activeConfig(ctx context.Context) (Config, error) {
	var result Config
	value, err := p.client.Request(ctx, "config:_:get", map[string]any{"plugin": configNamespace, "key": "active"}, rpcBudget)
	if err != nil {
		return result, fmt.Errorf("read llm config: %w", err)
	}
	if value != nil {
		encoded, _ := json.Marshal(value)
		_ = json.Unmarshal(encoded, &result)
	}
	return result, nil
}

type completionRequest struct {
	Messages       []map[string]string `json:"messages"`
	JSONMode       bool                `json:"json_mode"`
	TimeoutSeconds int                 `json:"timeout_seconds"`
	// ExtraBody is merged verbatim into the outbound request body (e.g.
	// {"chat_template_kwargs":{"enable_thinking":false}}). Endpoint-specific;
	// strict servers may reject unknown fields with HTTP 400.
	ExtraBody map[string]any `json:"extra_body"`
}

func (p *Plugin) handleComplete(frame busclient.Frame) {
	raw, ok := pluginrpc.Object(frame)
	if !ok {
		_ = pluginrpc.RespondError(p.client, frame, "bad_request", "payload must be an object")
		return
	}
	var request completionRequest
	encoded, _ := json.Marshal(raw)
	if err := json.Unmarshal(encoded, &request); err != nil || len(request.Messages) == 0 {
		_ = pluginrpc.RespondError(p.client, frame, "bad_request", "messages must be a non-empty array of {role, content}")
		return
	}
	config, err := p.activeConfig(context.Background())
	if err != nil {
		_ = pluginrpc.RespondError(p.client, frame, "config_error", err.Error())
		return
	}
	if strings.TrimSpace(config.Endpoint) == "" || strings.TrimSpace(config.Model) == "" {
		_ = pluginrpc.RespondError(p.client, frame, "not_configured", "LLM is not configured: set endpoint and model in the LLM pane")
		return
	}
	timeout := request.TimeoutSeconds
	if timeout <= 0 {
		timeout = config.TimeoutSeconds
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()
	result, err := complete(ctx, p.httpClient, config, request.Messages, request.JSONMode, request.ExtraBody)
	if err != nil {
		_ = pluginrpc.RespondError(p.client, frame, "llm_error", err.Error())
		return
	}
	_ = pluginrpc.Respond(p.client, frame, result)
}

// complete performs one OpenAI-compatible chat completion.
func complete(ctx context.Context, client *http.Client, config Config, messages []map[string]string, jsonMode bool, extraBody map[string]any) (CompletionResult, error) {
	body := map[string]any{"model": config.Model, "messages": messages}
	if jsonMode {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	for key, value := range extraBody {
		body[key] = value
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return CompletionResult{}, err
	}
	endpoint := strings.TrimRight(config.Endpoint, "/")
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return CompletionResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if config.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+config.APIKey)
	}
	response, err := client.Do(request)
	if err != nil {
		return CompletionResult{}, err
	}
	defer response.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return CompletionResult{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return CompletionResult{}, fmt.Errorf("LLM returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(limited)))
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
		return CompletionResult{}, errors.New("LLM returned a malformed completion")
	}
	model := envelope.Model
	if model == "" {
		model = config.Model
	}
	return CompletionResult{Content: strings.TrimSpace(envelope.Choices[0].Message.Content), Model: model}, nil
}
