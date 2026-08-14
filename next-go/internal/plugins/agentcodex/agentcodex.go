// Package agentcodex implements the headless viewer.agent-codex bus plugin.
package agentcodex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"viewer/internal/agentdriver"
	"viewer/internal/busclient"
	"viewer/internal/codexserver"
	"viewer/internal/plugins/pluginrpc"
)

const (
	PluginID        = "viewer.agent-codex"
	configNamespace = "plugins.viewer-agent-codex"
)

var Manifest = busclient.Manifest{
	ID: PluginID, Version: "0.1.0",
	Slots: map[string]any{
		PluginID + ":_:start": map[string]any{}, PluginID + ":_:prompt": map[string]any{}, PluginID + ":_:cancel": map[string]any{},
	},
	Emits: map[string]any{
		PluginID + ":_:event": map[string]any{}, PluginID + ":_:turn-ended": map[string]any{}, PluginID + ":_:catalog": map[string]any{},
	},
}

type session struct {
	client    *codexserver.Client
	model     string
	seq       int
	turnID    string
	active    bool
	cancelled bool
}

type Plugin struct {
	client   *busclient.Client
	mu       sync.Mutex
	sessions map[string]*session
	wg       sync.WaitGroup
	closed   bool
}

func New() *Plugin { return &Plugin{sessions: map[string]*session{}} }

func (p *Plugin) Start(ctx context.Context, kernelWS string, managed bool) error {
	p.client = busclient.New(kernelWS, Manifest, busclient.WithManaged(managed), busclient.WithInstanceID("_"))
	for channel, handler := range map[string]func(busclient.Frame){
		PluginID + ":_:start": p.handleStart, PluginID + ":_:prompt": p.handlePrompt, PluginID + ":_:cancel": p.handleCancel,
	} {
		if _, err := p.client.Subscribe(channel, func(frame busclient.Frame) { go handler(frame) }); err != nil {
			return err
		}
	}
	if err := p.client.Connect(ctx); err != nil {
		return err
	}
	return p.client.Set(context.Background(), PluginID+":_:catalog", p.catalog(ctx))
}

func (p *Plugin) handleStart(frame busclient.Frame) {
	var request agentdriver.StartRequest
	err := decodeFrame(frame, &request)
	if err == nil && strings.TrimSpace(request.CWD) == "" {
		err = errors.New("cwd is required")
	}
	if err == nil && request.Target.Agent != "" && request.Target.Agent != "codex-app-server" && request.Target.Agent != "codex" {
		err = fmt.Errorf("unsupported agent %q", request.Target.Agent)
	}
	if err == nil && !envBool("VIEWER_CODEX_APP_SERVER_ENABLED", true) {
		err = errors.New("codex app-server is disabled")
	}
	if err != nil {
		p.respond(frame, nil, err)
		return
	}
	p.mu.Lock()
	existing := p.sessions[request.SessionID]
	p.mu.Unlock()
	if request.SessionID != "" && existing != nil && existing.model == request.Target.Model {
		p.respond(frame, map[string]any{"session_id": request.SessionID, "resumed": true}, nil)
		return
	}
	client, err := newClient(context.Background())
	if err != nil {
		p.respond(frame, nil, err)
		return
	}
	startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sessionID, resumed := request.SessionID, false
	if sessionID != "" && client.ThreadResume(startCtx, sessionID, request.CWD) == nil {
		resumed = true
	} else {
		sessionID, err = client.ThreadStart(startCtx, request.CWD, request.Target.Model)
	}
	if err != nil {
		_ = client.Close()
		p.respond(frame, nil, fmt.Errorf("start codex session: %w", err))
		return
	}
	current := &session{client: client, model: request.Target.Model}
	client.OnUpdate(func(update codexserver.Update) { p.handleUpdate(sessionID, current, update) })
	p.mu.Lock()
	if replaced := p.sessions[sessionID]; replaced != nil {
		_ = replaced.client.Close()
	}
	p.sessions[sessionID] = current
	p.mu.Unlock()
	p.respond(frame, map[string]any{"session_id": sessionID, "resumed": resumed}, nil)
}

func (p *Plugin) handlePrompt(frame busclient.Frame) {
	var request agentdriver.PromptRequest
	err := decodeFrame(frame, &request)
	if err == nil && strings.TrimSpace(request.TurnID) == "" {
		err = errors.New("turn_id is required")
	}
	p.mu.Lock()
	current := p.sessions[request.SessionID]
	if err == nil && current == nil {
		err = errors.New("session not found")
	}
	if err == nil && current.active {
		err = errors.New("session turn already active")
	}
	if err == nil {
		current.active, current.cancelled, current.seq, current.turnID = true, false, 0, request.TurnID
	}
	p.mu.Unlock()
	if err != nil {
		p.respond(frame, nil, err)
		return
	}
	gate := make(chan struct{})
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		<-gate
		turn, promptErr := current.client.TurnStart(context.Background(), request.SessionID, request.Text, current.model)
		status, _ := turn["status"].(string)
		p.mu.Lock()
		cancelled := current.cancelled
		current.active, current.cancelled, current.turnID = false, false, ""
		p.mu.Unlock()
		reason := "end_turn"
		if cancelled || status == "interrupted" {
			reason = "cancelled"
		} else if promptErr != nil || status == "failed" {
			reason = "error"
		}
		payload := agentdriver.TurnEndedFrame{SessionID: request.SessionID, TurnID: request.TurnID, StopReason: reason}
		if promptErr != nil {
			payload.Error = promptErr.Error()
		}
		_ = p.client.Publish(context.Background(), PluginID+":_:turn-ended", payload)
	}()
	p.respond(frame, map[string]any{"accepted": true}, nil)
	close(gate)
}

func (p *Plugin) handleCancel(frame busclient.Frame) {
	var request struct {
		SessionID string `json:"session_id"`
	}
	err := decodeFrame(frame, &request)
	p.mu.Lock()
	current := p.sessions[request.SessionID]
	stopped := current != nil && current.active
	if stopped {
		current.cancelled = true
	}
	p.mu.Unlock()
	if err == nil && stopped {
		err = current.client.TurnInterrupt(context.Background(), request.SessionID)
	}
	p.respond(frame, map[string]any{"stopped": stopped}, err)
}

func (p *Plugin) handleUpdate(sessionID string, current *session, update codexserver.Update) {
	p.mu.Lock()
	if !current.active {
		p.mu.Unlock()
		return
	}
	seq := current.seq
	turnID := current.turnID
	current.seq++
	p.mu.Unlock()
	frame := agentdriver.EventFrame{SessionID: sessionID, TurnID: turnID, Seq: seq, Kind: update.Method, RawJSON: string(update.Raw), Block: codexserver.ParseBlock(update.Method, update.Params)}
	_ = p.client.Publish(context.Background(), PluginID+":_:event", frame)
}

func newClient(ctx context.Context) (*codexserver.Client, error) {
	command := strings.TrimSpace(os.Getenv("VIEWER_CODEX_APP_SERVER_COMMAND"))
	if command == "" {
		command = "codex"
	}
	return codexserver.New(ctx, codexserver.ProcessConfig{Command: command, Arguments: []string{"app-server", "--stdio"}, YOLO: envBool("VIEWER_CODEX_APP_SERVER_YOLO", true)})
}

func (p *Plugin) catalog(ctx context.Context) agentdriver.Catalog {
	result := discoverCatalog(ctx)
	var override agentdriver.Catalog
	value, err := p.client.Request(ctx, "config:_:get", map[string]any{"plugin": configNamespace, "key": "catalog"}, 5*time.Second)
	if err == nil && decode(value, &override) == nil && override.Agent != "" {
		result = override
	}
	if result.Providers == nil {
		result.Providers = []agentdriver.ProviderCatalog{}
	}
	return result
}

func discoverCatalog(ctx context.Context) agentdriver.Catalog {
	fallback := agentdriver.Catalog{Agent: "codex", Providers: []agentdriver.ProviderCatalog{{Provider: "openai-subscription", Models: []string{}}}}
	if !envBool("VIEWER_CODEX_APP_SERVER_ENABLED", true) {
		return fallback
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, err := newClient(discoveryCtx)
	if err != nil {
		return fallback
	}
	defer client.Close()
	value, err := client.ModelList(discoveryCtx)
	if err != nil {
		return fallback
	}
	models := modelIDs(value)
	fallback.Providers[0].Models = models
	return fallback
}

func modelIDs(value map[string]any) []string {
	items, _ := value["models"].([]any)
	result, seen := []string{}, map[string]bool{}
	for _, item := range items {
		model, _ := item.(map[string]any)
		id, _ := model["id"].(string)
		if id == "" {
			id, _ = model["model"].(string)
		}
		if id != "" && !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result
}

func decodeFrame(frame busclient.Frame, target any) error { return decode(frame.Value, target) }
func decode(value, target any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func (p *Plugin) respond(frame busclient.Frame, value any, err error) {
	if err == nil {
		_ = pluginrpc.Respond(p.client, frame, value)
	} else {
		_ = pluginrpc.RespondError(p.client, frame, "agent_error", err.Error())
	}
}

func (p *Plugin) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	items := make([]*session, 0, len(p.sessions))
	for _, item := range p.sessions {
		items = append(items, item)
	}
	p.mu.Unlock()
	for _, item := range items {
		_ = item.client.Close()
	}
	p.wg.Wait()
	if p.client != nil {
		return p.client.Close()
	}
	return nil
}

func envBool(name string, fallback bool) bool {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
