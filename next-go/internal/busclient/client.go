package busclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"viewer/internal/protocol"
)

type pendingRPC struct {
	result chan rpcResult
}

type rpcResult struct {
	value any
	err   error
}

// Client is a concurrency-safe connection to the Viewer bus.
type Client struct {
	url        string
	manifest   Manifest
	managed    bool
	instanceID *string

	requestTimeout time.Duration
	reconnect      bool
	backoffBase    time.Duration
	backoffCap     time.Duration
	dial           dialTransport

	mu            sync.RWMutex
	writeMu       sync.Mutex
	transport     transport
	conn          string
	connected     bool
	started       bool
	closed        bool
	subscriptions map[string]map[uint64]*Subscription
	nextSubID     uint64
	pending       map[string]*pendingRPC
	errorHandlers []func(ErrorEntry)
	stateHandlers []func(ConnectionState)
	connectWG     sync.WaitGroup
	running       bool

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// New constructs a disconnected bus client.
func New(url string, manifest Manifest, opts ...Option) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		url: url, manifest: manifest, requestTimeout: DefaultRequestTimeout,
		reconnect: true, backoffBase: 500 * time.Millisecond, backoffCap: 30 * time.Second,
		dial: dialWebSocket, subscriptions: make(map[string]map[uint64]*Subscription),
		pending: make(map[string]*pendingRPC), ctx: ctx, cancel: cancel, done: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *Client) Connected() bool { c.mu.RLock(); defer c.mu.RUnlock(); return c.connected }
func (c *Client) Conn() string    { c.mu.RLock(); defer c.mu.RUnlock(); return c.conn }

func (c *Client) ErrorChannel() string {
	conn := c.Conn()
	if conn == "" {
		return ""
	}
	return "_conn:" + conn + ":error"
}

// OnError registers a callback for protocol-error mailbox entries.
func (c *Client) OnError(callback func(ErrorEntry)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errorHandlers = append(c.errorHandlers, callback)
}

// OnStateChange registers a callback for future lifecycle transitions.
func (c *Client) OnStateChange(callback func(ConnectionState)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stateHandlers = append(c.stateHandlers, callback)
}

// Connect dials the WebSocket and waits until the kernel registry confirms the
// hello. A client may be connected only once.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	if c.started {
		c.mu.Unlock()
		return errors.New("bus client already started")
	}
	c.started = true
	c.connectWG.Add(1)
	c.mu.Unlock()
	defer c.connectWG.Done()
	c.emitState(StateConnecting)

	connectCtx, cancel := context.WithCancel(ctx)
	stopCancel := context.AfterFunc(c.ctx, cancel)
	defer func() {
		stopCancel()
		cancel()
	}()
	t, conn, err := c.open(connectCtx)
	if err != nil {
		c.mu.Lock()
		c.started = false
		c.mu.Unlock()
		if c.isClosed() {
			return ErrClosed
		}
		return err
	}
	if !c.install(t, conn) {
		return ErrClosed
	}
	if err := c.replayCurrent(connectCtx); err != nil {
		c.disconnect(t, err)
		c.mu.Lock()
		c.started = false
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.disconnect(t, ErrClosed)
		return ErrClosed
	}
	c.running = true
	c.mu.Unlock()
	go c.run(t)
	return nil
}

func (c *Client) install(t transport, conn string) bool {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = t.Close()
		return false
	}
	c.transport, c.conn, c.connected = t, conn, true
	c.mu.Unlock()
	c.emitState(StateConnected)
	return true
}

// Close permanently stops reconnecting and closes all handler goroutines.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	t := c.transport
	subs := make([]*Subscription, 0)
	for _, byID := range c.subscriptions {
		for _, sub := range byID {
			subs = append(subs, sub)
		}
	}
	c.mu.Unlock()
	c.cancel()
	if t != nil {
		_ = t.Close()
	}
	c.connectWG.Wait()
	c.mu.RLock()
	running := c.running
	c.mu.RUnlock()
	if running {
		<-c.done
	}
	for _, sub := range subs {
		sub.stop()
	}
	c.emitState(StateClosed)
	return nil
}

func (c *Client) Publish(ctx context.Context, channel string, value any) error {
	return c.send(ctx, map[string]any{"type": "publish", "channel": channel, "value": value})
}

func (c *Client) Set(ctx context.Context, channel string, value any) error {
	return c.send(ctx, map[string]any{"type": "set", "channel": channel, "value": value})
}

// Subscribe registers a handler and immediately subscribes when connected.
func (c *Client) Subscribe(pattern string, handler func(Frame)) (*Subscription, error) {
	if err := protocol.ValidatePattern(pattern); err != nil {
		return nil, err
	}
	if handler == nil {
		return nil, errors.New("subscription handler must not be nil")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	c.nextSubID++
	sub := newSubscription(c, c.nextSubID, pattern, handler)
	byID := c.subscriptions[pattern]
	first := len(byID) == 0
	if byID == nil {
		byID = make(map[uint64]*Subscription)
		c.subscriptions[pattern] = byID
	}
	byID[sub.id] = sub
	connected := c.connected
	c.mu.Unlock()
	if first && connected {
		if err := c.send(c.ctx, map[string]any{"type": "subscribe", "pattern": pattern}); err != nil && !errors.Is(err, ErrConnectionLost) {
			_ = c.unsubscribe(context.Background(), sub)
			return nil, err
		}
	}
	return sub, nil
}

func (c *Client) unsubscribe(ctx context.Context, sub *Subscription) error {
	c.mu.Lock()
	byID := c.subscriptions[sub.pattern]
	if _, exists := byID[sub.id]; !exists {
		c.mu.Unlock()
		return nil
	}
	delete(byID, sub.id)
	last := len(byID) == 0
	if last {
		delete(c.subscriptions, sub.pattern)
	}
	connected := c.connected
	c.mu.Unlock()
	sub.stop()
	if last && connected {
		return c.send(ctx, map[string]any{"type": "unsubscribe", "pattern": sub.pattern})
	}
	return nil
}

// Request performs inbox-convention RPC. An optional timeout overrides the
// configured 30-second default; a shorter context deadline always wins.
func (c *Client) Request(ctx context.Context, channel string, payload any, timeout ...time.Duration) (any, error) {
	duration := c.requestTimeout
	if len(timeout) > 0 && timeout[0] > 0 {
		duration = timeout[0]
	}
	c.mu.RLock()
	conn, connected := c.conn, c.connected
	c.mu.RUnlock()
	if conn == "" || !connected {
		return nil, ErrNotConnected
	}
	corr, err := correlationID()
	if err != nil {
		return nil, err
	}
	inbox := "_inbox:" + conn + ":" + corr
	pending := &pendingRPC{result: make(chan rpcResult, 1)}
	c.mu.Lock()
	if !c.connected || c.conn != conn {
		c.mu.Unlock()
		return nil, ErrConnectionLost
	}
	c.pending[corr] = pending
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, corr)
		c.mu.Unlock()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.send(cleanupCtx, map[string]any{"type": "unsubscribe", "pattern": inbox})
	}()
	if err := c.send(ctx, map[string]any{"type": "subscribe", "pattern": inbox}); err != nil {
		return nil, err
	}
	requestPayload := rpcPayload(payload)
	requestPayload["_reply_to"], requestPayload["_corr"] = inbox, corr
	if err := c.Publish(ctx, channel, requestPayload); err != nil {
		return nil, err
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case result := <-pending.result:
		return result.value, result.err
	case <-ctx.Done():
		c.bestEffortCancel(channel, corr)
		return nil, ctx.Err()
	case <-timer.C:
		c.bestEffortCancel(channel, corr)
		return nil, &RequestTimeoutError{Channel: channel, Corr: corr, Timeout: duration}
	}
}

// Cancel publishes the protocol's best-effort _cancel request.
func (c *Client) Cancel(ctx context.Context, channel, corr string) error {
	return c.Publish(ctx, channel, map[string]any{"_corr": corr, "_cancel": true})
}

func (c *Client) bestEffortCancel(channel, corr string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.Cancel(ctx, channel, corr)
}

func rpcPayload(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if object, ok := value.(map[string]any); ok {
		copy := make(map[string]any, len(object)+2)
		for key, item := range object {
			copy[key] = item
		}
		return copy
	}
	return map[string]any{"value": value}
}

func (c *Client) send(ctx context.Context, frame any) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("encode bus frame: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.RLock()
	t, connected, closed := c.transport, c.connected, c.closed
	c.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	if t == nil || !connected {
		return ErrNotConnected
	}
	if err := t.WriteFrame(ctx, data); err != nil {
		return fmt.Errorf("%w: %v", ErrConnectionLost, err)
	}
	return nil
}

func (c *Client) run(t transport) {
	defer close(c.done)
	attempt := 0
	for {
		err := c.readLoop(t)
		c.disconnect(t, err)
		if c.isClosed() || !c.reconnect {
			return
		}
		attempt++
		delay := c.backoffBase
		for i := 1; i < attempt && delay < c.backoffCap; i++ {
			delay *= 2
			if delay > c.backoffCap {
				delay = c.backoffCap
			}
		}
		select {
		case <-time.After(delay):
		case <-c.ctx.Done():
			return
		}
		c.emitState(StateConnecting)
		newTransport, conn, openErr := c.open(c.ctx)
		if openErr != nil {
			continue
		}
		t = newTransport
		if !c.install(t, conn) {
			return
		}
		if replayErr := c.replayCurrent(c.ctx); replayErr != nil {
			c.disconnect(t, replayErr)
			continue
		}
		attempt = 0
	}
}

func (c *Client) readLoop(t transport) error {
	for {
		data, err := t.ReadFrame(c.ctx)
		if err != nil {
			return err
		}
		var frame Frame
		if err := jsonUnmarshal(data, &frame); err != nil {
			continue
		}
		frame.Raw = append(json.RawMessage(nil), data...)
		c.dispatch(frame)
	}
}

func (c *Client) disconnect(t transport, cause error) {
	c.mu.Lock()
	if c.transport != t {
		c.mu.Unlock()
		return
	}
	c.transport, c.connected = nil, false
	pending := c.pending
	c.pending = make(map[string]*pendingRPC)
	closed := c.closed
	c.mu.Unlock()
	_ = t.Close()
	for _, request := range pending {
		select {
		case request.result <- rpcResult{err: fmt.Errorf("%w: %v", ErrConnectionLost, cause)}:
		default:
		}
	}
	if !closed {
		c.emitState(StateDisconnected)
	}
}

func (c *Client) open(ctx context.Context) (transport, string, error) {
	t, err := c.dial(ctx, c.url)
	if err != nil {
		return nil, "", err
	}
	conn, err := uuidV4()
	if err != nil {
		_ = t.Close()
		return nil, "", err
	}
	hello := map[string]any{"type": "hello", "protocol_version": protocol.Version, "conn": conn, "manifest": c.manifest, "managed": c.managed}
	if c.instanceID != nil {
		hello["instance_id"] = *c.instanceID
	}
	frames := []any{hello, map[string]any{"type": "subscribe", "pattern": "_conn:" + conn + ":error"}}
	patterns := c.patterns()
	for _, pattern := range patterns {
		frames = append(frames, map[string]any{"type": "subscribe", "pattern": pattern})
	}
	barrierOwned := !contains(patterns, "plugins:_:list")
	if barrierOwned {
		frames = append(frames, map[string]any{"type": "subscribe", "pattern": "plugins:_:list"})
	}
	for _, frame := range frames {
		data, marshalErr := json.Marshal(frame)
		if marshalErr != nil {
			_ = t.Close()
			return nil, "", marshalErr
		}
		if writeErr := t.WriteFrame(ctx, data); writeErr != nil {
			_ = t.Close()
			return nil, "", helloError(writeErr)
		}
	}
	for {
		data, readErr := t.ReadFrame(ctx)
		if readErr != nil {
			_ = t.Close()
			return nil, "", helloError(readErr)
		}
		var frame Frame
		if jsonUnmarshal(data, &frame) != nil {
			continue
		}
		frame.Raw = append(json.RawMessage(nil), data...)
		c.dispatch(frame)
		if frame.Channel == "plugins:_:list" && registryContains(frame.Value, conn) {
			break
		}
	}
	if barrierOwned {
		data, _ := json.Marshal(map[string]any{"type": "unsubscribe", "pattern": "plugins:_:list"})
		if err := t.WriteFrame(ctx, data); err != nil {
			_ = t.Close()
			return nil, "", helloError(err)
		}
	}
	return t, conn, nil
}

func (c *Client) patterns() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	patterns := make([]string, 0, len(c.subscriptions))
	for pattern := range c.subscriptions {
		patterns = append(patterns, pattern)
	}
	return patterns
}

func (c *Client) dispatch(frame Frame) {
	c.mu.Lock()
	conn := c.conn
	errorHandlers := append([]func(ErrorEntry){}, c.errorHandlers...)
	stateSubs := make([]*Subscription, 0)
	for pattern, byID := range c.subscriptions {
		if protocol.ChannelMatches(pattern, frame.Channel) {
			for _, sub := range byID {
				stateSubs = append(stateSubs, sub)
			}
		}
	}
	var pending *pendingRPC
	if value, ok := frame.Value.(map[string]any); ok && strings.HasPrefix(frame.Channel, "_inbox:") {
		if corr, ok := value["_corr"].(string); ok {
			pending = c.pending[corr]
			delete(c.pending, corr)
		}
	}
	c.mu.Unlock()
	if frame.Channel == "_conn:"+conn+":error" {
		data, _ := json.Marshal(frame.Value)
		var entry ErrorEntry
		if jsonUnmarshal(data, &entry) == nil {
			for _, callback := range errorHandlers {
				go c.safeCallback("error", func() { callback(entry) })
			}
		}
	}
	if pending != nil {
		value, _ := frame.Value.(map[string]any)
		if okValue, exists := value["ok"]; exists && okValue == false {
			errorObject, _ := value["error"].(map[string]any)
			pending.result <- rpcResult{err: &RPCError{Code: stringValue(errorObject["code"], "error"), Message: stringValue(errorObject["message"], "")}}
		} else {
			pending.result <- rpcResult{value: value["result"]}
		}
	}
	for _, sub := range stateSubs {
		sub.enqueue(frame)
	}
}

func (c *Client) emitState(state ConnectionState) {
	c.mu.RLock()
	callbacks := append([]func(ConnectionState){}, c.stateHandlers...)
	c.mu.RUnlock()
	for _, callback := range callbacks {
		go c.safeCallback("state", func() { callback(state) })
	}
}

func (c *Client) safeCallback(kind string, callback func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("plugin callback panic", "plugin", c.manifest.ID, "callback", kind, "panic", recovered, "stack", string(debug.Stack()))
		}
	}()
	callback()
}

func (c *Client) isClosed() bool { c.mu.RLock(); defer c.mu.RUnlock(); return c.closed }

// replayCurrent closes the small race where Subscribe is called while a
// connection attempt is completing. Duplicate subscribe frames are idempotent
// in the kernel and do not replay retained values twice.
func (c *Client) replayCurrent(ctx context.Context) error {
	for _, pattern := range c.patterns() {
		if err := c.send(ctx, map[string]any{"type": "subscribe", "pattern": pattern}); err != nil {
			return err
		}
	}
	return nil
}

func registryContains(value any, conn string) bool {
	entries, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range entries {
		if entry, ok := item.(map[string]any); ok && entry["conn"] == conn {
			return true
		}
	}
	return false
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func stringValue(value any, fallback string) string {
	if text, ok := value.(string); ok {
		return text
	}
	return fallback
}
func jsonUnmarshal(data []byte, target any) error {
	return json.Unmarshal(data, target)
}

func uuidV4() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate UUIDv4: %w", err)
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	hexValue := hex.EncodeToString(bytes[:])
	return hexValue[:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:], nil
}

func correlationID() (string, error) {
	id, err := uuidV4()
	if err != nil {
		return "", err
	}
	return id[:8] + id[9:13] + id[14:18] + id[19:23] + id[24:], nil
}
