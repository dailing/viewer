package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"viewer/internal/agentdriver"
	"viewer/sdk/go/busclient"
)

type dispatchRequest struct {
	IdempotencyKey    string   `json:"idempotency_key"`
	ChatID            string   `json:"-"`
	TaskID            string   `json:"task_id"`
	InputInstanceID   string   `json:"input_instance_id"`
	Message           string   `json:"message"`
	Text              string   `json:"text"`
	RoleIDs           []string `json:"role_ids"`
	BeforeMessageID   string   `json:"before_message_id"`
	HistoryWordBudget *int     `json:"history_word_budget"`
	ForceNewSession   bool     `json:"force_new_session"`
	ParallelDispatch  bool     `json:"parallel_dispatch"`
	ContinueTurnID    string   `json:"continue_turn_id"`
	Raw               bool     `json:"raw"`
	DecisionID        string   `json:"decision_id"`
	SourcePlugin      string   `json:"-"`
	SourceEventID     string   `json:"source_event_id"`
	CausationID       string   `json:"causation_id"`
	CorrelationID     string   `json:"correlation_id"`
	DispatchID        string   `json:"-"`
}

func runtimeKey(chatID, roleID string) string { return chatID + "\x00" + roleID }

// Work is serialized per Task. Different Tasks never share a queue key.
const maxQueuedPerLine = 32

func lineKey(chatID, taskID string) string { return chatID + "\x00task\x00" + taskID }

type queuedMessage struct {
	chatID     string
	targets    []relayTarget
	message    string
	before     int64
	forceNew   bool
	raw        bool
	enqueued   int64
	dispatchID string
	messageID  string
	taskID     string
}

type relayTarget struct {
	role   SuperRole
	resume string
	task   string
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
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if err != nil || request.TaskID == "" || request.Message == "" || request.IdempotencyKey == "" {
		if err == nil {
			err = errBadRequest
		}
		p.reply(frame, nil, err)
		return
	}
	task, err := p.store.task(request.TaskID)
	if err != nil || task == nil {
		if err == nil {
			err = errors.New("task not found")
		}
		p.reply(frame, nil, err)
		return
	}
	chat, err := p.store.chat(task.ChatID)
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
	request.ChatID = chat.ID
	request.SourcePlugin = "unknown"
	if frame.Origin != nil && frame.Origin.Plugin != "" {
		request.SourcePlugin = frame.Origin.Plugin
	}
	request.DispatchID = newID()
	p.operationMu.Lock()
	defer p.operationMu.Unlock()
	if existing, found, err := p.existingOperation(request.SourcePlugin, "dispatch", request.IdempotencyKey, value); found || err != nil {
		p.reply(frame, existing, err)
		return
	}
	if err := p.startOperation(request.SourcePlugin, "dispatch", request.IdempotencyKey, value, chat.ID, task.ID, request.DispatchID); err != nil {
		p.reply(frame, nil, err)
		return
	}
	reply, _, err := p.dispatchMessage(chat, workspace, request, value)
	status := "accepted"
	if err != nil {
		status = "failed"
	}
	if saveErr := p.finishOperation(request.SourcePlugin, "dispatch", request.IdempotencyKey, reply, status, err); saveErr != nil && err == nil {
		err = saveErr
	}
	p.reply(frame, reply, err)
}

// dispatchMessage is the single intake/admission path used by browser and
// programmatic sends. It persists the user Message and then starts or queues
// the ordinary Agent relay; it never manipulates Shell/view state.
func (p *Plugin) dispatchMessage(chat *Chat, workspace Workspace, request dispatchRequest, raw map[string]any) (reply map[string]any, user *Message, err error) {
	reopenEnded, _ := raw["reopen_ended"].(bool)
	task, err := p.resolveDispatchTask(*chat, request.TaskID, reopenEnded)
	if err != nil {
		return nil, nil, err
	}
	claimedDecision, err := p.associateDecision(task.ID, &request)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if finishErr := p.finishOrdinaryDecision(request.DecisionID, request.DispatchID, claimedDecision, err); err == nil {
			err = finishErr
		}
	}()
	resumeSession := ""
	var selected []SuperRole
	var rationale string
	if request.ContinueTurnID != "" {
		var laneRole SuperRole
		resumeSession, laneRole, err = p.resolveContinuation(*chat, workspace, task.ID, request.ContinueTurnID)
		if err != nil {
			return nil, nil, err
		}
		selected = []SuperRole{laneRole}
		rationale = "Continue the session of turn " + request.ContinueTurnID + "."
		request.ForceNewSession = false
		request.ParallelDispatch = false
	} else {
		selected, rationale, err = p.selectRoles(request, raw, *chat, workspace)
		if err != nil {
			return nil, nil, err
		}
	}
	parallel := request.ParallelDispatch
	resumeFor := func(role SuperRole) string {
		if request.ContinueTurnID != "" {
			return resumeSession
		}
		if request.ForceNewSession || parallel {
			return ""
		}
		return p.taskRoleSession(chat.ID, task.ID, role.ID)
	}
	key := lineKey(chat.ID, task.ID)
	if !parallel {
		p.mu.Lock()
		full := p.busy[key] && len(p.queues[key]) >= maxQueuedPerLine
		p.mu.Unlock()
		if full {
			return nil, nil, errQueueFull
		}
	}
	dispatchID := request.DispatchID
	if dispatchID == "" {
		dispatchID = newID()
	}
	request.DispatchID = dispatchID
	user = &Message{ID: newID(), ChatID: chat.ID, TaskID: task.ID, TurnID: dispatchID,
		Role: "user", Text: request.Message, SenderFrom: "user", SourcePlugin: request.SourcePlugin,
		SourceEventID: request.SourceEventID, CausationID: request.CausationID,
		CorrelationID: request.CorrelationID, CreatedAt: nowMillis()}
	if err = p.store.addMessage(user); err != nil {
		return nil, nil, err
	}
	p.touchTask(task.ID, user.CreatedAt)
	if request.DecisionID != "" {
		if err := p.store.db.Model(&Decision{}).Where("id = ? AND task_id = ? AND status = ?", request.DecisionID, task.ID, "dispatching").Update("resolved_dispatch_id", dispatchID).Error; err != nil {
			return nil, nil, err
		}
	}
	p.maybeGenerateTaskTitle(task, user)
	p.retainVisibleMessage(user, "", "")
	p.clearDraft(chat.ID, request.InputInstanceID, task.ID)
	targets := make([]relayTarget, 0, len(selected))
	for _, role := range selected {
		targets = append(targets, relayTarget{role: role, resume: resumeFor(role), task: task.ID})
	}
	if !parallel {
		p.mu.Lock()
		p.lineRaw[key] = request.Raw
		if p.busy[key] {
			p.queues[key] = append(p.queues[key], queuedMessage{chatID: chat.ID, targets: targets,
				message: request.Message, before: user.CreatedAt, forceNew: request.ForceNewSession,
				raw: request.Raw, enqueued: nowMillis(), dispatchID: dispatchID, messageID: user.ID, taskID: task.ID})
			p.mu.Unlock()
			p.publishMessage(user, task.ID)
			p.publishQueue(chat.ID)
			return map[string]any{"role_ids": roleIDs(selected), "started_role_ids": []string{},
				"queued_role_ids": roleIDs(selected), "rationale": rationale, "dispatch_id": dispatchID,
				"message_id": user.ID, "task_id": task.ID, "status": "queued"}, user, nil
		}
		p.busy[key] = true
		p.mu.Unlock()
	}
	startedKeys := []string{}
	if !parallel {
		startedKeys = []string{key}
	}
	p.publishMessage(user, task.ID)
	startedRoleIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		startedRoleIDs = append(startedRoleIDs, target.role.ID)
	}
	reply = map[string]any{"role_ids": roleIDs(selected), "started_role_ids": startedRoleIDs,
		"queued_role_ids": []string{}, "rationale": rationale, "dispatch_id": dispatchID,
		"message_id": user.ID, "task_id": task.ID, "status": "running"}
	if len(targets) == 0 {
		if !parallel {
			p.releaseBusy(startedKeys)
		}
		reply["status"] = "accepted"
		return reply, user, nil
	}
	p.wg.Add(1)
	startGate := make(chan struct{})
	go func() {
		<-startGate
		defer p.wg.Done()
		sessions := p.runRelay(*chat, workspace, targets, request.Message, user.CreatedAt, request.ForceNewSession, parallel, request.Raw, dispatchID)
		if !parallel {
			p.releaseBusyOrHold(startedKeys, sessions)
		}
	}()
	close(startGate)
	return reply, user, nil
}

// resolveTaskOwnership validates that the Task belongs to the Chat. Drafts use
// this read/write-neutral boundary so an ended Task can retain an unsent draft.
func (p *Plugin) resolveTaskOwnership(chat Chat, taskID string) (*Task, error) {
	task, err := p.store.task(taskID)
	if err != nil {
		return nil, err
	}
	if task == nil || task.ChatID != chat.ID {
		return nil, errors.New("task was not found in the chat")
	}
	return task, nil
}

// resolveTask validates ownership and the read-only ended boundary used by
// operations that must not implicitly continue an ended Task.
func (p *Plugin) resolveTask(chat Chat, taskID string) (*Task, error) {
	task, err := p.resolveTaskOwnership(chat, taskID)
	if err != nil {
		return nil, err
	}
	if task.ended() {
		return nil, errTaskEnded
	}
	return task, nil
}

// A human send may explicitly continue an ended Task. Other dispatch callers
// retain the existing open-only behavior by leaving reopen_ended unset.
func (p *Plugin) resolveDispatchTask(chat Chat, taskID string, reopenEnded bool) (*Task, error) {
	task, err := p.resolveTaskOwnership(chat, taskID)
	if err != nil || !task.ended() {
		return task, err
	}
	if !reopenEnded {
		return nil, errTaskEnded
	}
	return p.reopenTask(task.ID)
}

// taskRoleSession returns the role's latest resumable session on the
// task ("" when the role never ran there — the relay then starts a
// fresh session fed with the task's lineage context): the newest turn's
// stamped session, or the in-flight runtime's session while that turn
// runs.
func (p *Plugin) taskRoleSession(chatID, taskID, roleID string) string {
	turn, err := p.store.latestTaskRoleTurn(chatID, taskID, roleID)
	if err != nil || turn == nil {
		return ""
	}
	if turn.SessionID != "" {
		return turn.SessionID
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inflightTurnSessionLocked(turn.ID)
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
	history, err := p.historyPrompt(chat.ID, request.TaskID, before, budget)
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

// resolveContinuation resumes only a Turn owned by the target Task.
func (p *Plugin) resolveContinuation(chat Chat, workspace Workspace, taskID, turnID string) (string, SuperRole, error) {
	turn, err := p.store.turn(turnID)
	if err != nil {
		return "", SuperRole{}, err
	}
	if turn == nil || turn.ChatID != chat.ID || turn.TaskID != taskID {
		return "", SuperRole{}, errors.New("continue_turn_id was not found in the task")
	}
	sessionID := turn.SessionID
	if sessionID == "" {
		p.mu.Lock()
		sessionID = p.inflightTurnSessionLocked(turnID)
		p.mu.Unlock()
	}
	if sessionID == "" {
		return "", SuperRole{}, errors.New("that turn has no resumable session")
	}
	for _, role := range workspace.Roles {
		if role.ID == turn.RoleID {
			return sessionID, role, nil
		}
	}
	return "", SuperRole{}, errors.New("the turn's role no longer exists")
}

// inflightTurnSessionLocked returns the session of a currently running turn
// ("" when the turn is not in flight). Caller holds p.mu.
func (p *Plugin) inflightTurnSessionLocked(turnID string) string {
	for _, current := range p.runtimes {
		if current.activeTurn == turnID {
			return current.sessionID
		}
	}
	return ""
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

// startQueued starts the next dispatch on a freed Task.
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
	p.publishQueue(entry.chatID)
	chat, err := p.store.chat(entry.chatID)
	if err == nil && chat == nil {
		err = errors.New("chat not found")
	}
	var workspace Workspace
	if err == nil {
		workspace, err = p.workspace(p.ctx)
	}
	if err != nil {
		slog.Warn("chat queued batch dropped", "chat_id", entry.chatID, "dispatch_id", entry.dispatchID, "error", err)
		p.releaseBusy([]string{key})
		return
	}
	targets := entry.targets
	slog.Info("chat queued batch starting", "chat_id", entry.chatID, "task", entry.taskID, "roles", len(targets), "queued_ms", nowMillis()-entry.enqueued)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		sessions := p.runRelay(*chat, workspace, targets, entry.message, entry.before, entry.forceNew, false, entry.raw, entry.dispatchID)
		p.releaseBusyOrHold([]string{key}, sessions)
	}()
}

// queuedSnapshotLocked lists one chat's pending queue entries (across all
// role/lane keys) oldest-first — the pane's queued-chip source. position is
// the 1-based slot inside that role key's queue. Caller holds p.mu.
func (p *Plugin) queuedSnapshotLocked(chatID string) []map[string]any {
	keys := make([]string, 0, len(p.queues))
	for key := range p.queues {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := []map[string]any{}
	for _, key := range keys {
		for index, entry := range p.queues[key] {
			if entry.chatID != chatID {
				continue
			}
			roleIDs := make([]string, 0, len(entry.targets))
			roleNames := make([]string, 0, len(entry.targets))
			for _, target := range entry.targets {
				roleIDs = append(roleIDs, target.role.ID)
				roleNames = append(roleNames, target.role.Name)
			}
			firstID := ""
			if len(roleIDs) > 0 {
				firstID = roleIDs[0]
			}
			result = append(result, map[string]any{
				"message_id": entry.messageID, "dispatch_id": entry.dispatchID,
				"chat_id": entry.chatID, "role_id": firstID, "role_name": strings.Join(roleNames, ", "),
				"role_ids": roleIDs, "task_id": entry.taskID,
				"text": entry.message, "enqueued_at": entry.enqueued, "position": index + 1,
			})
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i]["enqueued_at"].(int64) < result[j]["enqueued_at"].(int64)
	})
	return result
}

func (p *Plugin) queuedSnapshot(chatID string) []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.queuedSnapshotLocked(chatID)
}

// publishQueue pushes the chat's full queue snapshot on the queue feed after
// any mutation (enqueue, dequeue, cancel, edit) so every pane reseeds
// wholesale instead of tracking per-entry deltas.
func (p *Plugin) publishQueue(chatID string) {
	p.publish("chat:_:queue", map[string]any{"chat_id": chatID, "queued": p.queuedSnapshot(chatID)})
}

// cancelQueued drops every queue entry of one dispatch in the chat (a
// multi-role dispatch queues one entry per busy role) and returns the count
// plus the dispatch's user message id. Caller holds no lock.
func (p *Plugin) cancelQueued(chatID, dispatchID string) (int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	removed := 0
	messageID := ""
	for key, queue := range p.queues {
		if !strings.HasPrefix(key, chatID+"\x00") {
			continue
		}
		kept := make([]queuedMessage, 0, len(queue))
		for _, entry := range queue {
			if entry.dispatchID == dispatchID {
				removed++
				if messageID == "" {
					messageID = entry.messageID
				}
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == 0 {
			delete(p.queues, key)
		} else {
			p.queues[key] = kept
		}
	}
	return removed, messageID
}

// updateQueued rewrites the message text of every queue entry of one
// dispatch, keeping each entry's position. Returns the count plus the
// dispatch's user message id.
func (p *Plugin) updateQueued(chatID, dispatchID, message string) (int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	updated := 0
	messageID := ""
	for key, queue := range p.queues {
		if !strings.HasPrefix(key, chatID+"\x00") {
			continue
		}
		for index := range queue {
			if queue[index].dispatchID != dispatchID {
				continue
			}
			queue[index].message = message
			updated++
			if messageID == "" {
				messageID = queue[index].messageID
			}
		}
	}
	return updated, messageID
}

func (p *Plugin) handleQueuedCancel(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	dispatchID, _ := value["dispatch_id"].(string)
	if err == nil && (chatID == "" || dispatchID == "") {
		err = errBadRequest
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	removed, messageID := p.cancelQueued(chatID, dispatchID)
	if removed > 0 {
		if err := p.store.db.Model(&OperationReceipt{}).Where("result_id = ? AND operation = ?", dispatchID, "dispatch").Update("status", "cancelled").Error; err != nil {
			p.reply(frame, nil, err)
			return
		}
	}
	if removed == 0 {
		p.reply(frame, nil, errNotQueued)
		return
	}
	// The dispatch's user message row leaves the timeline with its queue
	// entries; the deleted frame lets panes drop the box immediately.
	if messageID != "" {
		if err = p.store.deleteMessage(messageID); err != nil {
			p.reply(frame, nil, err)
			return
		}
		p.publish("chat:"+chatID+":message", map[string]any{"id": messageID, "chat_id": chatID, "deleted": true})
	}
	p.publishQueue(chatID)
	p.reply(frame, map[string]any{"cancelled": true, "removed": removed}, nil)
}

func (p *Plugin) handleQueuedUpdate(frame busclient.Frame) {
	p.operationMu.Lock()
	defer p.operationMu.Unlock()
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	dispatchID, _ := value["dispatch_id"].(string)
	message := strings.TrimSpace(requestString(value, "message"))
	if err == nil && (chatID == "" || dispatchID == "" || message == "") {
		err = errBadRequest
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	if taskID, ok := p.queuedDispatchLine(chatID, dispatchID); ok {
		task, taskErr := p.store.task(taskID)
		if taskErr != nil || task == nil || task.ended() {
			if taskErr == nil {
				taskErr = errTaskEnded
			}
			p.reply(frame, nil, taskErr)
			return
		}
	}
	updated, messageID := p.updateQueued(chatID, dispatchID, message)
	if updated == 0 {
		p.reply(frame, nil, errNotQueued)
		return
	}
	if messageID != "" {
		if err = p.store.updateMessageText(messageID, message); err != nil {
			p.reply(frame, nil, err)
			return
		}
		// Republish the row so open panes retext the query box in place.
		if row, rowErr := p.store.message(messageID); rowErr == nil && row != nil {
			if line, ok := p.queuedDispatchLine(chatID, row.TurnID); ok {
				p.publishMessage(row, line)
			} else {
				p.publishMessage(row)
			}
		}
	}
	p.publishQueue(chatID)
	p.reply(frame, map[string]any{"updated": true}, nil)
}

func (p *Plugin) runRelay(chat Chat, workspace Workspace, targets []relayTarget, message string, before int64, forceNew bool, parallel bool, raw bool, dispatchID string) []relaySession {
	// Resolve @<message id> citations once per dispatch (cite.go): the
	// section precedes the user query in every prompt mode below, while
	// context builders keep the raw message as their recall query.
	cited := p.buildCitedSection(chat.ID, message)
	routedMessage := withCitedSection(cited, message)
	// sessions are probed for live background work before releasing the Task.
	sessions := []relaySession{}
	for _, target := range targets {
		role := target.role
		turnID := newID()
		key := runtimeKey(chat.ID, role.ID)
		switch {
		case parallel:
			// Concurrent send-now turn: unique throwaway runtime key so the
			// canonical chat+role runtime (and its stored session) stays
			// untouched; the entry is removed when the turn ends.
			key += "\x00" + turnID
		case target.task != "":
			// Task turn: the task's session runs under its own
			// task-scoped key, so it never fights the canonical
			// session's busy lock; the runtime stays resident for the
			// task's next continuation.
			key += "\x00task\x00" + target.task
		case target.resume != "":
			// Lane continuation: the lane's session runs under its own key so
			// it never fights the canonical session's busy lock, and the
			// runtime stays resident for the lane's next continuation.
			key += "\x00lane\x00" + target.resume
		}
		turn := &Turn{ID: turnID, ChatID: chat.ID, TaskID: target.task, RoleID: role.ID, RoleName: role.Name, DispatchID: dispatchID, StartedAt: nowMillis()}
		if err := p.store.beginTurn(turn); err != nil {
			slog.Error("chat turn persistence failed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "error", err)
			p.store.db.Model(&OperationReceipt{}).Where("result_id = ? AND operation = ?", dispatchID, "dispatch").Update("failure", err.Error())
			continue
		}
		p.mu.Lock()
		p.turnTasks[turnID] = target.task
		p.mu.Unlock()
		// Global turn lifecycle feed for the Dock status dots: started here,
		// completed below alongside the per-chat turn-completed frame.
		p.publish("chat:_:turn", map[string]any{"chat_id": chat.ID, "turn_id": turnID, "role_id": role.ID, "role_name": role.Name, "phase": "started", "dispatch_id": dispatchID, "task_id": target.task})
		candidates, err := p.resolveCandidates(chat, workspace, role)
		reason, summaryProvider := "error", ""
		endErr := ""
		attempts := []map[string]any{}
		// laneSession records the session this turn ran on, for the
		// end-of-turn lane-queue kick below. lanePluginID is the plugin
		// owning it (the candidate that actually ran the turn).
		laneSession := ""
		lanePluginID := ""
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
				// A task's first turn starts a fresh session of its own:
				// forceNew + ephemeral (the stored canonical session is never
				// read or overwritten), but unlike anonymous parallel the
				// runtime stays resident under the task key for continuations.
				taskFresh := target.task != "" && target.resume == ""
				current, fresh, err = p.ensureBusRuntime(p.ctx, chat, target.task, role, candidate, turnID, key, forceNew || retryFresh || parallel || taskFresh, parallel, target.resume)
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
				// Stamp the session on the turn row and announce it on the turn
				// feed: the pane's session lanes and lane continuations build
				// from these records.
				laneSession = current.sessionID
				lanePluginID = candidate.pluginID
				if laneSession != "" {
					if sessionErr := p.store.setTurnSession(turnID, laneSession); sessionErr != nil {
						slog.Warn("chat turn session persistence failed", "chat_id", chat.ID, "turn_id", turnID, "error", sessionErr)
					}
					p.publish("chat:_:turn", map[string]any{"chat_id": chat.ID, "turn_id": turnID, "role_id": role.ID, "role_name": role.Name, "phase": "session", "dispatch_id": dispatchID, "session_id": laneSession, "task_id": target.task})
				}
				prompt := routedMessage
				contextBytes, promptMode := 0, "existing_session"
				switch {
				case raw:
					// Raw dispatch (the composer's per-line raw toggle): the
					// prompt is the cited section plus the message itself —
					// no common prompt, no role prompt, no line context, no
					// bridge. The session still resumes as usual, like
					// driving the agent from its own CLI through the viewer.
					promptMode = "raw"
				case fresh:
					contextBridge := p.buildTaskContext(chat, target.task, role.ID, message, before, dispatchID)
					contextBytes, promptMode = len(contextBridge), "new_session"
					prompt = initialPrompt(workspace, chat, role, contextBridge, routedMessage)
				default:
					if bridge := p.buildTaskRoleBridge(chat, target.task, role.ID, message, before, dispatchID); bridge != "" {
						contextBytes, promptMode = len(bridge), "role_switch"
						prompt = bridge + "\n\nCurrent routed message follows:\n" + routedMessage
					}
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
		p.touchTask(target.task, nowMillis())
		p.mu.Lock()
		delete(p.turnTasks, turnID)
		p.mu.Unlock()
		if reason != "cancelled" {
			p.detectDecision(turn)
		}
		p.retainTurnMessages(turnID, resolved)
		slog.Info("chat turn completed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "role_name", role.Name, "stop_reason", reason, "latency_ms", nowMillis()-turn.StartedAt, "attempts", attempts)
		p.publish("chat:"+chat.ID+":turn-completed", map[string]any{"chat_id": chat.ID, "task_id": target.task, "turn_id": turnID, "stop_reason": reason, "role_id": role.ID, "role_name": role.Name, "attempts": attempts, "sender": map[string]any{"from": "role", "role_id": role.ID, "role_name": role.Name}})
		p.publish("chat:_:turn", map[string]any{"chat_id": chat.ID, "turn_id": turnID, "role_id": role.ID, "role_name": role.Name, "phase": "completed", "stop_reason": reason, "dispatch_id": dispatchID, "agent": resolved.Agent, "provider": resolved.Provider, "model": resolved.Model, "task_id": target.task})
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
		if laneSession != "" {
			sessions = append(sessions, relaySession{pluginID: lanePluginID, sessionID: laneSession, task: target.task, role: role})
		}
		if err != nil || reason == "error" || reason == "cancelled" {
			break
		}
	}
	return sessions
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
	taskID, _ := p.taskForTurn(turnID)
	block := &MessageBlock{ID: newID(), EventID: newID(), ChatID: chatID, TaskID: taskID, TurnID: turnID, Kind: agentdriver.KindError, Text: text, Payload: string(payloadJSON), OccurredAt: nowMillis()}
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
	return fmt.Sprintf(`You are a persistent Super Workspace role named %q.

Role prompt:
%s

Operate as this role only. Prefer work that matches the fixed rules, files, topic, and responsibilities above. If a later user message appears unrelated to this role, say so briefly and ask for clarification instead of silently switching tasks.

When you genuinely need the user to choose before work can continue, end the visible reply with exactly one flat marker, and no text after its closing tag:
<viewer-decision>
version: 1
kind: choice
question: What should happen next?
option: Recommended next step
option: Alternative next step
display: buttons
tone: neutral
</viewer-decision>
Each non-empty marker line is one key: value entry. Do not use JSON, YAML, XML attributes, nested values, multiline values, or code fences around the marker. Options are optional and may repeat at most eight times. Use display auto, buttons, or list; use tone neutral or warning. Do not use this marker for rhetorical questions or ordinary status updates.`, role.Name, fallback(role.Prompt, "(No role-specific prompt was provided.)"))
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

func (p *Plugin) historyPrompt(chatID, taskID string, before int64, budget int) (string, error) {
	messages, err := p.store.history(taskID, before, budget)
	if err != nil {
		return "", err
	}
	return renderRecentHistory(messages, "Recent visible task history:", routerHistoryByteBudget), nil
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
	if err == nil && !stopped && turnID == "" {
		// No in-flight turn matched: with a background hold the line is
		// busy without a turn, so stop becomes the release escape hatch.
		stopped = p.releaseBackgroundForChat(chatID) > 0
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

// publishMessage pushes one message on the chat's live feed. taskIDs
// attributes the frame to its owning line(s) (main = "") so panes on a
// filtered view merge only their lines' frames; frames without attribution
// stay visible everywhere (anti-flicker fallback).
func (p *Plugin) publishMessage(message *Message, taskIDs ...string) {
	payload := message.payload()
	if len(taskIDs) > 0 {
		payload["task_ids"] = taskIDs
	}
	p.publish("chat:"+message.ChatID+":message", payload)
}

// queuedDispatchLine resolves the line a queued dispatch sits on — the live
// attribution source for in-place edits of its user message.
func (p *Plugin) queuedDispatchLine(chatID, dispatchID string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, queue := range p.queues {
		for _, entry := range queue {
			if entry.chatID == chatID && entry.dispatchID == dispatchID {
				return entry.taskID, true
			}
		}
	}
	return "", false
}

// taskForTurn resolves a turn's owning line for live frame attribution:
// the in-memory map stamped at beginTurn covers the streaming hot path; the
// row lookup covers turns begun before this process started.
func (p *Plugin) taskForTurn(turnID string) (string, bool) {
	p.mu.Lock()
	task, ok := p.turnTasks[turnID]
	p.mu.Unlock()
	if ok {
		return task, true
	}
	turn, err := p.store.turn(turnID)
	if err != nil || turn == nil {
		return "", false
	}
	return turn.TaskID, true
}
func (p *Plugin) publish(channel string, value any) {
	if p.client != nil {
		if err := p.client.Publish(context.Background(), channel, value); err != nil {
			slog.Warn("chat bus publish failed", "channel", channel, "error", err)
		}
	}
}
