package chat

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"viewer/internal/agentdriver"
	"viewer/internal/busclient"
)

func defaultAgents() map[string]string {
	return map[string]string{"hermes": "viewer.agent-hermes", "codex-app-server": "viewer.agent-codex", "opencode": "viewer.agent-opencode"}
}

func pluginFromChannel(channel string) string {
	if index := strings.IndexByte(channel, ':'); index >= 0 {
		return channel[:index]
	}
	return ""
}

func (p *Plugin) handleAgentBusFrame(frame busclient.Frame) {
	switch {
	case strings.HasSuffix(frame.Channel, ":_:catalog"):
		p.trackCatalog(frame)
	case strings.HasSuffix(frame.Channel, ":_:event"):
		p.handleAgentEvent(frame)
	case strings.HasSuffix(frame.Channel, ":_:turn-ended"):
		p.handleAgentTurnEnded(frame)
	}
}

func (p *Plugin) trackCatalog(frame busclient.Frame) {
	pluginID := pluginFromChannel(frame.Channel)
	var catalog agentdriver.Catalog
	if frame.Value != nil && decodeInto(frame.Value, &catalog) == nil && catalog.Agent != "" {
		p.mu.Lock()
		p.catalogs[pluginID] = catalog
		p.mu.Unlock()
	} else {
		p.mu.Lock()
		delete(p.catalogs, pluginID)
		p.mu.Unlock()
	}
}

func (p *Plugin) handleAgentCatalog(frame busclient.Frame) {
	p.mu.Lock()
	result := make([]map[string]any, 0, len(p.agents))
	agentIDs := make([]string, 0, len(p.agents))
	for agentID := range p.agents {
		agentIDs = append(agentIDs, agentID)
	}
	sort.Strings(agentIDs)
	for _, agentID := range agentIDs {
		pluginID := p.agents[agentID]
		catalog, online := p.catalogs[pluginID]
		item := map[string]any{"agent": agentID, "plugin_id": pluginID, "online": online, "providers": []agentdriver.ProviderCatalog{}}
		if online {
			item["providers"] = catalog.Providers
		}
		result = append(result, item)
	}
	p.mu.Unlock()
	p.reply(frame, result, nil)
}

func (p *Plugin) handleAgentEvent(frame busclient.Frame) {
	var update agentdriver.EventFrame
	if decodeInto(frame.Value, &update) != nil || update.SessionID == "" {
		return
	}
	pluginID := pluginFromChannel(frame.Channel)
	p.mu.Lock()
	var current *runtime
	for _, item := range p.runtimes {
		if item.pluginID == pluginID && item.sessionID == update.SessionID && item.activeTurn != "" {
			current = item
			break
		}
	}
	if current == nil {
		p.mu.Unlock()
		return
	}
	turnID, roleID, roleName, provider := current.activeTurn, current.roleID, current.roleName, current.providerKey
	p.mu.Unlock()
	occurredAt := nowMillis()
	event := &TurnEvent{ID: newID(), ChatID: chatIDForTurn(p, turnID), TurnID: turnID, RoleID: roleID, Provider: provider, SessionID: update.SessionID, Seq: update.Seq, Kind: fallback(update.Kind, "unknown"), RawJSON: update.RawJSON, OccurredAt: occurredAt}
	if err := p.store.addTurnEvent(event); err != nil {
		log.Printf("viewer-chat raw event persistence failed turn_id=%s provider=%s kind=%s: %v", turnID, provider, update.Kind, err)
	} else {
		block := &MessageBlock{ID: newID(), EventID: event.ID, ChatID: event.ChatID, TurnID: turnID, Kind: update.Block.Kind, Text: update.Block.Text, Payload: update.Block.Payload, OccurredAt: occurredAt}
		if block.Kind == "" {
			block.Kind = agentdriver.KindOther
		}
		if block.Payload == "" {
			block.Payload = "{}"
		}
		if err := p.store.addMessageBlock(block); err != nil {
			log.Printf("viewer-chat message block persistence failed event_id=%s: %v", event.ID, err)
		}
	}
	if update.Block.Kind == agentdriver.KindAgentText && update.Block.Text != "" {
		message := &Message{ID: newID(), ChatID: event.ChatID, TurnID: turnID, Role: "assistant", Text: update.Block.Text, SenderFrom: "role", RoleID: roleID, RoleName: roleName, CreatedAt: nowMillis()}
		if p.store.addMessage(message) == nil {
			p.publishMessage(message)
		}
	}
}

func chatIDForTurn(p *Plugin, turnID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, current := range p.runtimes {
		if current.activeTurn == turnID {
			return strings.SplitN(key, "\x00", 2)[0]
		}
	}
	return ""
}

func (p *Plugin) handleAgentTurnEnded(frame busclient.Frame) {
	var update struct {
		SessionID  string `json:"session_id"`
		StopReason string `json:"stop_reason"`
	}
	if decodeInto(frame.Value, &update) != nil {
		return
	}
	pluginID := pluginFromChannel(frame.Channel)
	p.mu.Lock()
	for _, current := range p.runtimes {
		if current.pluginID == pluginID && current.sessionID == update.SessionID && current.activeTurn != "" && current.ended != nil {
			select {
			case current.ended <- fallback(update.StopReason, "end_turn"):
			default:
			}
			break
		}
	}
	p.mu.Unlock()
}

type resolvedCandidate struct {
	target   agentdriver.Target
	pluginID string
}

func (p *Plugin) resolveCandidates(chat Chat, workspace Workspace, role SuperRole) ([]resolvedCandidate, error) {
	policyID := role.RoutingPolicyID
	if override := decodeStringMap(chat.RoleRoutingOverridesJSON)[role.ID]; override != "" {
		policyID = override
	}
	if policyID == "" {
		policyID = workspace.DefaultRoutingPolicyID
	}
	var policy *RoutingPolicyConfig
	for index := range workspace.RoutingPolicies {
		if workspace.RoutingPolicies[index].ID == policyID {
			policy = &workspace.RoutingPolicies[index]
			break
		}
	}
	configs := []RoutingCandidateConfig{}
	maxAttempts := 1
	if policy != nil {
		if !policy.Enabled {
			return nil, errors.New("routing policy is disabled")
		}
		configs = policy.Candidates
		if policy.AutoFailover {
			maxAttempts = policy.MaxAttempts
			if maxAttempts <= 0 {
				maxAttempts = 1
			}
		}
	} else {
		agentID, providerID := role.Provider, role.Provider
		if agentID == "hermes" {
			providerID = "default"
		}
		model := ""
		if role.Model != nil {
			model = strings.TrimSpace(*role.Model)
		}
		configs = []RoutingCandidateConfig{{AgentID: agentID, ProviderID: providerID, ModelID: model, Enabled: true, Parameters: map[string]any{}}}
	}
	result := []resolvedCandidate{}
	for _, candidate := range configs {
		if !candidate.Enabled {
			continue
		}
		agentID := strings.TrimSpace(candidate.AgentID)
		if agentID == "" {
			agentID = strings.TrimSpace(candidate.TargetID)
		}
		p.mu.Lock()
		pluginID := p.agents[agentID]
		_, online := p.catalogs[pluginID]
		p.mu.Unlock()
		if pluginID == "" || !online {
			continue
		}
		parameters := candidate.Parameters
		if parameters == nil {
			parameters = map[string]any{}
		}
		result = append(result, resolvedCandidate{target: agentdriver.Target{Agent: agentID, Provider: candidate.ProviderID, Model: candidate.ModelID, Parameters: parameters}, pluginID: pluginID})
		if len(result) >= maxAttempts {
			break
		}
	}
	if len(result) == 0 {
		return nil, errors.New("no enabled online routing candidate")
	}
	return result, nil
}

func candidateKey(target agentdriver.Target) string { return target.Agent + "/" + target.Provider }

func candidateProfile(target agentdriver.Target) string {
	value := struct {
		Model      string         `json:"model"`
		Parameters map[string]any `json:"parameters"`
	}{target.Model, target.Parameters}
	encoded, err := json.Marshal(value)
	if err != nil {
		return target.Model
	}
	return string(encoded)
}

func (p *Plugin) ensureBusRuntime(ctx context.Context, chat Chat, role SuperRole, candidate resolvedCandidate) (*runtime, bool, error) {
	key := runtimeKey(chat.ID, role.ID)
	effectiveCWD := chat.Root
	if role.CWD != "" {
		if filepath.IsAbs(role.CWD) {
			effectiveCWD = role.CWD
		} else {
			effectiveCWD = filepath.Join(chat.Root, role.CWD)
		}
	}
	absolute, err := filepath.Abs(effectiveCWD)
	if err != nil {
		return nil, false, err
	}
	providerKey := candidateKey(candidate.target)
	profile := candidateProfile(candidate.target)
	p.mu.Lock()
	existing := p.runtimes[key]
	if existing != nil && existing.pluginID == candidate.pluginID && existing.providerKey == providerKey && existing.profile == profile && existing.cwd == absolute && role.SessionPolicy != "new_each_run" {
		p.mu.Unlock()
		return existing, false, nil
	}
	p.mu.Unlock()
	state, err := p.store.roleSession(chat.ID, role.ID)
	if err != nil {
		return nil, false, err
	}
	requested := ""
	if role.SessionPolicy != "new_each_run" && state != nil && state.Provider == providerKey && state.ProviderProfile == profile && state.CWD == absolute {
		requested = state.ProviderSessionID
	}
	value, err := p.client.Request(ctx, candidate.pluginID+":_:start", map[string]any{"cwd": absolute, "target": candidate.target, "session_id": requested}, 30*time.Second)
	if err != nil {
		return nil, false, err
	}
	var started struct {
		SessionID string `json:"session_id"`
		Resumed   bool   `json:"resumed"`
	}
	if err = decodeInto(value, &started); err != nil || started.SessionID == "" {
		return nil, false, errors.New("agent start returned no session_id")
	}
	current := &runtime{sessionID: started.SessionID, profile: profile, cwd: absolute, roleID: role.ID, roleName: role.Name, pluginID: candidate.pluginID, providerKey: providerKey, target: candidate.target, ended: make(chan string, 1)}
	if err = p.store.saveRoleSession(&RoleSession{ChatID: chat.ID, RoleID: role.ID, Provider: providerKey, ProviderProfile: profile, ProviderSessionID: started.SessionID, CWD: absolute, UpdatedAt: nowMillis()}); err != nil {
		return nil, false, err
	}
	p.mu.Lock()
	p.runtimes[key] = current
	p.mu.Unlock()
	return current, !started.Resumed, nil
}

func (p *Plugin) promptBus(ctx context.Context, current *runtime, text string) (string, error) {
	for {
		select {
		case <-current.ended:
		default:
			goto drained
		}
	}
drained:
	if _, err := p.client.Request(ctx, current.pluginID+":_:prompt", map[string]any{"session_id": current.sessionID, "text": text}, 30*time.Second); err != nil {
		return "error", err
	}
	select {
	case reason := <-current.ended:
		return reason, nil
	case <-ctx.Done():
		return "error", ctx.Err()
	}
}
