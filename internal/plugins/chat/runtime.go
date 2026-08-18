package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	// ForceNewSession makes every selected role start a fresh agent session
	// for this message instead of reusing the stored one (one-shot, set by
	// the composer's new-session toggle).
	ForceNewSession bool `json:"force_new_session"`
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
		p.runRelay(*chat, workspace, selected, request.Message, user.CreatedAt, request.ForceNewSession)
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

func (p *Plugin) runRelay(chat Chat, workspace Workspace, roles []SuperRole, message string, before int64, forceNew bool) {
	for _, role := range roles {
		turnID := newID()
		turn := &Turn{ID: turnID, ChatID: chat.ID, RoleID: role.ID, RoleName: role.Name, StartedAt: nowMillis()}
		if err := p.store.beginTurn(turn); err != nil {
			slog.Error("chat turn persistence failed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "error", err)
			continue
		}
		candidates, err := p.resolveCandidates(chat, workspace, role)
		reason, summaryProvider := "error", ""
		endErr := ""
		attempts := []map[string]any{}
		if err != nil {
			slog.Warn("chat routing candidate resolution failed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "error", err)
		}
	candidateLoop:
		for _, candidate := range candidates {
			retryFresh := false
			for {
				var current *runtime
				var fresh bool
				attempt := map[string]any{"agent": candidate.target.Agent, "provider": candidate.target.Provider, "model": candidate.target.Model}
				current, fresh, err = p.ensureBusRuntime(p.ctx, chat, role, candidate, turnID, forceNew || retryFresh)
				if err != nil {
					attempt["outcome"], attempt["error"] = "start_error", err.Error()
					attempts = append(attempts, attempt)
					slog.Warn("agent start failed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "agent", candidate.target.Agent, "provider", candidate.target.Provider, "model", candidate.target.Model, "error", err)
					continue candidateLoop
				}
				summaryProvider = candidate.target.Agent
				prompt := message
				contextBytes, promptMode := 0, "existing_session"
				if fresh {
					contextBridge := p.buildNewSessionContext(chat, message, before)
					contextBytes, promptMode = len(contextBridge), "new_session"
					prompt = initialPrompt(workspace, chat, role, contextBridge, message)
				} else if bridge := p.buildRoleSwitchBridge(chat, role.ID, message, before); bridge != "" {
					contextBytes, promptMode = len(bridge), "role_switch"
					prompt = bridge + "\n\nCurrent routed message follows:\n" + message
				}
				slog.Info("agent prompt prepared",
					"chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID,
					"agent", candidate.target.Agent, "provider", candidate.target.Provider, "model", candidate.target.Model,
					"mode", promptMode, "prompt_bytes", len(prompt), "context_bytes", contextBytes,
					"message_bytes", len(message), "common_prompt_bytes", len(commonPrompt(workspace, chat)), "role_prompt_bytes", len(initialRolePrompt(role)),
				)
				p.mu.Lock()
				current.activeTurn, current.cancelRequested, current.sawEvent, current.roleName = turnID, false, false, role.Name
				if current.ended == nil {
					current.ended = make(chan turnEnd, 1)
				}
				p.mu.Unlock()
				var end turnEnd
				end, err = p.promptBus(p.ctx, current, turnID, prompt)
				reason = end.reason
				if end.err != "" {
					endErr = end.err
				}
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
					break candidateLoop
				}
				if err != nil {
					attempt["outcome"], attempt["error"] = "prompt_error", err.Error()
					attempts = append(attempts, attempt)
					slog.Warn("agent prompt failed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "agent", candidate.target.Agent, "provider", candidate.target.Provider, "model", candidate.target.Model, "error", err)
					continue candidateLoop
				}
				if reason == "error" {
					attempt["outcome"] = "turn_error"
					if endErr != "" {
						attempt["error"] = endErr
					}
					attempts = append(attempts, attempt)
					slog.Warn("agent turn ended with error", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "agent", candidate.target.Agent, "provider", candidate.target.Provider, "model", candidate.target.Model, "error", endErr)
					continue candidateLoop
				}
				if shouldRetryFreshHermesSession(candidate.target.Agent, fresh, reason, end.hadEvents, retryFresh) {
					attempt["outcome"] = "stale_session_retry"
					attempts = append(attempts, attempt)
					slog.Warn("hermes session refused without events; retrying with fresh session", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "session_id", current.sessionID)
					retryFresh = true
					continue
				}
				attempt["outcome"] = "completed"
				attempts = append(attempts, attempt)
				break candidateLoop
			}
		}
		if reason != "end_turn" && reason != "cancelled" {
			p.emitTurnFailure(chat.ID, turnID, role, reason, attempts, err, endErr)
		}
		if completeErr := p.store.completeTurn(turnID, reason); completeErr != nil {
			slog.Error("chat turn completion persistence failed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "stop_reason", reason, "error", completeErr)
		}
		slog.Info("chat turn completed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "role_name", role.Name, "stop_reason", reason, "latency_ms", nowMillis()-turn.StartedAt, "attempts", attempts)
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

func shouldRetryFreshHermesSession(agent string, fresh bool, reason string, hadEvents, alreadyRetried bool) bool {
	return agent == "hermes" && !fresh && reason == "refusal" && !hadEvents && !alreadyRetried
}

// emitTurnFailure records a failed/aborted turn as a visible "error" message
// block so the chat timeline shows what went wrong instead of going silent.
// relayErr is the routing/start/prompt error (may be nil), agentErr the error
// text the agent plugin reported on turn-ended (may be empty).
func (p *Plugin) emitTurnFailure(chatID, turnID string, role SuperRole, reason string, attempts []map[string]any, relayErr error, agentErr string) {
	text := turnFailureText(reason, attempts, relayErr, agentErr)
	payloadJSON, marshalErr := json.Marshal(map[string]any{"stop_reason": reason, "attempts": attempts})
	if marshalErr != nil {
		payloadJSON = []byte("{}")
	}
	block := &MessageBlock{ID: newID(), EventID: newID(), ChatID: chatID, TurnID: turnID, Kind: "error", Text: text, Payload: string(payloadJSON), OccurredAt: nowMillis()}
	if err := p.store.addMessageBlock(block); err != nil {
		slog.Warn("chat turn failure block persistence failed", "chat_id", chatID, "turn_id", turnID, "error", err)
		return
	}
	payload := block.payload()
	payload["role_id"] = role.ID
	payload["role_name"] = role.Name
	p.publish("chat:"+chatID+":block", payload)
	slog.Info("chat turn failure surfaced", "chat_id", chatID, "turn_id", turnID, "role_id", role.ID, "stop_reason", reason)
}

// turnFailureText renders a one-line summary plus per-attempt detail lines.
func turnFailureText(reason string, attempts []map[string]any, relayErr error, agentErr string) string {
	summary := "Turn failed"
	if reason != "" && reason != "error" {
		summary = "Turn ended: " + reason
	}
	details := []string{}
	for _, attempt := range attempts {
		attemptErr, _ := attempt["error"].(string)
		if attemptErr == "" {
			continue
		}
		target := strings.Join(nonEmpty(
			stringValue(attempt["agent"]), stringValue(attempt["provider"]), stringValue(attempt["model"])), " / ")
		outcome := stringValue(attempt["outcome"])
		if target != "" {
			details = append(details, fmt.Sprintf("%s: %s (%s)", target, attemptErr, outcome))
		} else {
			details = append(details, attemptErr)
		}
	}
	if relayErr != nil {
		details = append(details, relayErr.Error())
	}
	if agentErr != "" && !containsString(details, agentErr) {
		details = append(details, agentErr)
	}
	if len(details) == 0 {
		return summary
	}
	return summary + "\n" + strings.Join(details, "\n")
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle || strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func initialPrompt(workspace Workspace, chat Chat, role SuperRole, history, message string) string {
	sections := nonEmpty(commonPrompt(workspace, chat), initialRolePrompt(role))
	if history != "" {
		sections = append(sections, history+"\n\nCurrent routed message follows:")
	}
	sections = append(sections, message)
	return strings.Join(sections, "\n\n")
}

func commonPrompt(workspace Workspace, chat Chat) string {
	return strings.TrimSpace(strings.Join(nonEmpty(workspace.CommonPrompt, chat.CommonPrompt), "\n\n"))
}

func initialRolePrompt(role SuperRole) string {
	return fmt.Sprintf("You are a persistent Super Workspace role named %q.\n\nRole prompt:\n%s\n\nOperate as this role only. Prefer work that matches the fixed rules, files, topic, and responsibilities above. If a later user message appears unrelated to this role, say so briefly and ask for clarification instead of silently switching tasks.", role.Name, fallback(role.Prompt, "(No role-specific prompt was provided.)"))
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
	return renderRecentHistory(messages, "Recent visible chat history:", routerHistoryByteBudget), nil
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
		if err := p.client.Publish(context.Background(), channel, value); err != nil {
			slog.Warn("chat bus publish failed", "channel", channel, "error", err)
		}
	}
}
