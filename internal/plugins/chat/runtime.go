package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"viewer/sdk/go/busclient"
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
		candidates, err := p.resolveCandidates(chat, workspace, role)
		reason, summaryProvider := "error", ""
		attempts := []map[string]any{}
		for _, candidate := range candidates {
			var current *runtime
			var fresh bool
			attempt := map[string]any{"agent": candidate.target.Agent, "provider": candidate.target.Provider, "model": candidate.target.Model}
			current, fresh, err = p.ensureBusRuntime(p.ctx, chat, role, candidate, turnID)
			if err != nil {
				attempt["outcome"], attempt["error"] = "start_error", err.Error()
				attempts = append(attempts, attempt)
				continue
			}
			summaryProvider = candidate.target.Agent
			prompt := message
			if fresh {
				contextBridge := p.buildNewSessionContext(chat, message, before)
				prompt = initialPrompt(workspace, chat, role, contextBridge, message)
			} else if bridge := p.buildRoleSwitchBridge(chat, role.ID, message, before); bridge != "" {
				prompt = bridge + "\n\nCurrent routed message follows:\n" + message
			}
			p.mu.Lock()
			current.activeTurn, current.cancelRequested, current.roleName = turnID, false, role.Name
			if current.ended == nil {
				current.ended = make(chan string, 1)
			}
			p.mu.Unlock()
			reason, err = p.promptBus(p.ctx, current, turnID, prompt)
			p.mu.Lock()
			cancelled := current.cancelRequested
			if current.activeTurn == turnID {
				current.activeTurn, current.cancelRequested = "", false
			}
			if err != nil {
				delete(p.runtimes, runtimeKey(chat.ID, role.ID))
			}
			p.mu.Unlock()
			if cancelled {
				reason = "cancelled"
			} else if err != nil {
				reason = "error"
			} else if reason == "" {
				reason = "end_turn"
			}
			if cancelled || reason == "cancelled" {
				attempt["outcome"] = "cancelled"
				attempts = append(attempts, attempt)
				break
			}
			if err != nil {
				attempt["outcome"], attempt["error"] = "prompt_error", err.Error()
				attempts = append(attempts, attempt)
				continue
			}
			if reason == "error" {
				attempt["outcome"] = "turn_error"
				attempts = append(attempts, attempt)
				continue
			}
			attempt["outcome"] = "completed"
			attempts = append(attempts, attempt)
			break
		}
		_ = p.store.completeTurn(turnID, reason)
		p.publish("chat:"+chat.ID+":turn-completed", map[string]any{"chat_id": chat.ID, "turn_id": turnID, "stop_reason": reason, "role_id": role.ID, "role_name": role.Name, "attempts": attempts, "sender": map[string]any{"from": "role", "role_id": role.ID, "role_name": role.Name}})
		if reason != "cancelled" {
			p.wg.Add(1)
			go func(id, provider string) { defer p.wg.Done(); p.generateTurnSummary(id, provider) }(turnID, summaryProvider)
		}
		if err != nil || reason == "error" || reason == "cancelled" {
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
		var err error
		_, err = p.client.Request(ctx, current.pluginID+":_:cancel", map[string]any{"session_id": current.sessionID}, 5*time.Second)
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
