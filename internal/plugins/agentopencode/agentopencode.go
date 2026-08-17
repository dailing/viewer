// Package agentopencode implements the headless viewer.agent-opencode bus plugin.
package agentopencode

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

	"viewer/internal/acp"
	"viewer/internal/agentdriver"
	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

const (
	PluginID        = "viewer.agent-opencode"
	configNamespace = "plugins.viewer-agent-opencode"
)

var Manifest = busclient.Manifest{
	ID: PluginID, Version: "0.2.0",
	Slots: map[string]any{
		PluginID + ":_:start": map[string]any{}, PluginID + ":_:prompt": map[string]any{}, PluginID + ":_:cancel": map[string]any{},
		PluginID + ":_:catalog-refresh": map[string]any{},
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
	client       *busclient.Client
	mu           sync.Mutex
	sessions     map[string]*session
	wg           sync.WaitGroup
	closed       bool
	catalogCache *agentdriver.CatalogCache
}

func New() *Plugin {
	p := &Plugin{sessions: map[string]*session{}}
	fallback := agentdriver.Catalog{Agent: "opencode", Providers: []agentdriver.ProviderCatalog{{Provider: "default", Models: []string{}}}}
	p.catalogCache = agentdriver.NewCatalogCache(fallback, p.discoverCatalog)
	return p
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
			log.Printf("viewer-agent-opencode catalog publish failed: %v", err)
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
	if err == nil && request.Target.Agent != "" && request.Target.Agent != "opencode" {
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
	client, err := p.newClient(context.Background())
	if err != nil {
		p.respond(frame, nil, err)
		return
	}
	initCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err = client.Initialize(initCtx); err != nil {
		_ = client.Close()
		p.respond(frame, nil, fmt.Errorf("initialize opencode: %w", err))
		return
	}
	sessionID, resumed := request.SessionID, false
	if sessionID != "" && client.LoadSession(initCtx, sessionID, request.CWD) == nil {
		resumed = true
	} else {
		var info acp.SessionInfo
		info, err = client.NewSession(initCtx, request.CWD)
		sessionID = info.ID
	}
	if err != nil {
		_ = client.Close()
		p.respond(frame, nil, fmt.Errorf("start opencode session: %w", err))
		return
	}
	// Enforce the routing profile's model choice over ACP: opencode validates
	// the value server-side, so an unroutable selection fails the start instead
	// of silently running the default model.
	if value := opencodeModelValue(request.Target); value != "" {
		if err = client.SetConfigOption(initCtx, sessionID, "model", value); err != nil {
			_ = client.Close()
			p.respond(frame, nil, fmt.Errorf("set opencode model %q: %w", value, err))
			return
		}
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

func (p *Plugin) newClient(ctx context.Context) (*acp.Client, error) {
	command := strings.TrimSpace(os.Getenv("VIEWER_OPENCODE_COMMAND"))
	if command == "" {
		command = "opencode"
	}
	arguments := strings.TrimSpace(os.Getenv("VIEWER_OPENCODE_ARGS"))
	if arguments == "" {
		arguments = "acp"
	}
	return acp.New(ctx, command, strings.Fields(arguments)...)
}

// opencodeModelValue encodes the routing profile's provider+model using
// opencode's "provider/model" selection syntax (session/set_config_option
// configId "model"). An empty or "default" provider means "keep the agent's
// own default model", i.e. no enforcement.
func opencodeModelValue(target agentdriver.Target) string {
	model := strings.TrimSpace(target.Model)
	if model == "" {
		return ""
	}
	provider := strings.TrimSpace(target.Provider)
	if provider == "" || provider == "default" {
		return ""
	}
	return provider + "/" + model
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

// discoverCatalog enumerates providers/models over ACP: opencode does not
// implement SessionModelState, but session/new returns configOptions whose
// select-typed "model" option lists every configured "provider/model" choice
// (values validated server-side by session/set_config_option). The discovery
// session is closed immediately; no prompt is ever sent.
func (p *Plugin) discoverCatalog(ctx context.Context) (agentdriver.Catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := p.newClient(ctx)
	if err != nil {
		return agentdriver.Catalog{}, err
	}
	defer func() { _ = client.Close() }()
	if _, err = client.Initialize(ctx); err != nil {
		return agentdriver.Catalog{}, fmt.Errorf("initialize opencode: %w", err)
	}
	cwd, err := os.UserHomeDir()
	if err != nil || cwd == "" {
		cwd = "/"
	}
	info, err := client.NewSession(ctx, cwd)
	if err != nil {
		return agentdriver.Catalog{}, fmt.Errorf("opencode session/new: %w", err)
	}
	providers := agentdriver.GroupModelIDs(modelOptionValues(info.ConfigOptions), "/")
	if len(providers) == 0 {
		return agentdriver.Catalog{}, errors.New("opencode ACP session/new returned no model options")
	}
	return agentdriver.Catalog{Agent: "opencode", Providers: providers}, nil
}

// modelOptionValues extracts the choices of the ACP config option carrying
// opencode's model picker (select-typed, id "model").
func modelOptionValues(options []acp.ConfigOption) []string {
	for _, option := range options {
		if option.ID != "model" {
			continue
		}
		values := make([]string, 0, len(option.Options))
		for _, choice := range option.Options {
			values = append(values, choice.Value)
		}
		return values
	}
	return nil
}

// handleCatalogRefresh forces one discovery round and republishes the
// retained catalog mailbox. The previous catalog is kept when discovery
// fails, so a manual refresh can never blank the RoutesPanel pickers.
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
