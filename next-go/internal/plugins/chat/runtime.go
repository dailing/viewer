package chat

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"viewer/internal/busclient"
)

type dispatchRequest struct {
	ChatID            string   `json:"chat_id"`
	Message           string   `json:"message"`
	Text              string   `json:"text"`
	RoleIDs           []string `json:"role_ids"`
	BeforeMessageID   string   `json:"before_message_id"`
	HistoryWordBudget *int     `json:"history_word_budget"`
}

func runtimeKey(chatID, roleID string) string { return chatID + "\x00" + roleID }

func (p *Plugin) handleDispatch(frame busclient.Frame) {
	value, err := frameObject(frame)
	var request dispatchRequest
	if err == nil {
		err = decodeInto(value, &request)
	}
	if request.Message == "" {
		request.Message = request.Text
	}
	request.Message = strings.TrimSpace(request.Message)
	if err != nil || request.ChatID == "" || request.Message == "" {
		if err == nil {
			err = errBadRequest
		}
		p.reply(frame, nil, err)
		return
	}
	chat, err := p.store.chat(request.ChatID)
	if err != nil || chat == nil {
		if err == nil {
			err = errors.New("chat not found")
		}
		p.reply(frame, nil, err)
		return
	}
	workspace, err := p.workspace(p.ctx)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	selected, rationale, err := p.selectRoles(request, value, *chat, workspace)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	keys := make([]string, 0, len(selected))
	p.mu.Lock()
	for _, role := range selected {
		key := runtimeKey(chat.ID, role.ID)
		if p.busy[key] {
			err = errTurnActive
			break
		}
		keys = append(keys, key)
	}
	if err == nil {
		for _, key := range keys {
			p.busy[key] = true
		}
	}
	p.mu.Unlock()
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	dispatchID := newID()
	user := &Message{ID: newID(), ChatID: chat.ID, TurnID: dispatchID, Role: "user", Text: request.Message, SenderFrom: "user", CreatedAt: nowMillis()}
	if err = p.store.addMessage(user); err != nil {
		p.releaseBusy(keys)
		p.reply(frame, nil, err)
		return
	}
	p.publishMessage(user)
	p.wg.Add(1)
	startGate := make(chan struct{})
	go func() {
		<-startGate
		defer p.wg.Done()
		defer p.releaseBusy(keys)
		p.runRelay(*chat, workspace, selected, request.Message, user.CreatedAt)
	}()
	p.reply(frame, map[string]any{"role_ids": roleIDs(selected), "rationale": rationale, "dispatch_id": dispatchID}, nil)
	close(startGate)
}

func (p *Plugin) selectRoles(request dispatchRequest, raw map[string]any, chat Chat, workspace Workspace) ([]SuperRole, string, error) {
	members := map[string]bool{}
	for _, id := range decodeStrings(chat.MemberRoleIDsJSON) {
		members[id] = true
	}
	candidates := []SuperRole{}
	for _, role := range workspace.Roles {
		if members[role.ID] {
			candidates = append(candidates, role)
		}
	}
	_, explicit := raw["role_ids"]
	if explicit && len(request.RoleIDs) > 0 {
		selected := []SuperRole{}
		seen := map[string]bool{}
		for _, id := range request.RoleIDs {
			role, ok := roleByID(candidates, id)
			if !ok {
				return nil, "", fmt.Errorf("role is not a member of chat: %s", id)
			}
			if !seen[id] {
				selected = append(selected, role)
				seen[id] = true
			}
		}
		return selected, "Explicit role_ids; LLM routing skipped.", nil
	}
	eligible := []SuperRole{}
	for _, role := range candidates {
		if strings.TrimSpace(role.Description) != "" {
			eligible = append(eligible, role)
		}
	}
	if len(eligible) == 0 {
		return nil, "", errors.New("no dispatchable chat roles have descriptions")
	}
	budget := defaultHistoryWordBudget
	if request.HistoryWordBudget != nil {
		budget = *request.HistoryWordBudget
	}
	if budget < 0 {
		budget = 0
	}
	if budget > 8000 {
		budget = 8000
	}
	before := int64(0)
	if request.BeforeMessageID != "" {
		marker, markerErr := p.store.message(request.BeforeMessageID)
		if markerErr != nil {
			return nil, "", markerErr
		}
		if marker == nil || marker.ChatID != chat.ID {
			return nil, "", errors.New("before_message_id was not found in the chat")
		}
		before = marker.CreatedAt
	}
	history, err := p.historyPrompt(chat.ID, before, budget)
	if err != nil {
		return nil, "", err
	}
	config, err := p.llmConfig(p.ctx)
	if err != nil {
		return nil, "", err
	}
	ids, rationale, err := routeWithLLM(p.ctx, p.httpClient, config, request.Message, eligible, history)
	if err != nil {
		return nil, "", err
	}
	selected := []SuperRole{}
	for _, id := range ids {
		if role, ok := roleByID(eligible, id); ok {
			selected = append(selected, role)
		}
	}
	return selected, rationale, nil
}

func roleIDs(roles []SuperRole) []string {
	result := make([]string, 0, len(roles))
	for _, role := range roles {
		result = append(result, role.ID)
	}
	return result
}
func (p *Plugin) releaseBusy(keys []string) {
	p.mu.Lock()
	for _, key := range keys {
		delete(p.busy, key)
	}
	p.mu.Unlock()
}

func (p *Plugin) runRelay(chat Chat, workspace Workspace, roles []SuperRole, message string, before int64) {
	for _, role := range roles {
		turnID := newID()
		turn := &Turn{ID: turnID, ChatID: chat.ID, RoleID: role.ID, RoleName: role.Name, StartedAt: nowMillis()}
		if p.store.beginTurn(turn) != nil {
			continue
		}
		current, fresh, err := p.ensureRuntime(p.ctx, chat, role)
		reason := "error"
		if err == nil {
			prompt := message
			if fresh {
				contextBridge := p.buildNewSessionContext(chat, message, before)
				prompt = initialPrompt(workspace, chat, role, contextBridge, message)
			} else if bridge := p.buildRoleSwitchBridge(chat, role.ID, message, before); bridge != "" {
				prompt = bridge + "\n\nCurrent routed message follows:\n" + message
			}
			p.mu.Lock()
			current.activeTurn, current.cancelRequested, current.roleName, current.eventSeq = turnID, false, role.Name, 0
			p.mu.Unlock()
			reason, err = current.agent.Prompt(p.ctx, current.sessionID, prompt)
			p.mu.Lock()
			cancelled := current.cancelRequested
			if current.activeTurn == turnID {
				current.activeTurn, current.cancelRequested = "", false
			}
			if err != nil {
				delete(p.runtimes, runtimeKey(chat.ID, role.ID))
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
		}
		_ = p.store.completeTurn(turnID, reason)
		p.publish("chat:"+chat.ID+":turn-completed", map[string]any{"chat_id": chat.ID, "turn_id": turnID, "stop_reason": reason, "role_id": role.ID, "role_name": role.Name, "sender": map[string]any{"from": "role", "role_id": role.ID, "role_name": role.Name}})
		if reason != "cancelled" {
			p.wg.Add(1)
			go func(id, provider string) { defer p.wg.Done(); p.generateTurnSummary(id, provider) }(turnID, role.Provider)
		}
		if err != nil || reason == "cancelled" {
			break
		}
	}
}

func initialPrompt(workspace Workspace, chat Chat, role SuperRole, history, message string) string {
	common := strings.TrimSpace(strings.Join(nonEmpty(workspace.CommonPrompt, chat.CommonPrompt), "\n\n"))
	rolePrompt := fmt.Sprintf("You are a persistent Super Workspace role named %q.\n\nRole prompt:\n%s\n\nOperate as this role only. Prefer work that matches the fixed rules, files, topic, and responsibilities above. If a later user message appears unrelated to this role, say so briefly and ask for clarification instead of silently switching tasks.", role.Name, fallback(role.Prompt, "(No role-specific prompt was provided.)"))
	sections := nonEmpty(common, rolePrompt)
	if history != "" {
		sections = append(sections, history+"\n\nCurrent routed message follows:")
	}
	sections = append(sections, message)
	return strings.Join(sections, "\n\n")
}
func nonEmpty(values ...string) []string {
	result := []string{}
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, strings.TrimSpace(value))
		}
	}
	return result
}
func fallback(value, fallbackValue string) string {
	if strings.TrimSpace(value) == "" {
		return fallbackValue
	}
	return value
}

func (p *Plugin) historyPrompt(chatID string, before int64, budget int) (string, error) {
	messages, err := p.store.history(chatID, before, budget)
	if err != nil {
		return "", err
	}
	lines := []string{}
	for _, message := range messages {
		sender := "User"
		if message.RoleID != "" {
			sender = message.RoleName
		}
		lines = append(lines, fmt.Sprintf("%s: %s", sender, message.Text))
	}
	if len(lines) == 0 {
		return "", nil
	}
	return "Recent visible chat history:\n" + strings.Join(lines, "\n"), nil
}

func (p *Plugin) ensureRuntime(ctx context.Context, chat Chat, role SuperRole) (*runtime, bool, error) {
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
	p.mu.Lock()
	existing := p.runtimes[key]
	if existing != nil && role.SessionPolicy != "new_each_run" && existing.cwd == absolute {
		p.mu.Unlock()
		return existing, false, nil
	}
	if existing != nil {
		delete(p.runtimes, key)
	}
	p.mu.Unlock()
	if existing != nil {
		_ = existing.agent.Close()
	}
	process, profile, err := p.agentForRole(ctx, role)
	if err != nil {
		return nil, false, err
	}
	current := &runtime{agent: process, profile: profile, cwd: absolute, roleID: role.ID, roleName: role.Name}
	process.OnUpdate(func(update driverEvent) { p.handleUpdate(chat.ID, role.ID, update) })
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err = process.Initialize(initCtx); err != nil {
		_ = process.Close()
		return nil, false, fmt.Errorf("initialize %s driver: %w", role.Provider, err)
	}
	state, err := p.store.roleSession(chat.ID, role.ID)
	if err != nil {
		_ = process.Close()
		return nil, false, err
	}
	sessionID := ""
	newSession := true
	if role.SessionPolicy != "new_each_run" && state != nil && state.Provider == role.Provider && state.ProviderProfile == profile && state.CWD == absolute {
		if process.LoadSession(initCtx, state.ProviderSessionID, absolute) == nil {
			sessionID = state.ProviderSessionID
			newSession = false
		}
	}
	if sessionID == "" {
		sessionID, err = process.NewSession(initCtx, absolute)
		if err != nil {
			_ = process.Close()
			return nil, false, fmt.Errorf("create %s session: %w", role.Provider, err)
		}
		err = p.store.saveRoleSession(&RoleSession{ChatID: chat.ID, RoleID: role.ID, Provider: role.Provider, ProviderProfile: profile, ProviderSessionID: sessionID, CWD: absolute, UpdatedAt: nowMillis()})
		if err != nil {
			_ = process.Close()
			return nil, false, err
		}
	}
	current.sessionID = sessionID
	p.mu.Lock()
	p.runtimes[key] = current
	p.mu.Unlock()
	return current, newSession, nil
}

func (p *Plugin) handleUpdate(chatID, roleID string, update driverEvent) {
	p.mu.Lock()
	current := p.runtimes[runtimeKey(chatID, roleID)]
	if current == nil || current.activeTurn == "" || update.SessionID != current.sessionID {
		p.mu.Unlock()
		return
	}
	turnID, roleName, seq := current.activeTurn, current.roleName, current.eventSeq
	current.eventSeq++
	p.mu.Unlock()
	occurredAt := nowMillis()
	kind := update.Kind
	if kind == "" {
		kind = "unknown"
	}
	event := &TurnEvent{ID: newID(), ChatID: chatID, TurnID: turnID, RoleID: roleID, Provider: update.Provider, SessionID: update.SessionID, Seq: seq, Kind: kind, RawJSON: string(update.Raw), OccurredAt: occurredAt}
	if err := p.store.addTurnEvent(event); err != nil {
		log.Printf("viewer-chat raw event persistence failed chat_id=%s turn_id=%s provider=%s kind=%s: %v", chatID, turnID, update.Provider, kind, err)
	} else if block, err := deriveMessageBlock(event, update.Data); err != nil {
		log.Printf("viewer-chat message block derivation failed event_id=%s: %v", event.ID, err)
	} else if err = p.store.addMessageBlock(block); err != nil {
		log.Printf("viewer-chat message block persistence failed event_id=%s kind=%s: %v", event.ID, block.Kind, err)
	}
	if text := update.Text; text != "" {
		message := &Message{ID: newID(), ChatID: chatID, TurnID: turnID, Role: "assistant", Text: text, SenderFrom: "role", RoleID: roleID, RoleName: roleName, CreatedAt: nowMillis()}
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

func (p *Plugin) handleStop(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	roleID, _ := value["role_id"].(string)
	if err == nil && chatID == "" {
		err = errBadRequest
	}
	stopped := false
	if err == nil {
		stopped, err = p.stopTurn(chatID, roleID)
	}
	p.reply(frame, map[string]any{"stopped": stopped}, err)
}
func (p *Plugin) stopTurn(chatID, roleID string) (bool, error) {
	p.mu.Lock()
	targets := []*runtime{}
	for key, current := range p.runtimes {
		if strings.HasPrefix(key, chatID+"\x00") && current.activeTurn != "" && (roleID == "" || current.roleID == roleID) {
			current.cancelRequested = true
			targets = append(targets, current)
		}
	}
	p.mu.Unlock()
	var result error
	for _, current := range targets {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := current.agent.Cancel(ctx, current.sessionID)
		cancel()
		result = errors.Join(result, err)
	}
	return len(targets) > 0, result
}
func (p *Plugin) publishMessage(message *Message) {
	p.publish("chat:"+message.ChatID+":message", message.payload())
}
func (p *Plugin) publish(channel string, value any) {
	if p.client != nil {
		_ = p.client.Publish(context.Background(), channel, value)
	}
}
