// Package pirpc implements Pi's native JSONL RPC protocol.
package pirpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxLineBytes = 16 << 20

type ProcessConfig struct {
	Command   string
	Arguments []string
	Dir       string
}

type Event struct {
	Type  string
	Value map[string]any
	Raw   json.RawMessage
}

type Model struct {
	Provider      string `json:"provider"`
	ID            string `json:"id"`
	ContextWindow int64  `json:"contextWindow"`
}

type State struct {
	SessionFile   string `json:"sessionFile"`
	SessionID     string `json:"sessionId"`
	ThinkingLevel string `json:"thinkingLevel"`
	Model         *Model `json:"model"`
}

type PromptResult struct {
	Disposition string
	Aborted     bool
}

type response struct {
	data map[string]any
	raw  json.RawMessage
	err  error
}

type settled struct {
	aborted bool
	err     error
}

type Client struct {
	ctx    context.Context
	cancel context.CancelFunc
	cmd    *exec.Cmd
	stdin  io.WriteCloser

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan response
	onEvent func(Event)

	settledWaiter chan settled
	turnErr       string
	promptMu      sync.Mutex
	nextID        atomic.Int64
	closed        bool
	readerDone    chan struct{}
}

func New(ctx context.Context, config ProcessConfig) (*Client, error) {
	if strings.TrimSpace(config.Command) == "" {
		config.Command = "pi"
	}
	runCtx, cancel := context.WithCancel(ctx)
	client := &Client{
		ctx: runCtx, cancel: cancel, pending: map[string]chan response{},
		readerDone: make(chan struct{}),
	}
	client.cmd = exec.CommandContext(runCtx, config.Command, config.Arguments...)
	client.cmd.Dir = config.Dir
	stdout, err := client.cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	client.stdin, err = client.cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err = client.cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	go client.readLoop(stdout)
	return client, nil
}

func (c *Client) OnEvent(callback func(Event)) {
	c.mu.Lock()
	c.onEvent = callback
	c.mu.Unlock()
}

func (c *Client) readLoop(reader io.Reader) {
	defer close(c.readerDone)
	buffered := bufio.NewReaderSize(reader, 64*1024)
	for {
		line, err := buffered.ReadBytes('\n')
		if len(line) > maxLineBytes {
			c.failAll(fmt.Errorf("pi RPC line exceeds %d bytes", maxLineBytes))
			c.cancel()
			return
		}
		if len(line) > 0 {
			var message map[string]any
			if unmarshalErr := json.Unmarshal(line, &message); unmarshalErr != nil {
				c.failAll(fmt.Errorf("decode pi RPC record: %w", unmarshalErr))
				c.cancel()
				return
			}
			c.handleMessage(message, append(json.RawMessage(nil), line...))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.failAll(errors.New("pi RPC stdout closed"))
			} else {
				c.failAll(err)
			}
			return
		}
	}
}

func (c *Client) handleMessage(message map[string]any, raw json.RawMessage) {
	recordType, _ := message["type"].(string)
	if recordType == "response" {
		id, _ := message["id"].(string)
		c.mu.Lock()
		waiter := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if waiter != nil {
			if success, _ := message["success"].(bool); !success {
				errorText, _ := message["error"].(string)
				if errorText == "" {
					errorText = "Pi command failed"
				}
				waiter <- response{err: errors.New(errorText)}
			} else {
				data, _ := message["data"].(map[string]any)
				waiter <- response{data: data, raw: raw}
			}
		}
		return
	}

	if recordType == "extension_ui_request" {
		method, _ := message["method"].(string)
		switch method {
		case "select", "confirm", "input", "editor":
			// Viewer has no extension-dialog channel. Cancel rather than leave
			// Pi blocked forever waiting for a response that cannot be shown.
			id, _ := message["id"].(string)
			if id != "" {
				_ = c.write(map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true})
			}
		}
	}

	event := Event{Type: recordType, Value: message, Raw: raw}
	c.mu.Lock()
	callback := c.onEvent
	switch recordType {
	case "message_end":
		assistant, _ := message["message"].(map[string]any)
		role, _ := assistant["role"].(string)
		stopReason, _ := assistant["stopReason"].(string)
		if role == "assistant" && stopReason == "error" {
			errorText, _ := assistant["errorMessage"].(string)
			if errorText == "" {
				errorText = "Pi assistant turn failed"
			}
			c.turnErr = errorText
		} else if role == "assistant" && stopReason == "deferred" && c.turnErr == "" {
			c.turnErr = "Pi returned a deferred response; Viewer cannot resume deferred provider handles"
		}
	case "auto_retry_end":
		if success, _ := message["success"].(bool); !success {
			errorText, _ := message["finalError"].(string)
			if errorText == "" {
				errorText = "Pi automatic retry failed"
			}
			c.turnErr = errorText
		}
	}
	waiter := c.settledWaiter
	c.mu.Unlock()
	if callback != nil && recordType != "" {
		callback(event)
	}
	if recordType == "agent_settled" && waiter != nil {
		aborted, _ := message["aborted"].(bool)
		waiter <- settled{aborted: aborted}
	}
}

func (c *Client) write(payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(encoded)
	return err
}

func (c *Client) request(ctx context.Context, command string, fields map[string]any) (map[string]any, json.RawMessage, error) {
	id := fmt.Sprintf("viewer-%d", c.nextID.Add(1))
	waiter := make(chan response, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, nil, errors.New("pi RPC client is closed")
	}
	c.pending[id] = waiter
	c.mu.Unlock()
	payload := map[string]any{"id": id, "type": command}
	for key, value := range fields {
		payload[key] = value
	}
	if err := c.write(payload); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, nil, err
	}
	select {
	case value := <-waiter:
		return value.data, value.raw, value.err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, nil, fmt.Errorf("pi RPC %s: %w", command, ctx.Err())
	case <-c.ctx.Done():
		return nil, nil, errors.New("pi RPC client closed")
	}
}

func (c *Client) GetState(ctx context.Context) (State, error) {
	data, _, err := c.request(ctx, "get_state", nil)
	if err != nil {
		return State{}, err
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(encoded, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (c *Client) SwitchSession(ctx context.Context, sessionPath string) error {
	data, _, err := c.request(ctx, "switch_session", map[string]any{"sessionPath": sessionPath})
	if err != nil {
		return err
	}
	if cancelled, _ := data["cancelled"].(bool); cancelled {
		return errors.New("Pi cancelled the session switch")
	}
	return nil
}

func (c *Client) SetModel(ctx context.Context, provider, modelID string) error {
	_, _, err := c.request(ctx, "set_model", map[string]any{"provider": provider, "modelId": modelID})
	return err
}

func (c *Client) SetThinkingLevel(ctx context.Context, level string) error {
	_, _, err := c.request(ctx, "set_thinking_level", map[string]any{"level": level})
	return err
}

func (c *Client) GetAvailableModels(ctx context.Context) ([]Model, error) {
	data, _, err := c.request(ctx, "get_available_models", nil)
	if err != nil {
		return nil, err
	}
	items, _ := data["models"].([]any)
	models := make([]Model, 0, len(items))
	for _, item := range items {
		object, _ := item.(map[string]any)
		model := Model{Provider: StringField(object, "provider"), ID: StringField(object, "id")}
		if window, ok := object["contextWindow"].(float64); ok {
			model.ContextWindow = int64(window)
		}
		if model.Provider != "" && model.ID != "" {
			models = append(models, model)
		}
	}
	return models, nil
}

// PromptAndWait accepts a prompt and waits for agent_settled, not agent_end:
// Pi may still perform retries, compaction recovery or queued work after the
// low-level agent run ends. A handled extension command has no run to await.
func (c *Client) PromptAndWait(ctx context.Context, text string) (PromptResult, error) {
	c.promptMu.Lock()
	defer c.promptMu.Unlock()
	waiter := make(chan settled, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return PromptResult{}, errors.New("pi RPC client is closed")
	}
	c.settledWaiter = waiter
	c.turnErr = ""
	c.mu.Unlock()
	clearWaiter := func() {
		c.mu.Lock()
		c.settledWaiter = nil
		c.mu.Unlock()
	}
	data, _, err := c.request(ctx, "prompt", map[string]any{"message": text})
	if err != nil {
		clearWaiter()
		return PromptResult{}, err
	}
	disposition, _ := data["disposition"].(string)
	result := PromptResult{Disposition: disposition}
	if disposition == "handled" {
		clearWaiter()
		return result, nil
	}
	select {
	case completed := <-waiter:
		clearWaiter()
		result.Aborted = completed.aborted
		if completed.err != nil {
			return result, completed.err
		}
		c.mu.Lock()
		turnErr := c.turnErr
		c.mu.Unlock()
		if turnErr != "" {
			return result, errors.New(turnErr)
		}
		return result, nil
	case <-ctx.Done():
		clearWaiter()
		return result, ctx.Err()
	case <-c.ctx.Done():
		clearWaiter()
		return result, errors.New("pi RPC client closed")
	}
}

func (c *Client) Abort(ctx context.Context) error {
	_, _, err := c.request(ctx, "abort", nil)
	return err
}

func (c *Client) GetSessionStats(ctx context.Context) (map[string]any, json.RawMessage, error) {
	return c.request(ctx, "get_session_stats", nil)
}

func (c *Client) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, waiter := range c.pending {
		waiter <- response{err: err}
		delete(c.pending, id)
	}
	if c.settledWaiter != nil {
		c.settledWaiter <- settled{err: err}
	}
}

func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	c.cancel()
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	select {
	case <-c.readerDone:
	case <-time.After(2 * time.Second):
	}
	return nil
}

func StringField(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := object[key].(string); ok {
			return text
		}
	}
	return ""
}
