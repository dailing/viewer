package chat

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"viewer/internal/agentdriver"
	"viewer/sdk/go/busclient"
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

// handleAgentCatalogRefresh is the on-demand protocol-discovery trigger: the
// chat manager calls it when it opens (there is no periodic discovery
// anywhere). It fans out <plugin>:_:catalog-refresh to every agent plugin in
// parallel; each plugin re-runs wire-protocol discovery (ACP session/new
// models/configOptions, codex model/list) and re-publishes its retained
// catalog mailbox. Successful responses are adopted directly so the reply is
// fresh even before the mailbox frames land; failed plugins keep their
// previous catalog. The reply has the same shape as handleAgentCatalog.
func (p *Plugin) handleAgentCatalogRefresh(frame busclient.Frame) {
	p.mu.Lock()
	targets := make([]string, 0, len(p.agents))
	seen := map[string]bool{}
	for _, pluginID := range p.agents {
		if pluginID != "" && !seen[pluginID] {
			seen[pluginID] = true
			targets = append(targets, pluginID)
		}
	}
	p.mu.Unlock()
	type refreshResult struct {
		pluginID string
		catalog  agentdriver.Catalog
	}
	results := make(chan refreshResult, len(targets))
	var wg sync.WaitGroup
	for _, pluginID := range targets {
		wg.Add(1)
		go func(pluginID string) {
			defer wg.Done()
			value, err := p.client.Request(p.ctx, pluginID+":_:catalog-refresh", map[string]any{}, 45*time.Second)
			if err != nil {
				log.Printf("viewer-chat catalog refresh failed plugin=%s: %v", pluginID, err)
				return
			}
			var catalog agentdriver.Catalog
			if decodeInto(value, &catalog) != nil || catalog.Agent == "" {
				return
			}
			results <- refreshResult{pluginID: pluginID, catalog: catalog}
		}(pluginID)
	}
	wg.Wait()
	close(results)
	p.mu.Lock()
	for result := range results {
		p.catalogs[result.pluginID] = result.catalog
	}
	p.mu.Unlock()
	p.handleAgentCatalog(frame)
}

func (p *Plugin) handleAgentEvent(frame busclient.Frame) {
	var update agentdriver.EventFrame
	if decodeInto(frame.Value, &update) != nil || update.SessionID == "" || update.TurnID == "" {
		return
	}
	pluginID := pluginFromChannel(frame.Channel)
	p.mu.Lock()
	var current *runtime
	for _, item := range p.runtimes {
		if item.pluginID == pluginID && item.activeTurn == update.TurnID {
			current = item
			break
		}
	}
	if current == nil {
		p.mu.Unlock()
		return
	}
	turnID, roleID, roleName, provider := update.TurnID, current.roleID, current.roleName, current.providerKey
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
		// Streaming kinds (agent_text/thinking) arrive as many tiny deltas;
		// merge consecutive same-kind deltas into one open block per segment so
		// the frontend renders one activity row, not one row per chunk. A
		// different kind seals the segment; the next delta opens a new block.
		streaming := block.Kind == agentdriver.KindAgentText || block.Kind == agentdriver.KindThinking
		p.mu.Lock()
		open := p.openBlock[turnID]
		merge := streaming && open != nil && open.Kind == block.Kind
		if open != nil && !merge {
			delete(p.openBlock, turnID)
		}
		if merge {
			open.Text += block.Text
			block = open
		} else if streaming {
			p.openBlock[turnID] = block
		}
		p.mu.Unlock()
		var err error
		if merge {
			err = p.store.updateMessageBlockText(block.ID, block.Text)
		} else {
			err = p.store.addMessageBlock(block)
		}
		if err != nil {
			log.Printf("viewer-chat message block persistence failed event_id=%s: %v", event.ID, err)
		} else {
			payload := block.payload()
			payload["role_id"] = roleID
			payload["role_name"] = roleName
			p.publish("chat:"+block.ChatID+":block", payload)
		}
	}
	if update.Block.Kind == agentdriver.KindAgentText && update.Block.Text != "" {
		// Deltas aggregate into one open message per segment: append while no
		// non-text block intervenes, keep created_at at first-chunk time.
		p.mu.Lock()
		open, exists := p.openText[turnID]
		if !exists {
			open = &Message{ID: newID(), ChatID: event.ChatID, TurnID: turnID, Role: "assistant", SenderFrom: "role", RoleID: roleID, RoleName: roleName, CreatedAt: occurredAt}
			p.openText[turnID] = open
		}
		open.Text += update.Block.Text
		message := *open
		p.mu.Unlock()
		var err error
		if exists {
			err = p.store.updateMessageText(message.ID, message.Text)
		} else {
			err = p.store.addMessage(&message)
		}
		if err == nil {
			p.publishMessage(&message)
		}
	} else if update.Block.Kind != agentdriver.KindAgentText {
		// A non-text block seals the current text segment; the next delta opens a new one.
		p.mu.Lock()
		delete(p.openText, turnID)
		p.mu.Unlock()
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
	var update agentdriver.TurnEndedFrame
	if decodeInto(frame.Value, &update) != nil || update.TurnID == "" {
		return
	}
	pluginID := pluginFromChannel(frame.Channel)
	p.mu.Lock()
	delete(p.openText, update.TurnID)
	delete(p.openBlock, update.TurnID)
	for _, current := range p.runtimes {
		if current.pluginID == pluginID && current.activeTurn == update.TurnID && current.ended != nil {
			select {
			case current.ended <- turnEnd{reason: fallback(update.StopReason, "end_turn"), err: update.Error}:
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
		return nil, errors.New("role has no routing policy")
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

func (p *Plugin) ensureBusRuntime(ctx context.Context, chat Chat, role SuperRole, candidate resolvedCandidate, turnID string, forceNew bool) (*runtime, bool, error) {
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
	if forceNew && existing != nil {
		// One-shot new-session request: drop the in-memory runtime so the
		// start below creates a fresh agent session. The old session stays
		// resident inside the agent plugin (no close RPC exists; user-facing
		// resources are never auto-reaped).
		delete(p.runtimes, key)
		existing = nil
	}
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
	if !forceNew && role.SessionPolicy != "new_each_run" && state != nil && state.Provider == providerKey && state.ProviderProfile == profile && state.CWD == absolute {
		requested = state.ProviderSessionID
	}
	value, err := p.client.Request(ctx, candidate.pluginID+":_:start", map[string]any{"cwd": absolute, "target": candidate.target, "session_id": requested, "turn_id": turnID}, 30*time.Second)
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
	current := &runtime{sessionID: started.SessionID, profile: profile, cwd: absolute, roleID: role.ID, roleName: role.Name, pluginID: candidate.pluginID, providerKey: providerKey, target: candidate.target, ended: make(chan turnEnd, 1)}
	if err = p.store.saveRoleSession(&RoleSession{ChatID: chat.ID, RoleID: role.ID, Provider: providerKey, ProviderProfile: profile, ProviderSessionID: started.SessionID, CWD: absolute, UpdatedAt: nowMillis()}); err != nil {
		return nil, false, err
	}
	p.mu.Lock()
	p.runtimes[key] = current
	p.mu.Unlock()
	return current, !started.Resumed, nil
}

func (p *Plugin) promptBus(ctx context.Context, current *runtime, turnID, text string) (turnEnd, error) {
	for {
		select {
		case <-current.ended:
		default:
			goto drained
		}
	}
drained:
	if _, err := p.client.Request(ctx, current.pluginID+":_:prompt", map[string]any{"session_id": current.sessionID, "turn_id": turnID, "text": text}, 30*time.Second); err != nil {
		return turnEnd{reason: "error"}, err
	}
	select {
	case end := <-current.ended:
		return end, nil
	case <-ctx.Done():
		return turnEnd{reason: "error"}, ctx.Err()
	}
}
