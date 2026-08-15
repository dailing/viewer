// Package voice relays browser microphone audio from the Viewer bus to the
// external voice-service WebSocket.
package voice

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

const (
	DefaultServiceWS = "ws://127.0.0.1:8765/v1/voice/ws"
	MaxDuration      = 10 * time.Minute
	configNamespace  = "viewer-voice"
	ioTimeout        = 5 * time.Second
)

var Manifest = busclient.Manifest{
	ID: "voice", Version: "0.1.0",
	Slots: map[string]any{
		"voice:_:start":  map[string]any{"summary": "start a voice-service relay; RPC -> {rec_id}"},
		"voice:_:cancel": map[string]any{"summary": "cancel a recording relay"},
	},
	Emits: map[string]any{
		"voice:*:event": map[string]any{"summary": "normalized voice-service state"},
	},
}

type Config struct {
	KernelWS    string
	MaxDuration time.Duration
	Dial        func(context.Context, string) (*websocket.Conn, error)
}

func DefaultConfig() Config { return Config{MaxDuration: MaxDuration} }

type serviceConfig struct {
	URL      string
	Model    string
	Language string
}

type session struct {
	id     string
	plugin *Plugin
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc

	writeMu sync.Mutex
	subs    []*busclient.Subscription
	once    sync.Once
}

type Plugin struct {
	config Config
	client *busclient.Client

	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.RWMutex
	sessions map[string]*session
	closed   bool
	wg       sync.WaitGroup
}

func New(config Config) (*Plugin, error) {
	defaults := DefaultConfig()
	if config.KernelWS == "" {
		return nil, errors.New("kernel websocket is required")
	}
	if config.MaxDuration <= 0 {
		config.MaxDuration = defaults.MaxDuration
	}
	if config.Dial == nil {
		config.Dial = func(ctx context.Context, target string) (*websocket.Conn, error) {
			conn, _, err := websocket.Dial(ctx, target, nil)
			return conn, err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Plugin{config: config, ctx: ctx, cancel: cancel, sessions: make(map[string]*session)}, nil
}

func (p *Plugin) Run(ctx context.Context) error {
	if err := p.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return p.Close()
}

func (p *Plugin) Start(ctx context.Context) error {
	return p.StartWithManaged(ctx, os.Getenv("VIEWER_MANAGED") == "1")
}

func (p *Plugin) StartWithManaged(ctx context.Context, managed bool) error {
	p.client = busclient.New(p.config.KernelWS, Manifest, busclient.WithManaged(managed))
	for pattern, handler := range map[string]func(busclient.Frame){
		"voice:_:start": p.handleStart, "voice:_:cancel": p.handleCancel,
	} {
		if _, err := p.client.Subscribe(pattern, handler); err != nil {
			_ = p.client.Close()
			return fmt.Errorf("subscribe %s: %w", pattern, err)
		}
	}
	if err := p.client.Connect(ctx); err != nil {
		_ = p.client.Close()
		return fmt.Errorf("connect voice plugin: %w", err)
	}
	return nil
}

func (p *Plugin) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	sessions := make([]*session, 0, len(p.sessions))
	for _, current := range p.sessions {
		sessions = append(sessions, current)
	}
	p.mu.Unlock()
	p.cancel()
	for _, current := range sessions {
		current.close()
	}
	p.wg.Wait()
	if p.client != nil {
		return p.client.Close()
	}
	return nil
}

func (p *Plugin) handleStart(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	value, ok := pluginrpc.Object(frame)
	mimeType, mimeOK := value["mime_type"].(string)
	llmRefine, refineOK := value["llm_refine"].(bool)
	if !ok || !mimeOK || !refineOK {
		p.respondError(frame, "invalid_request", "mime_type string and llm_refine bool are required")
		return
	}
	service, err := p.readServiceConfig(p.ctx)
	if err != nil {
		p.respondError(frame, "config_error", err.Error())
		return
	}
	recID, err := newRecordingID()
	if err != nil {
		p.respondError(frame, "id_error", err.Error())
		return
	}
	dialCtx, cancelDial := context.WithTimeout(p.ctx, ioTimeout)
	conn, err := p.config.Dial(dialCtx, service.URL)
	cancelDial()
	if err != nil {
		p.respondError(frame, "service_unavailable", err.Error())
		return
	}
	conn.SetReadLimit(-1)
	sessionCtx, cancelSession := context.WithCancel(p.ctx)
	current := &session{id: recID, plugin: p, conn: conn, ctx: sessionCtx, cancel: cancelSession}
	start := map[string]any{"type": "start", "mimeType": mimeType, "llm_refine": llmRefine}
	if service.Model != "" {
		start["model"] = service.Model
	}
	if service.Language != "" {
		start["language"] = service.Language
	}
	if err := current.writeJSON(start); err != nil {
		current.close()
		p.respondError(frame, "service_write_failed", err.Error())
		return
	}
	chunkSub, err := p.client.Subscribe("voice:"+recID+":chunk", current.handleChunk)
	if err != nil {
		current.close()
		p.respondError(frame, "subscribe_failed", err.Error())
		return
	}
	current.subs = append(current.subs, chunkSub)
	stopSub, err := p.client.Subscribe("voice:"+recID+":stop", current.handleStop)
	if err != nil {
		current.close()
		p.respondError(frame, "subscribe_failed", err.Error())
		return
	}
	current.subs = append(current.subs, stopSub)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		current.close()
		p.respondError(frame, "closed", "voice plugin is stopping")
		return
	}
	p.sessions[recID] = current
	p.mu.Unlock()
	p.wg.Add(1)
	go current.relay()
	p.wg.Add(1)
	go current.enforceLimit(p.config.MaxDuration)
	if err := current.publish(map[string]any{"type": "ready"}); err != nil {
		current.close()
		p.respondError(frame, "publish_failed", err.Error())
		return
	}
	p.respond(frame, map[string]any{"rec_id": recID})
}

func (p *Plugin) handleCancel(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	value, ok := pluginrpc.Object(frame)
	recID, valid := value["rec_id"].(string)
	if !ok || !valid || recID == "" {
		p.respondError(frame, "invalid_request", "rec_id string is required")
		return
	}
	p.mu.RLock()
	current := p.sessions[recID]
	p.mu.RUnlock()
	if current != nil {
		current.close()
	}
	p.respond(frame, map[string]any{"rec_id": recID})
}

func (p *Plugin) readServiceConfig(ctx context.Context) (serviceConfig, error) {
	result := serviceConfig{URL: DefaultServiceWS}
	for key, target := range map[string]*string{
		"service_ws": &result.URL, "model": &result.Model, "language": &result.Language,
	} {
		value, err := p.client.Request(ctx, "config:_:get", map[string]any{"plugin": configNamespace, "key": key}, ioTimeout)
		if err != nil {
			return result, fmt.Errorf("read %s config: %w", key, err)
		}
		if configured, ok := value.(string); ok {
			*target = strings.TrimSpace(configured)
		}
	}
	if result.URL == "" {
		result.URL = DefaultServiceWS
	}
	return result, nil
}

func (p *Plugin) respond(frame busclient.Frame, result any) {
	if err := pluginrpc.Respond(p.client, frame, result); err != nil {
		slog.Warn("voice RPC response failed", "error", err)
	}
}

func (p *Plugin) respondError(frame busclient.Frame, code, message string) {
	if err := pluginrpc.RespondError(p.client, frame, code, message); err != nil {
		slog.Warn("voice RPC error response failed", "error", err)
	}
}

func (s *session) handleChunk(frame busclient.Frame) {
	value, ok := pluginrpc.Object(frame)
	encoded, valid := value["data"].(string)
	if !ok || !valid {
		s.fail("invalid base64 audio chunk")
		return
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		s.fail("invalid base64 audio chunk")
		return
	}
	if err := s.write(websocket.MessageBinary, data); err != nil && s.ctx.Err() == nil {
		s.fail("voice service write failed: " + err.Error())
	}
}

func (s *session) handleStop(busclient.Frame) {
	if err := s.writeJSON(map[string]any{"type": "stop"}); err != nil && s.ctx.Err() == nil {
		s.fail("voice service stop failed: " + err.Error())
	}
}

func (s *session) writeJSON(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.write(websocket.MessageText, data)
}

func (s *session) write(kind websocket.MessageType, data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, ioTimeout)
	defer cancel()
	return s.conn.Write(ctx, kind, data)
}

func (s *session) relay() {
	defer s.plugin.wg.Done()
	defer s.close()
	for {
		kind, data, err := s.conn.Read(s.ctx)
		if err != nil {
			return
		}
		if kind != websocket.MessageText {
			continue
		}
		event := normalizeServiceMessage(data)
		if event == nil || event["type"] == "ready" {
			continue
		}
		if err := s.publish(event); err != nil {
			return
		}
		if event["type"] == "final" || event["type"] == "error" {
			return
		}
	}
}

func (s *session) enforceLimit(duration time.Duration) {
	defer s.plugin.wg.Done()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		s.fail("recording exceeded max duration")
	case <-s.ctx.Done():
	}
}

func (s *session) fail(message string) {
	_ = s.publish(map[string]any{"type": "error", "message": message})
	s.close()
}

func (s *session) publish(value any) error {
	ctx, cancel := context.WithTimeout(s.ctx, ioTimeout)
	defer cancel()
	return s.plugin.client.Publish(ctx, "voice:"+s.id+":event", value)
}

func (s *session) close() {
	s.once.Do(func() {
		s.plugin.mu.Lock()
		if s.plugin.sessions[s.id] == s {
			delete(s.plugin.sessions, s.id)
		}
		s.plugin.mu.Unlock()
		s.cancel()
		_ = s.conn.CloseNow()
		for _, sub := range s.subs {
			ctx, cancel := context.WithTimeout(context.Background(), ioTimeout)
			_ = sub.Unsubscribe(ctx)
			cancel()
		}
	})
}

func normalizeServiceMessage(data []byte) map[string]any {
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		text := strings.TrimSpace(string(data))
		if text == "" {
			return nil
		}
		return map[string]any{"type": "partial", "text": text}
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return nil
	}
	kind, _ := object["type"].(string)
	switch kind {
	case "ready", "processing", "partial", "committed", "final", "error":
		return object
	default:
		if text, ok := object["text"]; ok && text != nil && fmt.Sprint(text) != "" {
			return map[string]any{"type": "partial", "text": fmt.Sprint(text)}
		}
		return nil
	}
}

func newRecordingID() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
