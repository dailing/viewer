package busclient

import (
	"context"
	"errors"
	"fmt"

	"github.com/coder/websocket"
)

// transport is deliberately the complete boundary between bus semantics and
// I/O. TODO(framework §17): add the in-process transport used by core plugins;
// its frames must remain indistinguishable from WebSocket frames.
type transport interface {
	ReadFrame(context.Context) ([]byte, error)
	WriteFrame(context.Context, []byte) error
	Close() error
}

type dialTransport func(context.Context, string) (transport, error)

type webSocketTransport struct{ conn *websocket.Conn }

func dialWebSocket(ctx context.Context, url string) (transport, error) {
	conn, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if response != nil {
			return nil, fmt.Errorf("websocket dial failed (HTTP %s): %w", response.Status, err)
		}
		return nil, fmt.Errorf("websocket dial failed: %w", err)
	}
	// Match the kernel/gateway accept side: no read limit. The default 32KiB
	// limit kills the connection (and any in-flight RPC) when a large frame —
	// e.g. an oversized prompt or agent event — is routed back to this plugin.
	conn.SetReadLimit(-1)
	return &webSocketTransport{conn: conn}, nil
}

func (t *webSocketTransport) ReadFrame(ctx context.Context) ([]byte, error) {
	kind, data, err := t.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if kind != websocket.MessageText {
		return nil, errors.New("bus sent a non-text WebSocket frame")
	}
	return data, nil
}

func (t *webSocketTransport) WriteFrame(ctx context.Context, data []byte) error {
	return t.conn.Write(ctx, websocket.MessageText, data)
}

func (t *webSocketTransport) Close() error {
	return t.conn.Close(websocket.StatusNormalClosure, "client closing")
}

func helloError(err error) error {
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) {
		return err
	}
	code := int(closeErr.Code)
	if code != 4001 && code != 4002 && code != 4003 {
		return err
	}
	message := closeErr.Reason
	var reason struct {
		Message string `json:"message"`
	}
	if jsonUnmarshal([]byte(closeErr.Reason), &reason) == nil && reason.Message != "" {
		message = reason.Message
	}
	return &HelloError{Code: code, Message: message}
}
