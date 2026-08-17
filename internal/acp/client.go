// Package acp implements the small ACP-over-stdio subset used by Viewer chat.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

const maxLineBytes = 16 * 1024 * 1024

// Update is one session/update notification.
type Update struct {
	SessionID string
	Value     map[string]any
	Raw       json.RawMessage
}

// RPCError is a JSON-RPC error returned by the ACP agent.
type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string { return fmt.Sprintf("ACP RPC error %d: %s", e.Code, e.Message) }

// ModelChoice is one entry of the ACP SessionModelState availableModels array.
// hermes encodes modelId as "provider:model"; the same string is accepted by
// session/set_model, so discovery and enforcement share one identifier.
type ModelChoice struct {
	ID          string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ConfigOptionChoice is one selectable value of an ACP session config option.
type ConfigOptionChoice struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description"`
}

// ConfigOption is one ACP session config option. opencode (which does not
// implement SessionModelState) exposes its model picker this way: a
// select-typed option with id "model" whose choice values are "provider/model".
type ConfigOption struct {
	ID           string               `json:"id"`
	Category     string               `json:"category"`
	Name         string               `json:"name"`
	Type         string               `json:"type"`
	CurrentValue string               `json:"currentValue"`
	Options      []ConfigOptionChoice `json:"options"`
}

// SessionInfo is the parsed result of session/new. Agents that predate the
// models/configOptions protocol extensions simply leave the slices empty.
type SessionInfo struct {
	ID            string
	Models        []ModelChoice
	CurrentModel  string
	ConfigOptions []ConfigOption
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type pendingResult struct {
	result json.RawMessage
	err    error
}

// Client owns one ACP stream. Request responses may arrive out of order.
type Client struct {
	reader io.ReadCloser
	writer io.WriteCloser
	cmd    *exec.Cmd

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[string]chan pendingResult
	updates func(Update)
	done    chan struct{}
	err     error
	close   sync.Once
	stderr  *boundedBuffer
}

// New starts an ACP subprocess with stdin/stdout/stderr pipes.
// The child process runs with a stable working directory (the user's home
// directory) instead of inheriting the parent's cwd: a deleted or
// inaccessible parent cwd makes Python-based agents (e.g. `hermes acp`)
// crash during startup before any ACP frame is exchanged.
func New(ctx context.Context, command string, arguments ...string) (*Client, error) {
	cmd := exec.CommandContext(ctx, command, arguments...)
	if dir, err := os.UserHomeDir(); err == nil && dir != "" {
		cmd.Dir = dir
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	client := newClient(stdout, stdin)
	client.cmd = cmd
	go func() {
		_, _ = io.Copy(client.stderr, stderr)
	}()
	go func() {
		err := cmd.Wait()
		client.fail(fmt.Errorf("ACP process exited: %w", err))
	}()
	return client, nil
}

// NewStream constructs a client around an already-connected duplex stream.
// It is primarily useful for protocol tests and embedded agents.
func NewStream(reader io.ReadCloser, writer io.WriteCloser) *Client {
	return newClient(reader, writer)
}

func newClient(reader io.ReadCloser, writer io.WriteCloser) *Client {
	c := &Client{reader: reader, writer: writer, pending: make(map[string]chan pendingResult), done: make(chan struct{}), stderr: &boundedBuffer{limit: 32 * 1024}}
	go c.readLoop()
	return c
}

// OnUpdate installs the notification callback. The callback runs on the reader goroutine.
func (c *Client) OnUpdate(callback func(Update)) { c.mu.Lock(); c.updates = callback; c.mu.Unlock() }

// Initialize performs the ACP handshake.
func (c *Client) Initialize(ctx context.Context) (map[string]any, error) {
	var result map[string]any
	err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]any{"name": "viewer-chat", "version": "0.1.0"},
	}, &result)
	return result, err
}

// NewSession creates an agent session rooted at cwd.
// hermes v0.20.0 made `mcpServers` a required field of session/new: the agent's
// pydantic schema rejects a request without it (-32602 Invalid params). Viewer
// never wires MCP servers into agent sessions, so an empty array satisfies the
// schema while keeping the session server-free.
func (c *Client) NewSession(ctx context.Context, cwd string) (SessionInfo, error) {
	var result struct {
		SessionID string `json:"sessionId"`
		Models    *struct {
			AvailableModels []ModelChoice `json:"availableModels"`
			CurrentModelID  string        `json:"currentModelId"`
		} `json:"models"`
		ConfigOptions []ConfigOption `json:"configOptions"`
	}
	if err := c.request(ctx, "session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}}, &result); err != nil {
		return SessionInfo{}, err
	}
	if result.SessionID == "" {
		return SessionInfo{}, errors.New("ACP session/new returned no sessionId")
	}
	info := SessionInfo{ID: result.SessionID, ConfigOptions: result.ConfigOptions}
	if result.Models != nil {
		info.Models = result.Models.AvailableModels
		info.CurrentModel = result.Models.CurrentModelID
	}
	return info, nil
}

// LoadSession binds a persisted provider session to this new transport.
// Same required-field rule as session/new: `mcpServers` must be present (can
// be empty) or hermes v0.20.0+ rejects the call with -32602.
func (c *Client) LoadSession(ctx context.Context, sessionID, cwd string) error {
	var result any
	if err := c.request(ctx, "session/load", map[string]any{"sessionId": sessionID, "cwd": cwd, "mcpServers": []any{}}, &result); err != nil {
		return err
	}
	if result == nil {
		return errors.New("ACP session/load returned null")
	}
	return nil
}

// SetSessionModel switches the session to the given model selection. hermes
// resolves "provider:model" via parse_model_input, so one call sets both the
// provider and the model for the session (acp_adapter.set_session_model
// rebuilds the agent with the requested provider). This is how viewerd
// enforces the routing profile's provider/model choice on the hermes side —
// CLI flags alone do not reach the ACP runtime model selection.
func (c *Client) SetSessionModel(ctx context.Context, sessionID, modelID string) error {
	var result any
	return c.request(ctx, "session/set_model", map[string]any{"sessionId": sessionID, "modelId": modelID}, &result)
}

// SetConfigOption sets one session config option. opencode routes model
// selection through this RPC (configId "model", value "provider/model") and
// validates the value server-side, rejecting unknown models with -32602.
func (c *Client) SetConfigOption(ctx context.Context, sessionID, configID, value string) error {
	var result any
	return c.request(ctx, "session/set_config_option", map[string]any{"sessionId": sessionID, "configId": configID, "value": value}, &result)
}

// Prompt sends one text-only turn and returns its stop reason.
func (c *Client) Prompt(ctx context.Context, sessionID, text string) (string, error) {
	var result struct {
		StopReason string `json:"stopReason"`
	}
	err := c.request(ctx, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    []map[string]any{{"type": "text", "text": text}},
	}, &result)
	if err != nil {
		return "", err
	}
	if result.StopReason == "" {
		result.StopReason = "end_turn"
	}
	return result.StopReason, nil
}

// Cancel sends the ACP best-effort cancellation notification.
func (c *Client) Cancel(ctx context.Context, sessionID string) error {
	return c.notify(ctx, "session/cancel", map[string]any{"sessionId": sessionID})
}

func (c *Client) request(ctx context.Context, method string, params any, target any) error {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.nextID++
	numericID := c.nextID
	id := strconv.FormatInt(numericID, 10)
	waiter := make(chan pendingResult, 1)
	c.pending[id] = waiter
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.write(ctx, map[string]any{"jsonrpc": "2.0", "id": numericID, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case item := <-waiter:
		if item.err != nil {
			return item.err
		}
		if target == nil {
			return nil
		}
		return json.Unmarshal(item.result, target)
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.Err()
	}
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	return c.write(ctx, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *Client) write(ctx context.Context, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	_, err = c.writer.Write(data)
	return err
}

func (c *Client) readLoop() {
	reader := bufio.NewReaderSize(c.reader, 64*1024)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > maxLineBytes {
			c.fail(errors.New("ACP frame exceeds 16 MiB"))
			return
		}
		if strings.TrimSpace(line) != "" {
			c.dispatch([]byte(line))
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.fail(fmt.Errorf("read ACP stream: %w", err))
			} else {
				c.fail(io.EOF)
			}
			return
		}
	}
}

func (c *Client) dispatch(line []byte) {
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if json.Unmarshal(line, &envelope) != nil || envelope.JSONRPC != "2.0" {
		return // malformed/foreign stdout is tolerated; later valid frames still work
	}
	if len(envelope.ID) > 0 && string(envelope.ID) != "null" && envelope.Method == "" {
		var item response
		if json.Unmarshal(line, &item) != nil {
			return
		}
		id := strings.Trim(string(item.ID), `"`)
		c.mu.Lock()
		waiter := c.pending[id]
		c.mu.Unlock()
		if waiter == nil {
			return
		}
		if item.Error != nil {
			waiter <- pendingResult{err: &RPCError{Code: item.Error.Code, Message: item.Error.Message}}
		} else {
			waiter <- pendingResult{result: item.Result}
		}
		return
	}
	// Incoming request from the agent (method + id). Viewer auto-approves every
	// permission request and rejects anything else it does not implement, so an
	// agent asking session/request_permission never stalls the turn waiting for
	// a human answer that has no UI in Viewer.
	if len(envelope.ID) > 0 && string(envelope.ID) != "null" && envelope.Method != "" {
		c.answerAgentRequest(envelope.ID, envelope.Method, envelope.Params)
		return
	}
	if envelope.Method != "session/update" {
		return
	}
	var params struct {
		SessionID string         `json:"sessionId"`
		Update    map[string]any `json:"update"`
	}
	if json.Unmarshal(envelope.Params, &params) != nil {
		return
	}
	c.mu.Lock()
	callback := c.updates
	c.mu.Unlock()
	if callback != nil {
		callback(Update{SessionID: params.SessionID, Value: params.Update, Raw: append(json.RawMessage(nil), envelope.Params...)})
	}
}

// answerAgentRequest replies to a request the agent sent to the client.
// session/request_permission is always approved (Viewer has no approval UI and
// the user policy is to allow everything); any other agent→client request gets
// a JSON-RPC method-not-found error so the agent unblocks immediately instead
// of waiting out its own timeout.
func (c *Client) answerAgentRequest(id json.RawMessage, method string, params json.RawMessage) {
	var result any
	var rpcErr *RPCError
	if method == "session/request_permission" {
		result = map[string]any{"outcome": map[string]any{
			"outcome":  "selected",
			"optionId": pickAllowOption(params),
		}}
	} else {
		rpcErr = &RPCError{Code: -32601, Message: "viewer-chat does not implement " + method}
	}
	frame := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		frame["error"] = map[string]any{"code": rpcErr.Code, "message": rpcErr.Message}
	} else {
		frame["result"] = result
	}
	_ = c.write(context.Background(), frame)
}

// pickAllowOption chooses the strongest allow option offered by a
// session/request_permission call: allow_always when present, otherwise the
// first option whose kind starts with "allow", otherwise the first option.
// With no usable options it returns the conventional "allow_once" id.
func pickAllowOption(params json.RawMessage) string {
	var parsed struct {
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(params, &parsed) != nil || len(parsed.Options) == 0 {
		return "allow_once"
	}
	fallback := ""
	for _, option := range parsed.Options {
		if option.OptionID == "allow_always" {
			return option.OptionID
		}
		if fallback == "" && strings.HasPrefix(option.Kind, "allow") {
			fallback = option.OptionID
		}
	}
	if fallback != "" {
		return fallback
	}
	return parsed.Options[0].OptionID
}

func (c *Client) fail(err error) {
	c.close.Do(func() {
		c.mu.Lock()
		c.err = err
		waiters := make([]chan pendingResult, 0, len(c.pending))
		for _, waiter := range c.pending {
			waiters = append(waiters, waiter)
		}
		c.mu.Unlock()
		for _, waiter := range waiters {
			select {
			case waiter <- pendingResult{err: err}:
			default:
			}
		}
		close(c.done)
	})
}

// Err reports the terminal stream/process error, if any.
func (c *Client) Err() error { c.mu.Lock(); defer c.mu.Unlock(); return c.err }

// Stderr returns the bounded tail captured from the subprocess.
func (c *Client) Stderr() string { return c.stderr.String() }

// Close terminates the subprocess and closes both stream directions.
func (c *Client) Close() error {
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	err := errors.Join(c.reader.Close(), c.writer.Close())
	c.fail(errors.New("ACP client closed"))
	return err
}

type boundedBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, value...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(value), nil
}

func (b *boundedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }
