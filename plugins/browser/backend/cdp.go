package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

type cdpCall struct {
	id     int64
	result chan cdpOutcome
}

type cdpOutcome struct {
	result json.RawMessage
	err    error
}

type cdpConn struct {
	conn *websocket.Conn

	next  atomic.Int64
	mu    sync.Mutex
	calls map[int64]chan cdpOutcome
	done  chan struct{}
	err   error
}

func dialCDP(ctx context.Context, url string) (*cdpConn, error) {
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(16 << 20)
	client := &cdpConn{conn: conn, calls: make(map[int64]chan cdpOutcome), done: make(chan struct{})}
	go client.readLoop()
	return client, nil
}

func (c *cdpConn) readLoop() {
	for {
		kind, data, err := c.conn.Read(context.Background())
		if err != nil {
			c.finish(err)
			return
		}
		if kind != websocket.MessageText {
			continue
		}
		var message struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &message) != nil || message.ID == 0 {
			continue
		}
		c.mu.Lock()
		result := c.calls[message.ID]
		delete(c.calls, message.ID)
		c.mu.Unlock()
		if result == nil {
			continue
		}
		if message.Error != nil {
			result <- cdpOutcome{err: fmt.Errorf("CDP %d: %s", message.Error.Code, message.Error.Message)}
		} else {
			result <- cdpOutcome{result: message.Result}
		}
	}
}

func (c *cdpConn) finish(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	calls := c.calls
	c.calls = make(map[int64]chan cdpOutcome)
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	c.mu.Unlock()
	for _, result := range calls {
		result <- cdpOutcome{err: err}
	}
}

func (c *cdpConn) call(ctx context.Context, method string, params any, target json.RawMessage) (json.RawMessage, error) {
	id := c.next.Add(1)
	result := make(chan cdpOutcome, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	c.calls[id] = result
	c.mu.Unlock()
	message := map[string]any{"id": id, "method": method, "params": params}
	data, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		c.mu.Lock()
		delete(c.calls, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case outcome := <-result:
		return outcome.result, outcome.err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.calls, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.done:
		return nil, errors.New("CDP connection closed")
	}
}

func (c *cdpConn) close() {
	_ = c.conn.Close(websocket.StatusNormalClosure, "browser plugin stopping")
	c.finish(errors.New("CDP connection closed"))
}

func cdpContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, timeout)
}
