// Package llm implements the global LLM forwarding plugin. Viewer plugins use
// `llm:_:complete`; local external tools may use its optional loopback
// OpenAI-compatible HTTP facade. Both re-read the same active config so one
// model switch applies everywhere.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

var Manifest = busclient.Manifest{
	ID: "llm", Version: "0.6.0",
	Slots: map[string]any{
		"llm:_:complete":       map[string]any{"summary": "OpenAI-compatible chat completion; RPC {messages, model?, json_mode?, timeout_seconds?, extra_body?} -> {content, model}; extra_body is merged verbatim into the request body over the active config's default extra_body (callers win per key; endpoint-specific). Fallback chain: a requested model's configs first (several configs may share one model name = its providers, list order decides priority), then active, then the remaining profiles top-down; an endpoint that fails with a retryable error (transport, 404/408/429/5xx, malformed reply) cools down for 60s and is skipped meanwhile"},
		"llm:_:test":           map[string]any{"summary": "Probe one endpoint config {endpoint, key?, model, timeout_seconds?, extra_body?} with a minimal completion -> {ok, latency_ms, model?, error?}; isolated from the fallback chain and the breaker"},
		"llm:_:http:configure": map[string]any{"summary": "Configure the OpenAI-compatible HTTP facade {enabled, port, expose}"},
		"llm:_:http:status":    map[string]any{"summary": "Report the loopback HTTP facade state"},
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
	// breakerCooldown benches an endpoint after one retryable failure; calls
	// skip it until the cooldown elapses. In-memory only; any plugins.llm
	// config change clears the state (user edits fix endpoints).
	breakerCooldown = 60 * time.Second
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
	// ExtraBody holds per-model default request fields (e.g.
	// {"reasoning_effort":"medium"}), merged into every outbound request
	// body; per-call fields (RPC extra_body / HTTP request body) win per
	// key. Endpoint-specific; strict servers may reject unknown fields with
	// HTTP 400.
	ExtraBody map[string]any `json:"extra_body,omitempty"`
}

// CompletionResult is the RPC reply payload.
type CompletionResult struct {
	Content string `json:"content"`
	Model   string `json:"model"`
}

type Plugin struct {
	client     *busclient.Client
	httpClient *http.Client
	router     *router
	serverMu   sync.Mutex
	server     *http.Server
	httpConfig HTTPConfig
	httpError  string
}

func New() *Plugin {
	// No client-level Timeout: callers bound their own contexts (per-request
	// timeout_seconds or the active config's), like chat's llm.go did.
	return &Plugin{httpClient: &http.Client{}, router: newRouter()}
}

func (p *Plugin) Start(ctx context.Context, kernelWS string, managed bool) error {
	p.client = busclient.New(kernelWS, Manifest, busclient.WithManaged(managed))
	if _, err := p.client.Subscribe("llm:_:complete", p.handleComplete); err != nil {
		return fmt.Errorf("subscribe llm:_:complete: %w", err)
	}
	if _, err := p.client.Subscribe("llm:_:test", p.handleTest); err != nil {
		return fmt.Errorf("subscribe llm:_:test: %w", err)
	}
	if _, err := p.client.Subscribe("llm:_:http:configure", p.handleHTTPConfigure); err != nil {
		return fmt.Errorf("subscribe llm:_:http:configure: %w", err)
	}
	if _, err := p.client.Subscribe("llm:_:http:status", p.handleHTTPStatus); err != nil {
		return fmt.Errorf("subscribe llm:_:http:status: %w", err)
	}
	if _, err := p.client.Subscribe("config:plugins.llm:config", p.handleConfigChange); err != nil {
		return fmt.Errorf("subscribe llm config mailbox: %w", err)
	}
	if err := p.client.Connect(ctx); err != nil {
		return fmt.Errorf("connect llm plugin: %w", err)
	}
	if err := p.migrateLegacyConfig(ctx); err != nil {
		return fmt.Errorf("migrate legacy llm config: %w", err)
	}
	httpConfig, err := p.loadHTTPConfig(ctx)
	if err != nil {
		return fmt.Errorf("read llm HTTP config: %w", err)
	}
	if err := p.applyHTTPConfig(httpConfig); err != nil {
		// A stale/occupied configured port must not prevent Viewer from starting.
		slog.Error("llm HTTP facade failed to start", "error", err)
	}
	return nil
}

func (p *Plugin) Close(ctx context.Context) error {
	p.serverMu.Lock()
	server := p.server
	p.server = nil
	p.serverMu.Unlock()
	if server != nil {
		_ = server.Shutdown(ctx)
	}
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

// candidate is one endpoint in the fallback chain: the active config first,
// then the profiles library in stored order.
type candidate struct {
	name   string
	config Config
}

// key identifies an endpoint for breaker state; the model matters because one
// server may serve several models with different health.
func (c candidate) key() string {
	return normalizeEndpoint(c.config.Endpoint) + "\n" + c.config.Model
}

// normalizeEndpoint makes the base /v1 URL and the full chat-completions URL
// equivalent, like the frontend's active matching.
func normalizeEndpoint(value string) string {
	result := strings.TrimRight(strings.TrimSpace(value), "/")
	return strings.TrimSuffix(result, "/chat/completions")
}

// storedProfile mirrors one entry of the frontend-managed profiles library
// (`plugins.llm.profiles`).
type storedProfile struct {
	Name           string         `json:"name"`
	Endpoint       string         `json:"endpoint"`
	APIKey         string         `json:"key"`
	Model          string         `json:"model"`
	TimeoutSeconds int            `json:"timeout_seconds"`
	ExtraBody      map[string]any `json:"extra_body,omitempty"`
}

// buildCandidates orders the fallback chain over the flat profiles library
// (several configs may share one model name = that model's providers).
// Default order: active first, then profiles top-down; entries without
// endpoint+model and duplicates of an already listed endpoint+model are
// skipped. A requested model name re-sorts stably: that model's configs first
// (list order kept), then the rest in default order, so an unknown or fully
// failing model still rolls down to the remaining configs.
func buildCandidates(active Config, profiles []storedProfile, requested string) []candidate {
	var chain []candidate
	seen := map[string]bool{}
	if strings.TrimSpace(active.Endpoint) != "" && strings.TrimSpace(active.Model) != "" {
		entry := candidate{name: "active", config: active}
		chain = append(chain, entry)
		seen[entry.key()] = true
	}
	for _, profile := range profiles {
		if strings.TrimSpace(profile.Endpoint) == "" || strings.TrimSpace(profile.Model) == "" {
			continue
		}
		name := strings.TrimSpace(profile.Name)
		if name == "" {
			name = profile.Model
		}
		entry := candidate{name: name, config: Config{
			Endpoint: profile.Endpoint, APIKey: profile.APIKey, Model: profile.Model,
			TimeoutSeconds: profile.TimeoutSeconds, ExtraBody: profile.ExtraBody,
		}}
		if seen[entry.key()] {
			continue
		}
		seen[entry.key()] = true
		chain = append(chain, entry)
	}
	if requested = strings.TrimSpace(requested); requested != "" {
		var matching, rest []candidate
		for _, entry := range chain {
			if entry.config.Model == requested {
				matching = append(matching, entry)
			} else {
				rest = append(rest, entry)
			}
		}
		chain = append(matching, rest...)
	}
	return chain
}

// router is the passive circuit breaker: an endpoint that fails with a
// retryable error is skipped for breakerCooldown, then retried once. State is
// in-memory only and cleared on any plugins.llm config change.
type router struct {
	mu        sync.Mutex
	coolUntil map[string]time.Time
	now       func() time.Time
}

func newRouter() *router {
	return &router{coolUntil: map[string]time.Time{}, now: time.Now}
}

func (r *router) cooled(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, exists := r.coolUntil[key]
	return exists && r.now().Before(until)
}

func (r *router) fail(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.coolUntil[key] = r.now().Add(breakerCooldown)
}

func (r *router) succeed(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.coolUntil, key)
}

func (r *router) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.coolUntil = map[string]time.Time{}
}

// route tries candidates in order, skipping cooled endpoints and falling back
// on retryable failures. A non-retryable error (caller-side 4xx) aborts the
// chain immediately: every endpoint would repeat it.
func (r *router) route(ctx context.Context, chain []candidate, attempt func(context.Context, Config) (CompletionResult, error)) (CompletionResult, error) {
	var failures []string
	for _, entry := range chain {
		key := entry.key()
		if r.cooled(key) {
			continue
		}
		result, err := attempt(ctx, entry.config)
		if err == nil {
			r.succeed(key)
			return result, nil
		}
		if !retryableError(err) {
			return CompletionResult{}, err
		}
		r.fail(key)
		slog.Warn("llm endpoint failed, falling back", "candidate", entry.name, "error", err)
		failures = append(failures, entry.name+": "+err.Error())
	}
	if len(failures) == 0 {
		return CompletionResult{}, errors.New("all LLM endpoints are cooling down after recent failures")
	}
	return CompletionResult{}, fmt.Errorf("all LLM endpoints failed (%s)", strings.Join(failures, "; "))
}

// statusError is an upstream non-2xx reply.
type statusError struct {
	status int
	body   string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("LLM returned HTTP %d: %s", e.status, e.body)
}

// retryableStatus marks availability failures worth falling back on; other
// 4xx are caller/config errors that every endpoint would repeat. 404 counts:
// an endpoint may not have the serving config's model loaded.
func retryableStatus(status int) bool {
	return status == http.StatusNotFound || status == http.StatusRequestTimeout ||
		status == http.StatusTooManyRequests || status >= 500
}

func retryableError(err error) bool {
	var statusErr *statusError
	if errors.As(err, &statusErr) {
		return retryableStatus(statusErr.status)
	}
	// Transport errors (endpoint down, timeout) and malformed replies.
	return true
}

// candidates builds the per-call fallback chain; both keys are re-read on
// every call so pane edits apply immediately. A non-empty requested model
// sorts that model's configs first (see buildCandidates).
func (p *Plugin) candidates(ctx context.Context, requested string) ([]candidate, error) {
	value, err := p.client.Request(ctx, "config:_:get", map[string]any{"plugin": configNamespace, "key": "active"}, rpcBudget)
	if err != nil {
		return nil, fmt.Errorf("read llm config: %w", err)
	}
	var active Config
	if value != nil {
		encoded, _ := json.Marshal(value)
		_ = json.Unmarshal(encoded, &active)
	}
	value, err = p.client.Request(ctx, "config:_:get", map[string]any{"plugin": configNamespace, "key": "profiles"}, rpcBudget)
	if err != nil {
		return nil, fmt.Errorf("read llm profiles: %w", err)
	}
	var profiles []storedProfile
	if value != nil {
		encoded, _ := json.Marshal(value)
		_ = json.Unmarshal(encoded, &profiles)
	}
	return buildCandidates(active, profiles, requested), nil
}

type completionRequest struct {
	Messages []map[string]string `json:"messages"`
	// Model optionally requests one model: its configs (one model name may
	// sit on several configs = its providers) are tried first, then the
	// chain rolls down to the remaining configs.
	Model          string `json:"model"`
	JSONMode       bool   `json:"json_mode"`
	TimeoutSeconds int    `json:"timeout_seconds"`
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
	chain, err := p.candidates(context.Background(), request.Model)
	if err != nil {
		_ = pluginrpc.RespondError(p.client, frame, "config_error", err.Error())
		return
	}
	if len(chain) == 0 {
		_ = pluginrpc.RespondError(p.client, frame, "not_configured", "LLM is not configured: set endpoint and model in the LLM pane")
		return
	}
	result, err := p.router.route(context.Background(), chain, func(ctx context.Context, config Config) (CompletionResult, error) {
		timeout := request.TimeoutSeconds
		if timeout <= 0 {
			timeout = config.TimeoutSeconds
		}
		if timeout <= 0 {
			timeout = defaultTimeout
		}
		attemptCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
		return complete(attemptCtx, p.httpClient, config, request.Messages, request.JSONMode, request.ExtraBody)
	})
	if err != nil {
		_ = pluginrpc.RespondError(p.client, frame, "llm_error", err.Error())
		return
	}
	_ = pluginrpc.Respond(p.client, frame, result)
}

// handleTest probes one explicit endpoint config with a minimal completion.
// It is isolated from the fallback chain and never touches the breaker, so
// the pane can check a channel without side effects.
func (p *Plugin) handleTest(frame busclient.Frame) {
	raw, ok := pluginrpc.Object(frame)
	if !ok {
		_ = pluginrpc.RespondError(p.client, frame, "bad_request", "payload must be an object {endpoint, key?, model, timeout_seconds?, extra_body?}")
		return
	}
	encoded, _ := json.Marshal(raw)
	var config Config
	if err := json.Unmarshal(encoded, &config); err != nil {
		_ = pluginrpc.RespondError(p.client, frame, "bad_request", err.Error())
		return
	}
	if strings.TrimSpace(config.Endpoint) == "" || strings.TrimSpace(config.Model) == "" {
		_ = pluginrpc.RespondError(p.client, frame, "bad_request", "endpoint and model are required")
		return
	}
	timeout := config.TimeoutSeconds
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()
	started := time.Now()
	result, err := complete(ctx, p.httpClient, config, []map[string]string{{"role": "user", "content": "reply with exactly: ok"}}, false, nil)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		_ = pluginrpc.Respond(p.client, frame, map[string]any{"ok": false, "latency_ms": latency, "error": err.Error()})
		return
	}
	_ = pluginrpc.Respond(p.client, frame, map[string]any{"ok": true, "latency_ms": latency, "model": result.Model})
}

// complete performs one OpenAI-compatible chat completion.
func complete(ctx context.Context, client *http.Client, config Config, messages []map[string]string, jsonMode bool, extraBody map[string]any) (CompletionResult, error) {
	body := map[string]any{"model": config.Model, "messages": messages}
	if jsonMode {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	for key, value := range config.ExtraBody {
		body[key] = value
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
		return CompletionResult{}, &statusError{status: response.StatusCode, body: strings.TrimSpace(string(limited))}
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
