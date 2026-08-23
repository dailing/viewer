package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"viewer/internal/agentdriver"
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
	// ParallelDispatch sends immediately without waiting for in-flight
	// turns: every selected role runs concurrently on a fresh, throwaway
	// agent session (the composer's send-now toggle). Implies a fresh
	// session; the canonical per-chat+role session is left untouched.
	ParallelDispatch bool `json:"parallel_dispatch"`
}

func runtimeKey(chatID, roleID string) string { return chatID + "\x00" + roleID }

// maxQueuedPerRole bounds the per-chat+role pending-message queue.
const maxQueuedPerRole = 32

// queuedMessage is a dispatch waiting for a busy chat+role: it starts as its
// own turn once the in-flight relay releases the role's busy key.
type queuedMessage struct {
	chatID   string
	role     SuperRole
	message  string
	before   int64
	forceNew bool
	enqueued int64
	// dispatchID of the dispatch that queued this message, so the turn that
	// eventually runs it links back to the user message's turn_id.
	dispatchID string
}

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
	parallel := request.ParallelDispatch
	if !parallel {
		// Queue-capacity pre-check: an over-full queue fails the dispatch
		// before the user message lands in the timeline.
		p.mu.Lock()
		for _, role := range selected {
			key := runtimeKey(chat.ID, role.ID)
			if p.busy[key] && len(p.queues[key]) >= maxQueuedPerRole {
				err = errQueueFull
				break
			}
		}
		p.mu.Unlock()
		if err != nil {
			p.reply(frame, nil, err)
			return
		}
	}
	dispatchID := newID()
	user := &Message{ID: newID(), ChatID: chat.ID, TurnID: dispatchID, Role: "user", Text: request.Message, SenderFrom: "user", CreatedAt: nowMillis()}
	if err = p.store.addMessage(user); err != nil {
		p.reply(frame, nil, err)
		return
	}
	// Busy roles queue the message (it starts when the in-flight turn ends);
	// free roles start immediately. Parallel dispatch skips the busy lock and
	// runs every role right away on a throwaway session.
	startedKeys := []string{}
	startedRoles := []SuperRole{}
	queuedRoleIDs := []string{}
	p.mu.Lock()
	for _, role := range selected {
		key := runtimeKey(chat.ID, role.ID)
		if !parallel && p.busy[key] {
			p.queues[key] = append(p.queues[key], queuedMessage{chatID: chat.ID, role: role, message: request.Message, before: user.CreatedAt, forceNew: request.ForceNewSession, enqueued: nowMillis(), dispatchID: dispatchID})
			queuedRoleIDs = append(queuedRoleIDs, role.ID)
			continue
		}
		if !parallel {
			p.busy[key] = true
			startedKeys = append(startedKeys, key)
		}
		startedRoles = append(startedRoles, role)
	}
	p.mu.Unlock()
	p.publishMessage(user)
	reply := map[string]any{"role_ids": roleIDs(selected), "started_role_ids": roleIDs(startedRoles), "queued_role_ids": queuedRoleIDs, "rationale": rationale, "dispatch_id": dispatchID}
	if len(startedRoles) == 0 {
		p.reply(frame, reply, nil)
		return
	}
	p.wg.Add(1)
	startGate := make(chan struct{})
	go func() {
		<-startGate
		defer p.wg.Done()
		if !parallel {
			defer p.releaseBusy(startedKeys)
		}
		p.runRelay(*chat, workspace, startedRoles, request.Message, user.CreatedAt, request.ForceNewSession, parallel, dispatchID)
	}()
	p.reply(frame, reply, nil)
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
	ids, rationale, err := routeWithLLM(p.ctx, p.llmFn, request.Message, eligible, history)
	if err != nil {
		slog.Warn("chat dispatch routing failed", "chat_id", chat.ID, "error", err)
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
	// A freed role may have queued messages waiting; start the next one.
	for _, key := range keys {
		p.startQueued(key)
	}
}

// startQueued pops the next queued message for a freed chat+role key, marks
// the key busy again, and relays it as its own single-role turn. Entries
// whose chat disappeared (or whose workspace can no longer load) are dropped
// and the cascade continues with the following entry.
func (p *Plugin) startQueued(key string) {
	p.mu.Lock()
	if p.busy[key] {
		p.mu.Unlock()
		return
	}
	queue := p.queues[key]
	if len(queue) == 0 {
		p.mu.Unlock()
		return
	}
	entry := queue[0]
	if len(queue) == 1 {
		delete(p.queues, key)
	} else {
		p.queues[key] = queue[1:]
	}
	p.busy[key] = true
	p.mu.Unlock()
	chat, err := p.store.chat(entry.chatID)
	if err == nil && chat == nil {
		err = errors.New("chat not found")
	}
	if err == nil {
		var workspace Workspace
		workspace, err = p.workspace(p.ctx)
		if err == nil {
			slog.Info("chat queued message starting", "chat_id", entry.chatID, "role_id", entry.role.ID, "role_name", entry.role.Name, "queued_ms", nowMillis()-entry.enqueued)
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				defer p.releaseBusy([]string{key})
				p.runRelay(*chat, workspace, []SuperRole{entry.role}, entry.message, entry.before, entry.forceNew, false, entry.dispatchID)
			}()
			return
		}
	}
	slog.Warn("chat queued message dropped", "chat_id", entry.chatID, "role_id", entry.role.ID, "error", err)
	p.releaseBusy([]string{key})
}

func (p *Plugin) runRelay(chat Chat, workspace Workspace, roles []SuperRole, message string, before int64, forceNew bool, parallel bool, dispatchID string) {
	for _, role := range roles {
		turnID := newID()
		key := runtimeKey(chat.ID, role.ID)
		if parallel {
			// Concurrent send-now turn: unique throwaway runtime key so the
			// canonical chat+role runtime (and its stored session) stays
			// untouched; the entry is removed when the turn ends.
			key += "\x00" + turnID
		}
		turn := &Turn{ID: turnID, ChatID: chat.ID, RoleID: role.ID, RoleName: role.Name, DispatchID: dispatchID, StartedAt: nowMillis()}
		if err := p.store.beginTurn(turn); err != nil {
			slog.Error("chat turn persistence failed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "error", err)
			continue
		}
		// Global turn lifecycle feed for the Dock status dots: started here,
		// completed below alongside the per-chat turn-completed frame.
		p.publish("chat:_:turn", map[string]any{"chat_id": chat.ID, "turn_id": turnID, "role_id": role.ID, "role_name": role.Name, "phase": "started"})
		candidates, err := p.resolveCandidates(chat, workspace, role)
		reason, summaryProvider := "error", ""
		endErr := ""
		attempts := []map[string]any{}
		// resolvedTarget records the candidate that actually runs the turn:
		// the planned first candidate at resolve time, replaced on failover.
		// Persisted on the turn row and published on the turn feed so the
		// pane's routing labels come from the execution record.
		resolved := agentdriver.Target{}
		if err != nil {
			slog.Warn("chat routing candidate resolution failed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "error", err)
		} else if len(candidates) > 0 {
			resolved = candidates[0].target
			p.recordTurnTarget(chat.ID, turnID, role, resolved, dispatchID)
		}
	candidateLoop:
		for _, candidate := range candidates {
			retryFresh := false
			for {
				var current *runtime
				var fresh bool
				attempt := map[string]any{"agent": candidate.target.Agent, "provider": candidate.target.Provider, "model": candidate.target.Model}
				current, fresh, err = p.ensureBusRuntime(p.ctx, chat, role, candidate, turnID, key, forceNew || retryFresh || parallel, parallel)
				if err != nil {
					attempt["outcome"], attempt["error"] = "start_error", err.Error()
					attempts = append(attempts, attempt)
					slog.Warn("agent start failed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "agent", candidate.target.Agent, "provider", candidate.target.Provider, "model", candidate.target.Model, "error", err)
					continue candidateLoop
				}
				summaryProvider = candidate.target.Agent
				if candidate.target.Agent != resolved.Agent || candidate.target.Provider != resolved.Provider || candidate.target.Model != resolved.Model {
					resolved = candidate.target
					p.recordTurnTarget(chat.ID, turnID, role, resolved, dispatchID)
				}
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
					delete(p.runtimes, key)
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
		p.publish("chat:_:turn", map[string]any{"chat_id": chat.ID, "turn_id": turnID, "role_id": role.ID, "role_name": role.Name, "phase": "completed", "stop_reason": reason, "dispatch_id": dispatchID, "agent": resolved.Agent, "provider": resolved.Provider, "model": resolved.Model})
		if reason != "cancelled" {
			p.wg.Add(1)
			go func(id, provider string) { defer p.wg.Done(); p.generateTurnSummary(id, provider) }(turnID, summaryProvider)
		}
		if parallel {
			// Send-now turns use a throwaway runtime; drop it once the turn
			// has fully ended so the map only holds reusable sessions.
			p.mu.Lock()
			delete(p.runtimes, key)
			p.mu.Unlock()
		}
		if err != nil || reason == "error" || reason == "cancelled" {
			break
		}
	}
}

func shouldRetryFreshHermesSession(agent string, fresh bool, reason string, hadEvents, alreadyRetried bool) bool {
	return agent == "hermes" && !fresh && reason == "refusal" && !hadEvents && !alreadyRetried
}

// recordTurnTarget persists the turn's routing target and announces it on
// the turn lifecycle feed (phase "target"), so panes label the turn with
// the candidate that actually runs it — the planned candidate right after
// resolution, the failover replacement when a later candidate takes over.
func (p *Plugin) recordTurnTarget(chatID, turnID string, role SuperRole, target agentdriver.Target, dispatchID string) {
	if err := p.store.setTurnTarget(turnID, target.Agent, target.Provider, target.Model); err != nil {
		slog.Warn("chat turn target persistence failed", "chat_id", chatID, "turn_id", turnID, "role_id", role.ID, "error", err)
	}
	p.publish("chat:_:turn", map[string]any{"chat_id": chatID, "turn_id": turnID, "role_id": role.ID, "role_name": role.Name, "phase": "target", "dispatch_id": dispatchID, "agent": target.Agent, "provider": target.Provider, "model": target.Model})
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
	turnID, _ := value["turn_id"].(string)
	if err == nil && chatID == "" {
		err = errBadRequest
	}
	stopped := false
	if err == nil {
		stopped, err = p.stopTurn(chatID, roleID, turnID)
	}
	p.reply(frame, map[string]any{"stopped": stopped}, err)
}

// stopTurn cancels in-flight turns of a chat. roleID narrows to one role;
// turnID narrows further to one specific turn — required now that parallel
// send-now turns of the same role can run side by side and the pane stops
// them individually.
func (p *Plugin) stopTurn(chatID, roleID, turnID string) (bool, error) {
	p.mu.Lock()
	targets := []*runtime{}
	for key, current := range p.runtimes {
		if strings.HasPrefix(key, chatID+"\x00") && current.activeTurn != "" && (roleID == "" || current.roleID == roleID) && (turnID == "" || current.activeTurn == turnID) {
			current.cancelRequested = true
			targets = append(targets, current)
		}
	}
	p.mu.Unlock()
	var result error
	for _, current := range targets {
		if p.client == nil {
			break // unit-test plugin without a bus: the cancel flag alone ends the relay turn
		}
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
