package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"viewer/sdk/go/protocol"
)

type Connection struct {
	Conn string

	queue chan protocol.Delivery

	priorityMu sync.Mutex
	priority   *protocol.Delivery
	priorityCh chan struct{}
	dropped    int64
	lastNotice int64
}

func newConnection(conn string, queueSize int) *Connection {
	return &Connection{
		Conn:       conn,
		queue:      make(chan protocol.Delivery, queueSize),
		priorityCh: make(chan struct{}, 1),
	}
}

func (c *Connection) enqueue(frame protocol.Delivery) bool {
	select {
	case c.queue <- frame:
		return true
	default:
		c.dropped++
		return false
	}
}

func (c *Connection) setPriority(frame protocol.Delivery) {
	c.priorityMu.Lock()
	c.priority = &frame
	c.priorityMu.Unlock()
	select {
	case c.priorityCh <- struct{}{}:
	default:
	}
}

func (c *Connection) takePriority() (protocol.Delivery, bool) {
	c.priorityMu.Lock()
	defer c.priorityMu.Unlock()
	if c.priority == nil {
		return protocol.Delivery{}, false
	}
	frame := *c.priority
	c.priority = nil
	select {
	case <-c.priorityCh:
	default:
	}
	return frame, true
}

func (c *Connection) Next(ctx context.Context) (protocol.Delivery, error) {
	if frame, ok := c.takePriority(); ok {
		return frame, nil
	}
	select {
	case <-ctx.Done():
		return protocol.Delivery{}, ctx.Err()
	case <-c.priorityCh:
		if frame, ok := c.takePriority(); ok {
			return frame, nil
		}
		return c.Next(ctx)
	case frame := <-c.queue:
		return frame, nil
	}
}

func (c *Connection) Dropped() int64 { return c.dropped }

type Broker struct {
	mu sync.Mutex

	queueSize     int
	connections   map[string]*Connection
	subscriptions map[string]map[string]struct{}
	mailbox       map[string]protocol.Delivery
	mailboxOrder  []string
	errorCounts   map[string]int64
}

func New(queueSize int) *Broker {
	if queueSize <= 0 {
		queueSize = protocol.DefaultQueueSize
	}
	return &Broker{
		queueSize:     queueSize,
		connections:   make(map[string]*Connection),
		subscriptions: make(map[string]map[string]struct{}),
		mailbox:       make(map[string]protocol.Delivery),
		errorCounts:   make(map[string]int64),
	}
}

func (b *Broker) AddConnection(conn string) (*Connection, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.connections[conn]; exists {
		return nil, fmt.Errorf("connection id already registered: %s", conn)
	}
	state := newConnection(conn, b.queueSize)
	b.connections[conn] = state
	b.subscriptions[conn] = make(map[string]struct{})
	return state, nil
}

func (b *Broker) RemoveConnection(conn string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.connections, conn)
	delete(b.subscriptions, conn)
	errorChannel := fmt.Sprintf("_conn:%s:error", conn)
	if _, exists := b.mailbox[errorChannel]; exists {
		delete(b.mailbox, errorChannel)
		for i, channel := range b.mailboxOrder {
			if channel == errorChannel {
				b.mailboxOrder = append(b.mailboxOrder[:i], b.mailboxOrder[i+1:]...)
				break
			}
		}
	}
	for key := range b.errorCounts {
		if len(key) > len(conn) && key[:len(conn)+1] == conn+"\x00" {
			delete(b.errorCounts, key)
		}
	}
}

func (b *Broker) Subscribe(conn, pattern string) error {
	if err := protocol.ValidatePattern(pattern); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	patterns, exists := b.subscriptions[conn]
	if !exists {
		return fmt.Errorf("connection not registered: %s", conn)
	}
	if _, exists := patterns[pattern]; exists {
		return nil
	}
	// Retained replay and enabling the live subscription share this critical
	// section with Publish, making the handoff lossless and duplicate-free.
	for _, channel := range b.mailboxOrder {
		if protocol.ChannelMatches(pattern, channel) {
			b.enqueueLocked(conn, b.mailbox[channel])
		}
	}
	patterns[pattern] = struct{}{}
	return nil
}

func (b *Broker) Unsubscribe(conn, pattern string) error {
	if err := protocol.ValidatePattern(pattern); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if patterns := b.subscriptions[conn]; patterns != nil {
		delete(patterns, pattern)
	}
	return nil
}

func (b *Broker) Publish(frame protocol.Delivery, sourceConn string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if frame.Depth >= protocol.MaxDepth {
		if sourceConn != "" {
			b.reportErrorLocked(sourceConn, "depth_exceeded", fmt.Sprintf("causal publish depth must be less than %d", protocol.MaxDepth), map[string]any{"channel": frame.Channel, "depth": frame.Depth})
		}
		return false
	}
	if frame.Type == "set" {
		if _, exists := b.mailbox[frame.Channel]; !exists {
			b.mailboxOrder = append(b.mailboxOrder, frame.Channel)
		}
		b.mailbox[frame.Channel] = cloneDelivery(frame)
	}
	matched := false
	for conn, patterns := range b.subscriptions {
		if matchesAny(patterns, frame.Channel) {
			b.enqueueLocked(conn, frame)
			matched = true
		}
	}
	if !matched {
		b.respondNoRouteLocked(frame, sourceConn)
	}
	return true
}

// rpcEnvelope extracts the inbox-RPC markers from a frame value. Parsed only
// on the rare no-route/drop paths, never on the hot fanout path.
type rpcEnvelope struct {
	ReplyTo string `json:"_reply_to"`
	Corr    string `json:"_corr"`
}

func rpcMarkers(value json.RawMessage) rpcEnvelope {
	var envelope rpcEnvelope
	if len(value) > 0 && value[0] == '{' {
		_ = json.Unmarshal(value, &envelope)
	}
	return envelope
}

// respondNoRouteLocked fast-fails an inbox-RPC request whose channel has no
// subscriber — without it the caller hangs until its client-side timeout
// (e.g. a request racing a plugin's re-subscription across a restart).
// Non-RPC frames (no _reply_to/_corr) stay silent: events with zero listeners
// are legitimate.
func (b *Broker) respondNoRouteLocked(frame protocol.Delivery, sourceConn string) {
	envelope := rpcMarkers(frame.Value)
	if envelope.ReplyTo == "" || envelope.Corr == "" {
		return
	}
	slog.Warn("rpc request unrouted: no subscriber", "channel", frame.Channel, "corr", envelope.Corr, "source", sourceConn)
	value, err := json.Marshal(map[string]any{
		"ok":    false,
		"error": map[string]any{"code": "no_route", "message": fmt.Sprintf("no subscriber for %s", frame.Channel)},
		"_corr": envelope.Corr,
	})
	if err != nil {
		return
	}
	response := protocol.Delivery{
		Type: "publish", Channel: envelope.ReplyTo, Value: value,
		TS:     time.Now().UnixMilli(),
		Origin: protocol.Origin{Plugin: protocol.KernelPluginID, Instance: protocol.DefaultInstanceID},
	}
	for conn, patterns := range b.subscriptions {
		if matchesAny(patterns, envelope.ReplyTo) {
			b.enqueueLocked(conn, response)
		}
	}
}

func (b *Broker) ReportError(conn, code, message string, detail any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reportErrorLocked(conn, code, message, detail)
}

func (b *Broker) reportErrorLocked(conn, code, message string, detail any) {
	state := b.connections[conn]
	if state == nil {
		return
	}
	key := conn + "\x00" + code
	b.errorCounts[key]++
	errorDetail := map[string]any{"count": b.errorCounts[key]}
	if values, ok := detail.(map[string]any); ok {
		for name, value := range values {
			errorDetail[name] = value
		}
	} else if detail != nil {
		errorDetail["value"] = detail
	}
	now := time.Now().UnixMilli()
	value, _ := json.Marshal(map[string]any{
		"code": code, "message": message, "ts": now, "detail": errorDetail,
	})
	channel := fmt.Sprintf("_conn:%s:error", conn)
	frame := protocol.Delivery{
		Type: "set", Channel: channel, Value: value, Depth: 0, TS: now,
		Origin: protocol.Origin{Plugin: protocol.KernelPluginID, Instance: protocol.DefaultInstanceID},
	}
	if _, exists := b.mailbox[channel]; !exists {
		b.mailboxOrder = append(b.mailboxOrder, channel)
	}
	b.mailbox[channel] = frame
	if matchesAny(b.subscriptions[conn], channel) {
		state.setPriority(frame)
	}
}

func (b *Broker) enqueueLocked(conn string, frame protocol.Delivery) {
	state := b.connections[conn]
	if state == nil || state.enqueue(cloneDelivery(frame)) {
		return
	}
	if envelope := rpcMarkers(frame.Value); envelope.Corr != "" {
		// A dropped RPC frame means its caller hangs until the client-side
		// timeout — log each one with its correlation id so it stays traceable.
		slog.Warn("dropping rpc frame for slow consumer", "conn", conn, "channel", frame.Channel, "corr", envelope.Corr, "dropped", state.dropped)
	}
	if state.dropped == 1 || state.dropped >= max(2, state.lastNotice*2) {
		state.lastNotice = state.dropped
		b.reportErrorLocked(conn, "slow_consumer", "outbound queue full; new frames were dropped", map[string]any{"dropped": state.dropped})
	}
}

func (b *Broker) GetRetained(channel string) (protocol.Delivery, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	frame, exists := b.mailbox[channel]
	return cloneDelivery(frame), exists
}

func matchesAny(patterns map[string]struct{}, channel string) bool {
	for pattern := range patterns {
		if protocol.ChannelMatches(pattern, channel) {
			return true
		}
	}
	return false
}

func cloneDelivery(frame protocol.Delivery) protocol.Delivery {
	frame.Value = append(json.RawMessage(nil), frame.Value...)
	return frame
}
