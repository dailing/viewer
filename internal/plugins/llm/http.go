package llm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

const (
	defaultHTTPPort    = 18731
	maxRequestBytes    = 1 << 20
	maxResponseLogSize = 4 << 20
	loopbackHost       = "127.0.0.1"
	allInterfacesHost  = "0.0.0.0"
)

// HTTPConfig controls the OpenAI-compatible facade. Expose=false is
// loopback-only; expose=true also permits LAN and host.docker.internal clients.
type HTTPConfig struct {
	Enabled bool `json:"enabled"`
	Port    int  `json:"port"`
	Expose  bool `json:"expose"`
}

type httpStatus struct {
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	BaseURL   string `json:"base_url,omitempty"`
	Expose    bool   `json:"expose"`
	LastError string `json:"last_error,omitempty"`
}

func (config HTTPConfig) host() string {
	if config.Expose {
		return allInterfacesHost
	}
	return loopbackHost
}

func normalizeHTTPConfig(config HTTPConfig) (HTTPConfig, error) {
	if config.Port == 0 {
		config.Port = defaultHTTPPort
	}
	if config.Port < 1 || config.Port > 65535 {
		return config, fmt.Errorf("port must be between 1 and 65535")
	}
	return config, nil
}

func (p *Plugin) loadHTTPConfig(ctx context.Context) (HTTPConfig, error) {
	value, err := p.client.Request(ctx, "config:_:get", map[string]any{"plugin": configNamespace, "key": "http"}, rpcBudget)
	if err != nil {
		return HTTPConfig{}, err
	}
	config := HTTPConfig{Port: defaultHTTPPort}
	if value != nil {
		encoded, _ := json.Marshal(value)
		if err := json.Unmarshal(encoded, &config); err != nil {
			return config, err
		}
	}
	return normalizeHTTPConfig(config)
}

func (p *Plugin) handleConfigChange(frame busclient.Frame) {
	raw, ok := pluginrpc.Object(frame)
	if !ok {
		return
	}
	config := HTTPConfig{Port: defaultHTTPPort}
	if value, exists := raw["http"]; exists && value != nil {
		encoded, _ := json.Marshal(value)
		if err := json.Unmarshal(encoded, &config); err != nil {
			p.setHTTPError(err)
			return
		}
	}
	if err := p.applyHTTPConfig(config); err != nil {
		slog.Error("reload llm HTTP facade", "error", err)
	}
}

func (p *Plugin) handleHTTPConfigure(frame busclient.Frame) {
	raw, ok := pluginrpc.Object(frame)
	if !ok {
		_ = pluginrpc.RespondError(p.client, frame, "bad_request", "payload must be {enabled, port}")
		return
	}
	encoded, _ := json.Marshal(raw)
	var config HTTPConfig
	if err := json.Unmarshal(encoded, &config); err != nil {
		_ = pluginrpc.RespondError(p.client, frame, "bad_request", err.Error())
		return
	}
	config, err := normalizeHTTPConfig(config)
	if err != nil {
		_ = pluginrpc.RespondError(p.client, frame, "bad_request", err.Error())
		return
	}

	p.serverMu.Lock()
	previous := p.httpConfig
	p.serverMu.Unlock()
	if err := p.applyHTTPConfig(config); err != nil {
		_ = pluginrpc.RespondError(p.client, frame, "listen_error", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), rpcBudget)
	defer cancel()
	if _, err := p.client.Request(ctx, "config:_:set", map[string]any{"plugin": configNamespace, "key": "http", "value": config}, rpcBudget); err != nil {
		_ = p.applyHTTPConfig(previous)
		_ = pluginrpc.RespondError(p.client, frame, "config_error", err.Error())
		return
	}
	_ = pluginrpc.Respond(p.client, frame, p.currentHTTPStatus())
}

func (p *Plugin) handleHTTPStatus(frame busclient.Frame) {
	_ = pluginrpc.Respond(p.client, frame, p.currentHTTPStatus())
}

func (p *Plugin) currentHTTPStatus() httpStatus {
	p.serverMu.Lock()
	defer p.serverMu.Unlock()
	status := httpStatus{
		Enabled: p.httpConfig.Enabled, Running: p.server != nil,
		Host: p.httpConfig.host(), Port: p.httpConfig.Port, Expose: p.httpConfig.Expose, LastError: p.httpError,
	}
	if status.Running {
		// 0.0.0.0 is a bind address, not a useful client destination.
		status.BaseURL = "http://" + net.JoinHostPort(loopbackHost, strconv.Itoa(status.Port)) + "/v1"
	}
	return status
}

func (p *Plugin) setHTTPError(err error) {
	p.serverMu.Lock()
	defer p.serverMu.Unlock()
	p.httpError = err.Error()
}

// applyHTTPConfig first binds a replacement listener, then swaps servers. A
// bad new port therefore leaves the existing facade available.
func (p *Plugin) applyHTTPConfig(raw HTTPConfig) error {
	config, err := normalizeHTTPConfig(raw)
	if err != nil {
		p.setHTTPError(err)
		return err
	}
	p.serverMu.Lock()
	if config == p.httpConfig && ((config.Enabled && p.server != nil) || (!config.Enabled && p.server == nil)) {
		p.httpError = ""
		p.serverMu.Unlock()
		return nil
	}
	p.serverMu.Unlock()

	var replacement *http.Server
	var listener net.Listener
	if config.Enabled {
		address := net.JoinHostPort(config.host(), strconv.Itoa(config.Port))
		listener, err = net.Listen("tcp", address)
		if err != nil {
			wrapped := fmt.Errorf("listen %s: %w", address, err)
			p.setHTTPError(wrapped)
			return wrapped
		}
		replacement = &http.Server{
			Handler:           p.httpHandler(),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       90 * time.Second,
		}
	}

	p.serverMu.Lock()
	previous := p.server
	p.server = replacement
	p.httpConfig = config
	p.httpError = ""
	p.serverMu.Unlock()
	if replacement != nil {
		go func() {
			if serveErr := replacement.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
				slog.Error("llm HTTP facade stopped", "error", serveErr)
				p.setHTTPError(serveErr)
			}
		}()
		slog.Info("llm HTTP facade listening", "address", listener.Addr().String())
	}
	if previous != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = previous.Shutdown(shutdownCtx)
		cancel()
	}
	return nil
}

func (p *Plugin) httpHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/models", p.serveModels)
	mux.HandleFunc("POST /v1/chat/completions", p.serveChatCompletions)
	return mux
}

func (p *Plugin) serveModels(writer http.ResponseWriter, request *http.Request) {
	config, err := p.activeConfig(request.Context())
	if err != nil {
		writeOpenAIError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	if strings.TrimSpace(config.Model) == "" {
		writeOpenAIError(writer, http.StatusServiceUnavailable, "LLM is not configured")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"object": "list",
		"data":   []map[string]any{{"id": config.Model, "object": "model", "owned_by": "viewer"}},
	})
}

func (p *Plugin) serveChatCompletions(writer http.ResponseWriter, request *http.Request) {
	requestID := newRequestID()
	loggedWriter := newLoggingResponseWriter(writer)
	writer = loggedWriter
	writer.Header().Set("X-Viewer-LLM-Request-ID", requestID)
	startedAt := time.Now()
	var requestBody, upstreamBody, upstreamEndpoint, requestError string
	var upstreamStatus int
	defer func() {
		slog.Info("llm HTTP completion",
			"request_id", requestID,
			"remote_addr", request.RemoteAddr,
			"user_agent", request.UserAgent(),
			"request_body", requestBody,
			"upstream_endpoint", upstreamEndpoint,
			"upstream_body", upstreamBody,
			"upstream_status", upstreamStatus,
			"response_status", loggedWriter.statusCode(),
			"response_body", loggedWriter.body.String(),
			"response_truncated", loggedWriter.body.truncated,
			"error", requestError,
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)
	}()

	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBytes)
	rawBody, err := io.ReadAll(request.Body)
	requestBody = string(rawBody)
	if err != nil {
		requestError = err.Error()
		writeOpenAIError(writer, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	var body map[string]any
	if err := json.Unmarshal(rawBody, &body); err != nil {
		requestError = err.Error()
		writeOpenAIError(writer, http.StatusBadRequest, "invalid JSON request: "+err.Error())
		return
	}
	if _, ok := body["messages"].([]any); !ok {
		requestError = "messages must be an array"
		writeOpenAIError(writer, http.StatusBadRequest, "messages must be an array")
		return
	}
	config, err := p.activeConfig(request.Context())
	if err != nil {
		requestError = err.Error()
		writeOpenAIError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	if strings.TrimSpace(config.Endpoint) == "" || strings.TrimSpace(config.Model) == "" {
		requestError = "LLM is not configured"
		writeOpenAIError(writer, http.StatusServiceUnavailable, "LLM is not configured")
		return
	}
	body["model"] = config.Model
	encoded, err := json.Marshal(body)
	if err != nil {
		requestError = err.Error()
		writeOpenAIError(writer, http.StatusBadRequest, err.Error())
		return
	}
	upstreamBody = string(encoded)
	endpoint := strings.TrimRight(config.Endpoint, "/")
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}
	upstreamEndpoint = endpoint
	timeout := config.TimeoutSeconds
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(request.Context(), time.Duration(timeout)*time.Second)
	defer cancel()
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(encoded)))
	if err != nil {
		requestError = err.Error()
		writeOpenAIError(writer, http.StatusBadGateway, err.Error())
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Accept", request.Header.Get("Accept"))
	if config.APIKey != "" {
		upstream.Header.Set("Authorization", "Bearer "+config.APIKey)
	}
	response, err := p.httpClient.Do(upstream)
	if err != nil {
		requestError = err.Error()
		writeOpenAIError(writer, http.StatusBadGateway, err.Error())
		return
	}
	defer response.Body.Close()
	upstreamStatus = response.StatusCode
	for _, header := range []string{"Content-Type", "Cache-Control"} {
		if value := response.Header.Get(header); value != "" {
			writer.Header().Set(header, value)
		}
	}
	writer.WriteHeader(response.StatusCode)
	if _, err := io.Copy(writer, response.Body); err != nil {
		requestError = err.Error()
	}
}

type limitedLogBuffer struct {
	bytes.Buffer
	truncated bool
}

func (buffer *limitedLogBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	remaining := maxResponseLogSize - buffer.Len()
	if remaining <= 0 {
		buffer.truncated = buffer.truncated || originalLength > 0
		return originalLength, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		buffer.truncated = true
	}
	_, _ = buffer.Buffer.Write(value)
	return originalLength, nil
}

type loggingResponseWriter struct {
	http.ResponseWriter
	body   limitedLogBuffer
	status int
}

func newLoggingResponseWriter(writer http.ResponseWriter) *loggingResponseWriter {
	return &loggingResponseWriter{ResponseWriter: writer}
}

func (writer *loggingResponseWriter) WriteHeader(status int) {
	if writer.status == 0 {
		writer.status = status
	}
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *loggingResponseWriter) Write(value []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	_, _ = writer.body.Write(value)
	return writer.ResponseWriter.Write(value)
}

func (writer *loggingResponseWriter) Flush() {
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (writer *loggingResponseWriter) statusCode() int {
	if writer.status == 0 {
		return http.StatusOK
	}
	return writer.status
}

func newRequestID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err == nil {
		return fmt.Sprintf("%x", value[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeOpenAIError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]any{"error": map[string]any{"message": message, "type": "viewer_llm_error"}})
}
