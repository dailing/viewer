// Package chat exposes the M6a single-provider chat skeleton on the Viewer bus.
package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"viewer/internal/acp"
	"viewer/internal/busclient"
	"viewer/internal/plugins/pluginrpc"
)

var Manifest = busclient.Manifest{
	ID: "chat", Version: "0.1.0",
	Slots: map[string]any{
		"chat:_:send-message": map[string]any{"value": map[string]any{"chat_id": "string", "text": "string", "cwd": "string?"}},
		"chat:_:stop":         map[string]any{"value": map[string]any{"chat_id": "string"}},
	},
	Emits: map[string]any{
		"chat:*:message":        map[string]any{"value": map[string]any{"id": "string", "chat_id": "string", "turn_id": "string", "role": "user|assistant", "text": "string", "created_at": "unix-ms"}},
		"chat:*:turn-completed": map[string]any{"value": map[string]any{"chat_id": "string", "turn_id": "string", "stop_reason": "string"}},
	},
}

type agent interface {
	Initialize(context.Context) (map[string]any, error)
	NewSession(context.Context, string) (string, error)
	LoadSession(context.Context, string, string) error
	Prompt(context.Context, string, string) (string, error)
	Cancel(context.Context, string) error
	OnUpdate(func(acp.Update))
	Stderr() string
	Close() error
}

type agentFactory func(context.Context) (agent, string, error)
type Option func(*Plugin)

func WithAgentFactory(factory agentFactory) Option { return func(p *Plugin) { p.factory = factory } }

type runtime struct {
	agent           agent
	sessionID       string
	profile         string
	cwd             string
	activeTurn      string
	cancelRequested bool
}

type Plugin struct {
	dataDir string
	store   *store
	client  *busclient.Client
	factory agentFactory

	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	runtimes map[string]*runtime
	closed   bool
	wg       sync.WaitGroup
}

func New(dataDir string, options ...Option) (*Plugin, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	database, err := openStore(dataDir)
	if err != nil {
		return nil, err
	}
	p := &Plugin{dataDir: dataDir, store: database, runtimes: make(map[string]*runtime)}
	p.factory = p.hermesAgent
	for _, option := range options {
		option(p)
	}
	return p, nil
}

func (p *Plugin) hermesAgent(ctx context.Context) (agent, string, error) {
	command := strings.TrimSpace(os.Getenv("VIEWER_HERMES_COMMAND"))
	if command == "" {
		command = "hermes"
	}
	profile := strings.TrimSpace(os.Getenv("VIEWER_HERMES_PROFILE"))
	if profile == "" {
		profile = "default"
	}
	yolo := envBool("VIEWER_HERMES_YOLO", true)
	arguments := []string{"-p", profile}
	if yolo {
		arguments = append(arguments, "--yolo")
	}
	arguments = append(arguments, "acp")
	client, err := acp.New(ctx, command, arguments...)
	return client, profile, err
}

func envBool(name string, fallback bool) bool {
	value, exists := os.LookupEnv(name)
	if !exists {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func (p *Plugin) Start(ctx context.Context, kernelWS string, managed bool) error {
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.client = busclient.New(kernelWS, Manifest, busclient.WithManaged(managed))
	for pattern, handler := range map[string]func(busclient.Frame){
		"chat:_:send-message": p.handleSend,
		"chat:_:stop":         p.handleStop,
	} {
		if _, err := p.client.Subscribe(pattern, handler); err != nil {
			return err
		}
	}
	return p.client.Connect(ctx)
}

func (p *Plugin) handleSend(frame busclient.Frame) {
	value, ok := pluginrpc.Object(frame)
	if !ok {
		pluginrpc.RespondError(p.client, frame, "bad_request", "payload must be an object")
		return
	}
	chatID, _ := value["chat_id"].(string)
	text, _ := value["text"].(string)
	cwd, _ := value["cwd"].(string)
	result, startTurn, err := p.accept(p.ctx, strings.TrimSpace(chatID), text, strings.TrimSpace(cwd))
	if err != nil {
		code := "send_failed"
		if errors.Is(err, errBadRequest) {
			code = "bad_request"
		}
		if errors.Is(err, errTurnActive) {
			code = "turn_in_progress"
		}
		_ = pluginrpc.RespondError(p.client, frame, code, err.Error())
		return
	}
	_ = pluginrpc.Respond(p.client, frame, result)
	startTurn()
}

func (p *Plugin) handleStop(frame busclient.Frame) {
	value, ok := pluginrpc.Object(frame)
	if !ok {
		_ = pluginrpc.RespondError(p.client, frame, "bad_request", "payload must be an object")
		return
	}
	chatID, _ := value["chat_id"].(string)
	stopped, err := p.stopTurn(strings.TrimSpace(chatID))
	if err != nil {
		_ = pluginrpc.RespondError(p.client, frame, "stop_failed", err.Error())
		return
	}
	_ = pluginrpc.Respond(p.client, frame, map[string]any{"stopped": stopped})
}

var (
	errBadRequest = errors.New("chat_id and text are required")
	errTurnActive = errors.New("chat already has a turn in progress")
)

func (p *Plugin) accept(ctx context.Context, chatID, text, requestedCWD string) (map[string]any, func(), error) {
	if chatID == "" || strings.TrimSpace(text) == "" {
		return nil, nil, errBadRequest
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, nil, errors.New("chat plugin is stopping")
	}
	if current := p.runtimes[chatID]; current != nil && current.activeTurn != "" {
		return nil, nil, errTurnActive
	}
	chat, err := p.store.chat(chatID)
	if err == nil && chat == nil {
		cwd := requestedCWD
		if cwd == "" {
			cwd, err = os.Getwd()
		}
		if err != nil {
			return nil, nil, err
		}
		cwd, err = filepath.Abs(cwd)
		if err != nil {
			return nil, nil, err
		}
		chat = &Chat{ID: chatID, CreatedAt: nowMillis(), Provider: "hermes", CWD: cwd}
	} else if err != nil {
		return nil, nil, err
	}
	if requestedCWD != "" {
		absolute, absErr := filepath.Abs(requestedCWD)
		if absErr != nil {
			return nil, nil, absErr
		}
		if chat.CWD != "" && chat.CWD != absolute {
			if old := p.runtimes[chatID]; old != nil {
				_ = old.agent.Close()
				delete(p.runtimes, chatID)
			}
			chat.ProviderSessionID = ""
		}
		chat.CWD = absolute
	}
	current, err := p.ensureRuntime(ctx, chat)
	if err != nil {
		return nil, nil, err
	}
	turnID := newID()
	message := &Message{ID: newID(), ChatID: chatID, TurnID: turnID, Role: "user", Text: text, CreatedAt: nowMillis()}
	turn := &Turn{ID: turnID, ChatID: chatID, StartedAt: nowMillis()}
	if err := p.store.beginTurn(chat, turn, message); err != nil {
		return nil, nil, err
	}
	current.activeTurn, current.cancelRequested = turnID, false
	p.publishMessage(message)
	startGate := make(chan struct{})
	p.wg.Add(1)
	go func() {
		<-startGate
		p.runTurn(chatID, turnID, text, current)
	}()
	var startOnce sync.Once
	start := func() {
		startOnce.Do(func() { close(startGate) })
	}
	return map[string]any{"accepted": true, "turn_id": turnID}, start, nil
}

func (p *Plugin) ensureRuntime(ctx context.Context, chat *Chat) (*runtime, error) {
	if current := p.runtimes[chat.ID]; current != nil {
		return current, nil
	}
	process, profile, err := p.factory(ctx)
	if err != nil {
		return nil, err
	}
	process.OnUpdate(func(update acp.Update) { p.handleUpdate(chat.ID, update) })
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := process.Initialize(initCtx); err != nil {
		_ = process.Close()
		return nil, fmt.Errorf("initialize Hermes ACP: %w", err)
	}
	sessionID := chat.ProviderSessionID
	if sessionID != "" && chat.ProviderProfile == profile {
		if err := process.LoadSession(initCtx, sessionID, chat.CWD); err != nil {
			sessionID = ""
		}
	} else {
		sessionID = ""
	}
	if sessionID == "" {
		sessionID, err = process.NewSession(initCtx, chat.CWD)
		if err != nil {
			_ = process.Close()
			return nil, fmt.Errorf("create Hermes ACP session: %w", err)
		}
		chat.ProviderSessionID, chat.ProviderProfile, chat.Provider = sessionID, profile, "hermes"
		if err := p.store.saveChat(chat); err != nil {
			_ = process.Close()
			return nil, err
		}
	}
	current := &runtime{agent: process, sessionID: sessionID, profile: profile, cwd: chat.CWD}
	p.runtimes[chat.ID] = current
	return current, nil
}

func (p *Plugin) handleUpdate(chatID string, update acp.Update) {
	p.mu.Lock()
	current := p.runtimes[chatID]
	if current == nil || current.activeTurn == "" || update.SessionID != current.sessionID {
		p.mu.Unlock()
		return
	}
	turnID := current.activeTurn
	p.mu.Unlock()
	if text := updateText(update.Value); text != "" {
		message := &Message{ID: newID(), ChatID: chatID, TurnID: turnID, Role: "assistant", Text: text, CreatedAt: nowMillis()}
		if p.store.addMessage(message) == nil {
			p.publishMessage(message)
		}
	}
}

func updateText(value map[string]any) string {
	kind, _ := value["sessionUpdate"].(string)
	if kind == "" {
		kind, _ = value["session_update"].(string)
	}
	if kind != "agent_message_chunk" {
		return ""
	}
	if content, ok := value["content"].(map[string]any); ok {
		text, _ := content["text"].(string)
		return text
	}
	text, _ := value["text"].(string)
	return text
}

func (p *Plugin) runTurn(chatID, turnID, text string, current *runtime) {
	defer p.wg.Done()
	reason, err := current.agent.Prompt(p.ctx, current.sessionID, text)
	p.mu.Lock()
	cancelled := current.cancelRequested
	if current.activeTurn == turnID {
		current.activeTurn = ""
		current.cancelRequested = false
	}
	if err != nil {
		delete(p.runtimes, chatID)
		_ = current.agent.Close()
	}
	p.mu.Unlock()
	if cancelled {
		reason = "cancelled"
	} else if err != nil {
		reason = "error"
	} else if reason == "" {
		reason = "end_turn"
	}
	_ = p.store.completeTurn(turnID, reason)
	p.publish("chat:"+chatID+":turn-completed", map[string]any{"chat_id": chatID, "turn_id": turnID, "stop_reason": reason})
}

func (p *Plugin) stopTurn(chatID string) (bool, error) {
	if chatID == "" {
		return false, errBadRequest
	}
	p.mu.Lock()
	current := p.runtimes[chatID]
	if current == nil || current.activeTurn == "" {
		p.mu.Unlock()
		return false, nil
	}
	current.cancelRequested = true
	agent, sessionID := current.agent, current.sessionID
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return true, agent.Cancel(ctx, sessionID)
}

func (p *Plugin) publishMessage(message *Message) {
	p.publish("chat:"+message.ChatID+":message", map[string]any{
		"id": message.ID, "chat_id": message.ChatID, "turn_id": message.TurnID,
		"role": message.Role, "text": message.Text, "created_at": message.CreatedAt,
	})
}

func (p *Plugin) publish(channel string, value any) {
	if p.client != nil {
		_ = p.client.Publish(context.Background(), channel, value)
	}
}

func (p *Plugin) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	if p.cancel != nil {
		p.cancel()
	}
	items := make([]*runtime, 0, len(p.runtimes))
	for _, item := range p.runtimes {
		items = append(items, item)
	}
	p.mu.Unlock()
	for _, item := range items {
		_ = item.agent.Close()
	}
	p.wg.Wait()
	var busErr error
	if p.client != nil {
		busErr = p.client.Close()
	}
	return errors.Join(busErr, p.store.close())
}

func newID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}
