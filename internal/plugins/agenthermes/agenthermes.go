// Package agenthermes implements the headless viewer.agent-hermes bus plugin.
package agenthermes

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"viewer/internal/acp"
	"viewer/internal/agentdriver"
	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

const (
	PluginID        = "viewer.agent-hermes"
	configNamespace = "plugins.viewer-agent-hermes"
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
	client    *acp.Client
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
		PluginID + ":_:start":  p.handleStart,
		PluginID + ":_:prompt": p.handlePrompt,
		PluginID + ":_:cancel": p.handleCancel,
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
	if err == nil && request.Target.Agent != "" && request.Target.Agent != "hermes" {
		err = fmt.Errorf("unsupported agent %q", request.Target.Agent)
	}
	if err != nil {
		p.respond(frame, nil, err)
		return
	}
	p.mu.Lock()
	existing := p.sessions[request.SessionID]
	p.mu.Unlock()
	if request.SessionID != "" && existing != nil {
		p.respond(frame, map[string]any{"session_id": request.SessionID, "resumed": true}, nil)
		return
	}
	client, err := p.newClient(context.Background(), request.Target.Parameters)
	if err != nil {
		p.respond(frame, nil, err)
		return
	}
	initCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err = client.Initialize(initCtx); err != nil {
		_ = client.Close()
		p.respond(frame, nil, fmt.Errorf("initialize hermes: %w", err))
		return
	}
	sessionID, resumed := request.SessionID, false
	if sessionID != "" && client.LoadSession(initCtx, sessionID, request.CWD) == nil {
		resumed = true
	} else {
		sessionID, err = client.NewSession(initCtx, request.CWD)
	}
	if err != nil {
		_ = client.Close()
		p.respond(frame, nil, fmt.Errorf("start hermes session: %w", err))
		return
	}
	current := &session{client: client}
	client.OnUpdate(func(update acp.Update) { p.handleUpdate(sessionID, current, update) })
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
		reason, promptErr := current.client.Prompt(context.Background(), request.SessionID, request.Text)
		p.mu.Lock()
		cancelled := current.cancelled
		current.active, current.cancelled, current.turnID = false, false, ""
		p.mu.Unlock()
		if cancelled {
			reason = "cancelled"
		} else if promptErr != nil {
			reason = "error"
		} else if reason == "" {
			reason = "end_turn"
		}
		_ = p.client.Publish(context.Background(), PluginID+":_:turn-ended", agentdriver.TurnEndedFrame{SessionID: request.SessionID, TurnID: request.TurnID, StopReason: reason})
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
		err = current.client.Cancel(context.Background(), request.SessionID)
	}
	p.respond(frame, map[string]any{"stopped": stopped}, err)
}

func (p *Plugin) handleUpdate(sessionID string, current *session, update acp.Update) {
	kind, _ := update.Value["sessionUpdate"].(string)
	if kind == "" {
		kind, _ = update.Value["session_update"].(string)
	}
	if kind == "" {
		kind = "unknown"
	}
	p.mu.Lock()
	if !current.active {
		p.mu.Unlock()
		return
	}
	seq := current.seq
	turnID := current.turnID
	current.seq++
	p.mu.Unlock()
	frame := agentdriver.EventFrame{SessionID: sessionID, TurnID: turnID, Seq: seq, Kind: kind, RawJSON: string(update.Raw), Block: acp.ParseBlock(kind, update.Value)}
	_ = p.client.Publish(context.Background(), PluginID+":_:event", frame)
}

func (p *Plugin) newClient(ctx context.Context, parameters map[string]any) (*acp.Client, error) {
	command := strings.TrimSpace(os.Getenv("VIEWER_HERMES_COMMAND"))
	if command == "" {
		command = "hermes"
	}
	profile := strings.TrimSpace(os.Getenv("VIEWER_HERMES_PROFILE"))
	if profile == "" {
		profile = "default"
	}
	yolo := envBool("VIEWER_HERMES_YOLO", true)
	if value, ok := parameters["profile"].(string); ok && strings.TrimSpace(value) != "" {
		profile = strings.TrimSpace(value)
	}
	if value, ok := parameters["yolo"].(bool); ok {
		yolo = value
	}
	args := []string{"-p", profile}
	if yolo {
		args = append(args, "--yolo")
	}
	args = append(args, "acp")
	return acp.New(ctx, command, args...)
}

func (p *Plugin) catalog(ctx context.Context) agentdriver.Catalog {
	result := discoverCatalog()
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

func discoverCatalog() agentdriver.Catalog {
	fallback := agentdriver.Catalog{Agent: "hermes", Providers: []agentdriver.ProviderCatalog{{Provider: "default", Models: []string{}}}}
	home, err := os.UserHomeDir()
	if err != nil {
		return fallback
	}
	file, err := os.Open(filepath.Join(home, ".hermes", "config.yaml"))
	if err != nil {
		return fallback
	}
	defer file.Close()
	providers := []agentdriver.ProviderCatalog{}
	seen := map[string]int{}
	section, defaultProvider, defaultModel := "", "", ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		if indent == 0 {
			section = ""
			if trimmed == "model:" {
				section = "model"
			}
			if trimmed == "providers:" {
				section = "providers"
			}
			continue
		}
		if section == "model" && indent == 2 {
			key, value, ok := yamlPair(trimmed)
			if !ok {
				continue
			}
			switch key {
			case "provider":
				defaultProvider = value
			case "default", "model":
				defaultModel = value
			}
		}
		if section == "providers" && indent == 2 && strings.HasSuffix(trimmed, ":") {
			name := strings.Trim(strings.TrimSuffix(trimmed, ":"), `"'`)
			if name != "" {
				seen[name] = len(providers)
				providers = append(providers, agentdriver.ProviderCatalog{Provider: name, Models: []string{}})
			}
		}
	}
	if defaultProvider != "" {
		models := []string{}
		if defaultModel != "" {
			models = append(models, defaultModel)
		}
		if index, ok := seen[defaultProvider]; ok {
			providers[index].Models = models
		} else {
			providers = append([]agentdriver.ProviderCatalog{{Provider: defaultProvider, Models: models}}, providers...)
		}
	}
	if len(providers) > 0 {
		fallback.Providers = providers
	}
	return fallback
}

func yamlPair(line string) (string, string, bool) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return strings.TrimSpace(parts[0]), strings.Trim(strings.TrimSpace(parts[1]), `"'`), true
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
