package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"viewer/internal/broker"
	"viewer/internal/protocol"
)

type Config struct {
	Host             string
	Port             int
	FrameSize        int
	OutboundQueue    int
	PingInterval     time.Duration
	AutostartCommand []string
}

func DefaultConfig() Config {
	return Config{
		Host: "127.0.0.1", Port: 8765, FrameSize: protocol.DefaultFrameSize,
		OutboundQueue: protocol.DefaultQueueSize, PingInterval: 30 * time.Second,
	}
}

type clientSocket struct {
	ws     *websocket.Conn
	cancel context.CancelFunc
}

type Server struct {
	config   Config
	broker   *broker.Broker
	registry *broker.Registry

	listener net.Listener
	http     *http.Server
	serveErr chan error
	stopping atomic.Bool

	mu       sync.Mutex
	sockets  map[*websocket.Conn]*clientSocket
	handlers sync.WaitGroup
}

func New(config Config) *Server {
	defaults := DefaultConfig()
	if config.Host == "" {
		config.Host = defaults.Host
	}
	if config.FrameSize <= 0 {
		config.FrameSize = defaults.FrameSize
	}
	if config.OutboundQueue <= 0 {
		config.OutboundQueue = defaults.OutboundQueue
	}
	if config.PingInterval <= 0 {
		config.PingInterval = defaults.PingInterval
	}
	b := broker.New(config.OutboundQueue)
	return &Server{
		config: config, broker: b, registry: broker.NewRegistry(b),
		serveErr: make(chan error, 1), sockets: make(map[*websocket.Conn]*clientSocket),
	}
}

func (s *Server) Broker() *broker.Broker { return s.broker }

func (s *Server) Addr() string {
	if s.listener == nil {
		return net.JoinHostPort(s.config.Host, strconv.Itoa(s.config.Port))
	}
	return s.listener.Addr().String()
}

func (s *Server) Port() int {
	if s.listener == nil {
		return s.config.Port
	}
	return s.listener.Addr().(*net.TCPAddr).Port
}

func (s *Server) Start() error {
	listener, err := net.Listen("tcp", net.JoinHostPort(s.config.Host, strconv.Itoa(s.config.Port)))
	if err != nil {
		return err
	}
	s.listener = listener
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWebSocket)
	s.http = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		err := s.http.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			err = nil
		}
		s.serveErr <- err
	}()
	slog.Info("kernel listening", "address", "ws://"+s.Addr()+"/ws")
	// TODO(§9/v0.16): AutostartCommand is the milestone interface only. A later
	// milestone may launch the single supervisor command here.
	return nil
}

func (s *Server) Wait() error { return <-s.serveErr }

func (s *Server) Shutdown(ctx context.Context) error {
	if !s.stopping.CompareAndSwap(false, true) {
		return nil
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
	reason := closeReason(4009, "kernel shutting down")
	s.mu.Lock()
	sockets := make([]*clientSocket, 0, len(s.sockets))
	for _, socket := range s.sockets {
		sockets = append(sockets, socket)
	}
	s.mu.Unlock()
	var closeWG sync.WaitGroup
	for _, socket := range sockets {
		closeWG.Add(1)
		go func(socket *clientSocket) {
			defer closeWG.Done()
			_ = socket.ws.Close(websocket.StatusCode(4009), reason)
			socket.cancel()
		}(socket)
	}
	closed := make(chan struct{})
	go func() { closeWG.Wait(); close(closed) }()
	select {
	case <-closed:
	case <-ctx.Done():
		for _, socket := range sockets {
			socket.ws.CloseNow()
			socket.cancel()
		}
	}
	handled := make(chan struct{})
	go func() { s.handlers.Wait(); close(handled) }()
	select {
	case <-handled:
	case <-ctx.Done():
		return ctx.Err()
	}
	if s.http != nil {
		_ = s.http.Close()
	}
	slog.Info("kernel stopped")
	return nil
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if s.stopping.Load() {
		http.Error(w, "kernel shutting down", http.StatusServiceUnavailable)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		slog.Warn("websocket accept failed", "error", err)
		return
	}
	// The protocol requires oversized frames to be drained and rejected without
	// closing the connection, so the transport limit is disabled and readMessage
	// retains only FrameSize+1 bytes while discarding the remainder.
	ws.SetReadLimit(-1)
	ctx, cancel := context.WithCancel(r.Context())
	socket := &clientSocket{ws: ws, cancel: cancel}
	s.mu.Lock()
	s.sockets[ws] = socket
	s.mu.Unlock()
	s.handlers.Add(1)
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.sockets, ws)
		s.mu.Unlock()
		s.handlers.Done()
	}()
	if s.stopping.Load() {
		_ = ws.Close(websocket.StatusCode(4009), closeReason(4009, "kernel shutting down"))
		return
	}
	s.serveConnection(ctx, ws)
}

func (s *Server) serveConnection(ctx context.Context, ws *websocket.Conn) {
	messageType, data, size, oversized, err := readMessage(ctx, ws, s.config.FrameSize)
	if err != nil {
		return
	}
	if messageType != websocket.MessageText {
		_ = ws.Close(websocket.StatusCode(4001), closeReason(4001, "first frame must be hello"))
		return
	}
	if oversized {
		_ = ws.Close(websocket.StatusCode(4002), closeReason(4002, "hello frame exceeds size limit"))
		return
	}
	var first map[string]json.RawMessage
	if json.Unmarshal(data, &first) != nil || first == nil {
		_ = ws.Close(websocket.StatusCode(4001), closeReason(4001, "first frame must be hello"))
		return
	}
	var frameType string
	if json.Unmarshal(first["type"], &frameType) != nil || frameType != "hello" {
		_ = ws.Close(websocket.StatusCode(4001), closeReason(4001, "first frame must be hello"))
		return
	}
	hello, err := protocol.ParseHello(data)
	if err != nil {
		_ = ws.Close(websocket.StatusCode(4002), closeReason(4002, err.Error()))
		return
	}
	if hello.ProtocolVersion != protocol.Version {
		message := fmt.Sprintf("protocol version mismatch: got %d, want %d", hello.ProtocolVersion, protocol.Version)
		_ = ws.Close(websocket.StatusCode(4003), closeReason(4003, message))
		return
	}
	state, err := s.broker.AddConnection(hello.Conn)
	if err != nil {
		_ = ws.Close(websocket.StatusCode(4002), closeReason(4002, err.Error()))
		return
	}
	registered := false
	writerCtx, writerCancel := context.WithCancel(ctx)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		s.writer(writerCtx, ws, state)
	}()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		s.heartbeat(writerCtx, ws)
	}()
	defer func() {
		s.broker.RemoveConnection(hello.Conn)
		if registered {
			s.registry.Deregister(hello.Conn)
		}
		writerCancel()
		<-writerDone
		<-heartbeatDone
	}()
	s.registry.Register(hello)
	registered = true
	for {
		messageType, data, size, oversized, err = readMessage(ctx, ws, s.config.FrameSize)
		if err != nil {
			return
		}
		if messageType != websocket.MessageText {
			s.broker.ReportError(hello.Conn, "malformed_frame", "binary frames are not supported", nil)
			continue
		}
		if oversized {
			s.reportOversize(hello.Conn, size, s.config.FrameSize)
			continue
		}
		frame, parseErr := protocol.ParseClientFrame(data)
		if parseErr != nil {
			var protocolErr *protocol.Error
			if errors.As(parseErr, &protocolErr) {
				s.broker.ReportError(hello.Conn, protocolErr.Code, protocolErr.Message, protocolErr.Detail)
			} else {
				s.broker.ReportError(hello.Conn, "malformed_frame", parseErr.Error(), nil)
			}
			continue
		}
		switch frame.Type {
		case "subscribe":
			if err := s.broker.Subscribe(hello.Conn, frame.Pattern); err != nil {
				s.broker.ReportError(hello.Conn, "invalid_pattern", err.Error(), nil)
			}
		case "unsubscribe":
			if err := s.broker.Unsubscribe(hello.Conn, frame.Pattern); err != nil {
				s.broker.ReportError(hello.Conn, "invalid_pattern", err.Error(), nil)
			}
		default:
			instance := protocol.DefaultInstanceID
			if hello.InstanceID != nil {
				instance = *hello.InstanceID
			}
			delivery := protocol.Delivery{
				Type: frame.Type, Channel: frame.Channel, Value: frame.Value,
				TraceID: frame.TraceID, Depth: frame.Depth, TS: time.Now().UnixMilli(),
				Origin: protocol.Origin{Plugin: hello.Manifest.ID, Instance: instance},
			}
			encoded, marshalErr := json.Marshal(delivery)
			if marshalErr != nil {
				s.broker.ReportError(hello.Conn, "malformed_frame", "frame cannot be encoded", nil)
				continue
			}
			if len(encoded) > s.config.FrameSize {
				s.reportOversize(hello.Conn, len(encoded), s.config.FrameSize)
				continue
			}
			s.broker.Publish(delivery, hello.Conn)
		}
	}
}

func (s *Server) writer(ctx context.Context, ws *websocket.Conn, state *broker.Connection) {
	for {
		frame, err := state.Next(ctx)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(frame)
		if err != nil {
			slog.Error("failed to encode outbound frame", "conn", state.Conn, "error", err)
			continue
		}
		if len(encoded) > s.config.FrameSize {
			slog.Warn("dropping oversized internal outbound frame", "conn", state.Conn, "size", len(encoded), "limit", s.config.FrameSize)
			continue
		}
		if err := ws.Write(ctx, websocket.MessageText, encoded); err != nil {
			return
		}
	}
}

func (s *Server) heartbeat(ctx context.Context, ws *websocket.Conn) {
	ticker := time.NewTicker(s.config.PingInterval)
	defer ticker.Stop()
	failures := 0
	pingTimeout := min(10*time.Second, s.config.PingInterval/3)
	if pingTimeout <= 0 {
		pingTimeout = time.Second
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
			err := ws.Ping(pingCtx)
			cancel()
			if err == nil {
				failures = 0
				continue
			}
			failures++
			if failures >= 2 {
				ws.CloseNow()
				return
			}
		}
	}
}

func (s *Server) reportOversize(conn string, size, limit int) {
	s.broker.ReportError(conn, "frame_too_large", fmt.Sprintf("frame size %d exceeds limit %d", size, limit), map[string]any{"size": size, "limit": limit})
}

func readMessage(ctx context.Context, ws *websocket.Conn, limit int) (websocket.MessageType, []byte, int, bool, error) {
	messageType, reader, err := ws.Reader(ctx)
	if err != nil {
		return 0, nil, 0, false, err
	}
	var buffer bytes.Buffer
	read, err := io.CopyN(&buffer, reader, int64(limit)+1)
	if errors.Is(err, io.EOF) {
		return messageType, buffer.Bytes(), int(read), false, nil
	}
	if err != nil {
		return 0, nil, int(read), false, err
	}
	discarded, err := io.Copy(io.Discard, reader)
	if err != nil {
		return 0, nil, int(read + discarded), true, err
	}
	return messageType, nil, int(read + discarded), true, nil
}

func closeReason(code int, message string) string {
	encoded, _ := json.Marshal(map[string]any{"code": code, "message": message})
	return string(encoded)
}
