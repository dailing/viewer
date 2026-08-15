// Package codexserver implements the bounded Codex App Server JSONL protocol subset used by chat.
package codexserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

const maxLineBytes = 16 << 20

type ProcessConfig struct {
	Command   string
	Arguments []string
	YOLO      bool
}

type Update struct {
	ThreadID string
	Method   string
	Params   map[string]any
	Raw      json.RawMessage
}

type response struct {
	result map[string]any
	err    error
}

type Client struct {
	config      ProcessConfig
	ctx         context.Context
	cancel      context.CancelFunc
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	mu          sync.Mutex
	writeMu     sync.Mutex
	pending     map[int64]chan response
	turnWaiters map[string]chan map[string]any
	completed   map[string]map[string]any
	bound       map[string]bool
	onUpdate    func(Update)
	nextID      atomic.Int64
	closed      bool
	readerDone  chan struct{}
}

func New(ctx context.Context, config ProcessConfig) (*Client, error) {
	if config.Command == "" {
		config.Command = "codex"
	}
	runCtx, cancel := context.WithCancel(ctx)
	client := &Client{config: config, ctx: runCtx, cancel: cancel, pending: map[int64]chan response{}, turnWaiters: map[string]chan map[string]any{}, completed: map[string]map[string]any{}, bound: map[string]bool{}, readerDone: make(chan struct{})}
	client.cmd = exec.CommandContext(runCtx, config.Command, config.Arguments...)
	stdout, err := client.cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdin, err := client.cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	client.stdin = stdin
	if err = client.cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	go client.readLoop(stdout)
	initCtx, initCancel := context.WithTimeout(runCtx, 30*time.Second)
	defer initCancel()
	if _, err = client.request(initCtx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "viewer_chat", "title": "Viewer Chat", "version": "0.1.0"}}); err != nil {
		_ = client.Close()
		return nil, err
	}
	if err = client.notify("initialized", map[string]any{}); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func (c *Client) OnUpdate(callback func(Update)) { c.mu.Lock(); c.onUpdate = callback; c.mu.Unlock() }

func (c *Client) readLoop(reader io.Reader) {
	defer close(c.readerDone)
	buffered := bufio.NewReaderSize(reader, 64*1024)
	for {
		line, err := buffered.ReadBytes('\n')
		if len(line) > maxLineBytes {
			c.failAll(fmt.Errorf("codex app-server line exceeds %d bytes", maxLineBytes))
			c.cancel()
			return
		}
		if len(line) > 0 {
			var message map[string]any
			if json.Unmarshal(line, &message) == nil {
				c.handleMessage(message, append(json.RawMessage(nil), line...))
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.failAll(err)
			} else {
				c.failAll(errors.New("codex app-server stdout closed"))
			}
			return
		}
	}
}

func (c *Client) handleMessage(message map[string]any, raw json.RawMessage) {
	if rawID, ok := message["id"]; ok {
		id, valid := numberID(rawID)
		if _, hasMethod := message["method"]; hasMethod {
			_ = c.write(map[string]any{"id": rawID, "error": map[string]any{"code": -32601, "message": "Viewer does not support app-server request"}})
			return
		}
		if valid {
			c.mu.Lock()
			waiter := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if waiter != nil {
				if rpcError, exists := message["error"]; exists {
					waiter <- response{err: fmt.Errorf("codex app-server RPC error: %v", rpcError)}
				} else {
					result, _ := message["result"].(map[string]any)
					waiter <- response{result: result}
				}
			}
		}
		return
	}
	method, _ := message["method"].(string)
	params, _ := message["params"].(map[string]any)
	threadID := stringValue(params, "threadId", "thread_id")
	c.mu.Lock()
	callback := c.onUpdate
	c.mu.Unlock()
	if callback != nil && method != "" {
		callback(Update{ThreadID: threadID, Method: method, Params: params, Raw: raw})
	}
	if method == "turn/completed" {
		turn, _ := params["turn"].(map[string]any)
		turnID := stringValue(turn, "id")
		if turnID != "" {
			c.mu.Lock()
			waiter := c.turnWaiters[turnID]
			delete(c.turnWaiters, turnID)
			if waiter == nil {
				c.completed[turnID] = turn
			}
			c.mu.Unlock()
			if waiter != nil {
				waiter <- turn
			}
		}
	}
}

func numberID(value any) (int64, bool) { number, ok := value.(float64); return int64(number), ok }
func stringValue(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := value[key].(string); ok {
			return text
		}
	}
	return ""
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
func (c *Client) notify(method string, params map[string]any) error {
	return c.write(map[string]any{"method": method, "params": params})
}
func (c *Client) request(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	id := c.nextID.Add(1)
	waiter := make(chan response, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("codex app-server is closed")
	}
	c.pending[id] = waiter
	c.mu.Unlock()
	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case value := <-waiter:
		return value.result, value.err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("codex app-server %s: %w", method, ctx.Err())
	case <-c.ctx.Done():
		return nil, errors.New("codex app-server closed")
	}
}

func (c *Client) ThreadStart(ctx context.Context, cwd, model string) (string, error) {
	params := map[string]any{"cwd": cwd}
	if model != "" {
		params["model"] = model
	}
	result, err := c.request(ctx, "thread/start", params)
	if err != nil {
		return "", err
	}
	thread, _ := result["thread"].(map[string]any)
	id := stringValue(thread, "id")
	if id == "" {
		id = stringValue(result, "threadId")
	}
	if id == "" {
		return "", errors.New("codex app-server thread/start returned no thread id")
	}
	c.mu.Lock()
	c.bound[id] = true
	c.mu.Unlock()
	return id, nil
}

// ModelList returns the App Server's model/list result for best-effort catalog
// discovery. Callers deliberately own compatibility parsing of the result.
func (c *Client) ModelList(ctx context.Context) (map[string]any, error) {
	return c.request(ctx, "model/list", map[string]any{})
}
func (c *Client) ThreadResume(ctx context.Context, threadID, cwd string) error {
	c.mu.Lock()
	bound := c.bound[threadID]
	c.mu.Unlock()
	if bound {
		return nil
	}
	result, err := c.request(ctx, "thread/resume", map[string]any{"threadId": threadID, "cwd": cwd})
	if err != nil {
		return err
	}
	thread, _ := result["thread"].(map[string]any)
	resumed := stringValue(thread, "id")
	if resumed != "" && resumed != threadID {
		return fmt.Errorf("codex resumed unexpected thread %s", resumed)
	}
	c.mu.Lock()
	c.bound[threadID] = true
	c.mu.Unlock()
	return nil
}
func (c *Client) TurnStart(ctx context.Context, threadID, prompt, model string) (map[string]any, error) {
	params := map[string]any{"threadId": threadID, "input": []map[string]any{{"type": "text", "text": prompt}}}
	if model != "" {
		params["model"] = model
	}
	if c.config.YOLO {
		params["approvalPolicy"] = "never"
		params["sandboxPolicy"] = map[string]any{"type": "dangerFullAccess"}
	}
	result, err := c.request(ctx, "turn/start", params)
	if err != nil {
		return nil, err
	}
	turn, _ := result["turn"].(map[string]any)
	id := stringValue(turn, "id")
	if id == "" {
		return nil, errors.New("codex app-server turn/start returned no turn id")
	}
	status := stringValue(turn, "status")
	if status == "completed" || status == "failed" || status == "interrupted" {
		return turn, nil
	}
	c.mu.Lock()
	if completed := c.completed[id]; completed != nil {
		delete(c.completed, id)
		c.mu.Unlock()
		return completed, nil
	}
	waiter := make(chan map[string]any, 1)
	c.turnWaiters[id] = waiter
	c.mu.Unlock()
	select {
	case completed := <-waiter:
		return completed, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.turnWaiters, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, errors.New("codex app-server closed")
	}
}
func (c *Client) TurnInterrupt(ctx context.Context, threadID string) error {
	_, err := c.request(ctx, "turn/interrupt", map[string]any{"threadId": threadID})
	return err
}

func (c *Client) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, waiter := range c.pending {
		waiter <- response{err: err}
		delete(c.pending, id)
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
