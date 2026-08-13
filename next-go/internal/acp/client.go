// Package acp implements the small ACP-over-stdio subset used by Viewer chat.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
func New(ctx context.Context, command string, arguments ...string) (*Client, error) {
	cmd := exec.CommandContext(ctx, command, arguments...)
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
func (c *Client) NewSession(ctx context.Context, cwd string) (string, error) {
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.request(ctx, "session/new", map[string]any{"cwd": cwd}, &result); err != nil {
		return "", err
	}
	if result.SessionID == "" {
		return "", errors.New("ACP session/new returned no sessionId")
	}
	return result.SessionID, nil
}

// LoadSession binds a persisted provider session to this new transport.
func (c *Client) LoadSession(ctx context.Context, sessionID, cwd string) error {
	var result any
	if err := c.request(ctx, "session/load", map[string]any{"sessionId": sessionID, "cwd": cwd}, &result); err != nil {
		return err
	}
	if result == nil {
		return errors.New("ACP session/load returned null")
	}
	return nil
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
	if len(envelope.ID) > 0 && string(envelope.ID) != "null" {
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
