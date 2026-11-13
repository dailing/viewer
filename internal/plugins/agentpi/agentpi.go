// Package agentpi implements the headless viewer.agent-pi bus plugin using
// Pi's native JSONL RPC protocol rather than ACP.
package agentpi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"viewer/internal/agentdriver"
	"viewer/internal/pirpc"
	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

const (
	PluginID        = "viewer.agent-pi"
	configNamespace = "plugins.viewer-agent-pi"
)

var Manifest = busclient.Manifest{
	ID: PluginID, Version: "0.1.0",
	Slots: map[string]any{
		PluginID + ":_:start": map[string]any{}, PluginID + ":_:prompt": map[string]any{}, PluginID + ":_:cancel": map[string]any{},
		PluginID + ":_:catalog-refresh": map[string]any{},
	},
	Emits: map[string]any{
		PluginID + ":_:event": map[string]any{}, PluginID + ":_:turn-ended": map[string]any{}, PluginID + ":_:catalog": map[string]any{},
	},
}

type session struct {
	client    *pirpc.Client
	seq       int
	turnID    string
	active    bool
	cancelled bool
}

type Plugin struct {
	client       *busclient.Client
	mu           sync.Mutex
	sessions     map[string]*session
	wg           sync.WaitGroup
	closed       bool
	catalogCache *agentdriver.CatalogCache
}

func New() *Plugin {
	plugin := &Plugin{sessions: map[string]*session{}}
	fallback := agentdriver.Catalog{Agent: "pi", Providers: []agentdriver.ProviderCatalog{{Provider: "default", Models: []string{}}}}
	plugin.catalogCache = agentdriver.NewCatalogCache(fallback, plugin.discoverCatalog)
	return plugin
}

func (p *Plugin) Start(ctx context.Context, kernelWS string, managed bool) error {
	p.client = busclient.New(kernelWS, Manifest, busclient.WithManaged(managed), busclient.WithInstanceID("_"))
	for channel, handler := range map[string]func(busclient.Frame){
		PluginID + ":_:start": p.handleStart, PluginID + ":_:prompt": p.handlePrompt, PluginID + ":_:cancel": p.handleCancel,
		PluginID + ":_:catalog-refresh": p.handleCatalogRefresh,
	} {
		if _, err := p.client.Subscribe(channel, func(frame busclient.Frame) { go handler(frame) }); err != nil {
			return err
		}
	}
	if err := p.client.Connect(ctx); err != nil {
		return err
	}
	if err := p.client.Set(context.Background(), PluginID+":_:catalog", p.catalog(ctx)); err != nil {
		return err
	}
	go p.catalogCache.StartOnce(context.Background(), func(agentdriver.Catalog) {
		if err := p.client.Set(context.Background(), PluginID+":_:catalog", p.catalog(context.Background())); err != nil {
			log.Printf("viewer-agent-pi catalog publish failed: %v", err)
		}
	})
	return nil
}

func (p *Plugin) handleStart(frame busclient.Frame) {
	var request agentdriver.StartRequest
	err := decodeFrame(frame, &request)
	if err == nil && strings.TrimSpace(request.CWD) == "" {
		err = errors.New("cwd is required")
	}
	if err == nil && request.Target.Agent != "" && request.Target.Agent != "pi" {
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

	client, err := newClient(context.Background(), request.CWD, false)
	if err != nil {
		p.respond(frame, nil, err)
		return
	}
	startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sessionID, resumed := request.SessionID, false
	var state pirpc.State
	if sessionID != "" {
		if switchErr := client.SwitchSession(startCtx, sessionID); switchErr == nil {
			state, err = client.GetState(startCtx)
			if err == nil && state.SessionFile != sessionID {
				err = fmt.Errorf("Pi resumed unexpected session file %q", state.SessionFile)
			} else if err == nil {
				resumed = true
			}
		}
	}
	if !resumed && err == nil {
		// A newly started RPC process already owns a fresh persistent session.
		// If resume was rejected, deliberately use that fresh session instead
		// of accepting a stale path that would fail on the next prompt. Once a
		// switch succeeded, state-validation errors remain fatal instead.
		state, err = client.GetState(startCtx)
		sessionID = state.SessionFile
	}
	if err == nil && sessionID == "" {
		err = errors.New("Pi RPC returned no session file; Viewer role sessions require persistent sessions (do not use --no-session)")
	}
	provider, modelID := piModelSelection(request.Target)
	if err == nil && modelID != "" {
		err = client.SetModel(startCtx, provider, modelID)
		if err != nil {
			err = fmt.Errorf("set Pi model %q: %w", provider+"/"+modelID, err)
		}
	}
	if err == nil {
		if level := piThinkingLevel(request.Target); level != "" {
			err = client.SetThinkingLevel(startCtx, level)
			if err != nil {
				err = fmt.Errorf("set Pi thinking level %q: %w", level, err)
			}
		}
	}
	if err != nil {
		_ = client.Close()
		p.respond(frame, nil, fmt.Errorf("start Pi session: %w", err))
		return
	}
	current := &session{client: client}
	client.OnEvent(func(event pirpc.Event) { p.handleEvent(sessionID, current, event) })
	p.mu.Lock()
	if replaced := p.sessions[sessionID]; replaced != nil {
		_ = replaced.client.Close()
	}
	p.sessions[sessionID] = current
	p.mu.Unlock()
	p.respond(frame, map[string]any{"session_id": sessionID, "pi_session_id": state.SessionID, "resumed": resumed}, nil)
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
		result, promptErr := current.client.PromptAndWait(context.Background(), request.Text)
		// Pi's message_update usage is per model response and can accompany a
		// text delta in the same record. Use the authoritative post-run stats
		// for Viewer's one-block-per-event context indicator instead.
		statsCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		stats, raw, statsErr := current.client.GetSessionStats(statsCtx)
		cancel()
		if statsErr == nil {
			if block, ok := pirpc.SessionStatsBlock(stats); ok {
				p.publishBlock(request.SessionID, current, "get_session_stats", raw, block)
			}
		}
		p.mu.Lock()
		cancelled := current.cancelled
		current.active, current.cancelled, current.turnID = false, false, ""
		p.mu.Unlock()
		reason := "end_turn"
		if cancelled || result.Aborted {
			reason = "cancelled"
		} else if promptErr != nil {
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
		cancelCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = current.client.Abort(cancelCtx)
		cancel()
	}
	p.respond(frame, map[string]any{"stopped": stopped}, err)
}

func (p *Plugin) handleEvent(sessionID string, current *session, event pirpc.Event) {
	p.publishBlock(sessionID, current, event.Type, event.Raw, pirpc.ParseBlock(event.Type, event.Value))
}

func (p *Plugin) publishBlock(sessionID string, current *session, kind string, raw json.RawMessage, block agentdriver.Block) {
	p.mu.Lock()
	if !current.active {
		p.mu.Unlock()
		return
	}
	seq := current.seq
	turnID := current.turnID
	current.seq++
	p.mu.Unlock()
	frame := agentdriver.EventFrame{SessionID: sessionID, TurnID: turnID, Seq: seq, Kind: kind, RawJSON: string(raw), Block: block}
	_ = p.client.Publish(context.Background(), PluginID+":_:event", frame)
}

func newClient(ctx context.Context, cwd string, noSession bool) (*pirpc.Client, error) {
	command := strings.TrimSpace(os.Getenv("VIEWER_PI_COMMAND"))
	if command == "" {
		command = "pi"
	}
	arguments := strings.Fields(os.Getenv("VIEWER_PI_ARGS"))
	if len(arguments) == 0 {
		arguments = []string{"--mode", "rpc"}
	}
	if noSession {
		arguments = append(arguments, "--no-session")
	}
	return pirpc.New(ctx, pirpc.ProcessConfig{Command: command, Arguments: arguments, Dir: cwd})
}

// piModelSelection follows the other drivers' default-provider rule: an empty
// or default provider leaves Pi's own model untouched. Concrete routing
// candidates always carry provider+model and are enforced by Pi itself.
func piModelSelection(target agentdriver.Target) (string, string) {
	provider := strings.TrimSpace(target.Provider)
	model := strings.TrimSpace(target.Model)
	if model == "" || provider == "" || provider == "default" {
		return "", ""
	}
	return provider, model
}

func piThinkingLevel(target agentdriver.Target) string {
	if target.Parameters == nil {
		return ""
	}
	level, _ := target.Parameters["thinking_level"].(string)
	return strings.TrimSpace(level)
}

func (p *Plugin) discoverCatalog(ctx context.Context) (agentdriver.Catalog, error) {
	discoveryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cwd, err := os.UserHomeDir()
	if err != nil || cwd == "" {
		cwd = "/"
	}
	client, err := newClient(discoveryCtx, cwd, true)
	if err != nil {
		return agentdriver.Catalog{}, err
	}
	defer client.Close()
	models, err := client.GetAvailableModels(discoveryCtx)
	if err != nil {
		return agentdriver.Catalog{}, err
	}
	if len(models) == 0 {
		return agentdriver.Catalog{}, errors.New("Pi RPC get_available_models returned no models")
	}
	providers := make([]agentdriver.ProviderCatalog, 0)
	indices := map[string]int{}
	seen := map[string]bool{}
	for _, model := range models {
		index, exists := indices[model.Provider]
		if !exists {
			index = len(providers)
			indices[model.Provider] = index
			providers = append(providers, agentdriver.ProviderCatalog{Provider: model.Provider, Models: []string{}, ParameterSchema: piParameterSchema()})
		}
		key := model.Provider + "\x00" + model.ID
		if !seen[key] {
			seen[key] = true
			providers[index].Models = append(providers[index].Models, model.ID)
		}
	}
	return agentdriver.Catalog{Agent: "pi", Providers: providers}, nil
}

func piParameterSchema() map[string]any {
	return map[string]any{
		"thinking_level": map[string]any{
			"type": "string", "enum": []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"},
			"description": "Pi thinking level; Pi clamps or rejects levels unsupported by the selected model",
		},
	}
}

func (p *Plugin) catalog(ctx context.Context) agentdriver.Catalog {
	result := p.catalogCache.Current()
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

func (p *Plugin) handleCatalogRefresh(frame busclient.Frame) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, err := p.catalogCache.Refresh(ctx)
	if err == nil {
		err = p.client.Set(context.Background(), PluginID+":_:catalog", p.catalog(context.Background()))
	}
	p.respond(frame, map[string]any{"catalog": p.catalog(context.Background())}, err)
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
