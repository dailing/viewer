// Package busclient connects Go plugins to the Viewer message bus.
package busclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"viewer/sdk/go/protocol"
)

const DefaultRequestTimeout = 30 * time.Second

// Manifest is the plugin identity and declared bus surface sent during hello.
type Manifest = protocol.Manifest

// Origin is stamped by the kernel on every delivered frame.
type Origin = protocol.Origin

// Frame is a publish or set frame delivered by the kernel.
type Frame struct {
	Type    string          `json:"type"`
	Channel string          `json:"channel"`
	Value   any             `json:"value"`
	TraceID string          `json:"trace_id,omitempty"`
	Depth   int             `json:"depth,omitempty"`
	TS      int64           `json:"ts,omitempty"`
	Origin  *Origin         `json:"origin,omitempty"`
	Raw     json.RawMessage `json:"-"`
}

// ErrorEntry is delivered through the connection's protocol-error mailbox.
type ErrorEntry struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	TS      int64          `json:"ts,omitempty"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// ConnectionState describes a client lifecycle transition.
type ConnectionState string

const (
	StateConnecting   ConnectionState = "connecting"
	StateConnected    ConnectionState = "connected"
	StateDisconnected ConnectionState = "disconnected"
	StateClosed       ConnectionState = "closed"
)

var (
	ErrNotConnected   = errors.New("not connected to the bus")
	ErrConnectionLost = errors.New("bus connection lost")
	ErrClosed         = errors.New("bus client is closed")
	ErrRequestTimeout = errors.New("bus request timed out")
)

// HelloError reports a fatal WebSocket close during the hello handshake.
type HelloError struct {
	Code    int
	Message string
}

func (e *HelloError) Error() string {
	return fmt.Sprintf("bus hello rejected (%d): %s", e.Code, e.Message)
}

// Named hello rejection errors support errors.Is as well as errors.As.
var (
	ErrFirstFrameNotHello = &HelloError{Code: 4001, Message: "first frame must be hello"}
	ErrInvalidHello       = &HelloError{Code: 4002, Message: "hello schema validation failed"}
	ErrProtocolMismatch   = &HelloError{Code: 4003, Message: "protocol version mismatch"}
)

func (e *HelloError) Is(target error) bool {
	other, ok := target.(*HelloError)
	return ok && e.Code == other.Code
}

// RPCError is an ok:false response from the callee.
type RPCError struct {
	Code    string
	Message string
}

func (e *RPCError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// RequestTimeoutError identifies the request which exceeded its deadline.
type RequestTimeoutError struct {
	Channel string
	Corr    string
	Timeout time.Duration
}

func (e *RequestTimeoutError) Error() string {
	return fmt.Sprintf("rpc timeout after %s on %s (corr %s)", e.Timeout, e.Channel, e.Corr)
}

func (e *RequestTimeoutError) Unwrap() error { return ErrRequestTimeout }

// Option configures a Client.
type Option func(*Client)

func WithManaged(managed bool) Option { return func(c *Client) { c.managed = managed } }

func WithInstanceID(instanceID string) Option {
	return func(c *Client) { c.instanceID = &instanceID }
}

func WithRequestTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		if timeout > 0 {
			c.requestTimeout = timeout
		}
	}
}

func WithReconnect(enabled bool) Option { return func(c *Client) { c.reconnect = enabled } }

func WithBackoff(base, cap time.Duration) Option {
	return func(c *Client) {
		if base > 0 {
			c.backoffBase = base
		}
		if cap > 0 {
			c.backoffCap = cap
		}
	}
}
