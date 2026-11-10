package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"viewer/sdk/go/busclient"
)

func hasDecisionMarkerCandidate(text string) bool {
	lines := decisionSourceLines(text)
	var fenceMarker byte
	fenceSize := 0
	for index := range lines {
		line := lines[index].text
		if fenceMarker != 0 {
			marker, size, rest, ok := markdownFence(line)
			if ok && marker == fenceMarker && size >= fenceSize && strings.TrimSpace(rest) == "" {
				fenceMarker, fenceSize = 0, 0
			}
			continue
		}
		if marker, size, _, ok := markdownFence(line); ok {
			fenceMarker, fenceSize = marker, size
			continue
		}
		if strings.Contains(line, decisionOpenTag) || strings.Contains(line, decisionCloseTag) {
			return true
		}
	}
	return false
}

func (p *Plugin) detectDecision(turn *Turn) {
	if turn == nil || turn.TaskID == "" {
		return
	}
	messages, err := p.store.turnMessages(turn.ID)
	if err != nil {
		return
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "assistant" {
			continue
		}
		marker := parseDecisionMarker(message.Text)
		status, warning := "pending", ""
		if marker == nil {
			if !hasDecisionMarkerCandidate(message.Text) {
				return
			}
			marker = &decisionMarker{Kind: "choice", OptionsJSON: "[]", Display: "auto", Tone: "neutral"}
			status, warning = "invalid", "invalid <viewer-decision> marker; left unchanged in the assistant message"
		} else {
			warning = marker.Warning
		}
		now := nowMillis()
		decision := &Decision{ID: newID(), ChatID: turn.ChatID, TaskID: turn.TaskID, SourceTurnID: turn.ID,
			SourceMessageID: message.ID, RoleID: turn.RoleID, Version: 1, Kind: marker.Kind, Question: marker.Question,
			OptionsJSON: marker.OptionsJSON, Display: marker.Display, Tone: marker.Tone, ParseWarning: warning,
			Status: status, MarkerStart: marker.Start, MarkerEnd: marker.End, CreatedAt: now, UpdatedAt: now}
		if task, taskErr := p.store.task(turn.TaskID); taskErr == nil && task != nil && task.ended() && decision.Status == "pending" {
			decision.Status = "cancelled"
		}
		if err := p.store.db.Create(decision).Error; err != nil {
			return // unique source_turn_id makes detection idempotent
		}
		block := &MessageBlock{ID: newID(), EventID: decision.ID, ChatID: decision.ChatID, TaskID: decision.TaskID,
			TurnID: decision.SourceTurnID, Kind: "decision", Text: decision.Question,
			Payload: encodeJSON(map[string]any{"decision_id": decision.ID}), OccurredAt: now}
		_ = p.store.db.Create(block).Error
		p.publishDecision(decision)
		return
	}
}

func (p *Plugin) publishDecision(decision *Decision) {
	p.publish("chat:_:decision", decisionPayload(decision))
}

func decisionPayload(decision *Decision) map[string]any {
	if decision == nil {
		return map[string]any{}
	}
	options := []map[string]any{}
	_ = json.Unmarshal([]byte(decision.OptionsJSON), &options)
	return map[string]any{"id": decision.ID, "chat_id": decision.ChatID, "task_id": decision.TaskID,
		"source_turn_id": decision.SourceTurnID, "source_message_id": decision.SourceMessageID, "role_id": decision.RoleID,
		"version": decision.Version, "kind": decision.Kind, "question": decision.Question, "options": options,
		"display": decision.Display, "tone": decision.Tone, "parse_warning": decision.ParseWarning,
		"status": decision.Status, "response": decision.Response, "resolved_dispatch_id": decision.ResolvedDispatchID,
		"marker_start": decision.MarkerStart, "marker_end": decision.MarkerEnd, "created_at": decision.CreatedAt, "updated_at": decision.UpdatedAt}
}

type decisionOption struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Action string `json:"action,omitempty"`
}

func parseDecisionOptions(encoded string) ([]decisionOption, error) {
	var options []decisionOption
	if err := json.Unmarshal([]byte(encoded), &options); err != nil {
		return nil, err
	}
	return options, nil
}

func (p *Plugin) decision(id string) (*Decision, error) {
	var decision Decision
	result := p.store.db.Limit(1).Find(&decision, "id = ?", id)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, errors.New("decision not found")
	}
	return &decision, nil
}

func (p *Plugin) handleDecisionsList(frame busclient.Frame) {
	request, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	query := p.store.db.Model(&Decision{})
	if taskID := requestString(request, "task_id"); taskID != "" {
		query = query.Where("task_id = ?", taskID)
	}
	if chatID := requestString(request, "chat_id"); chatID != "" {
		query = query.Where("chat_id = ?", chatID)
	}
	if status := requestString(request, "status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if pending, ok := request["pending"].(bool); ok && pending {
		query = query.Where("status IN ?", []string{"pending", "dispatching"})
	}
	if history, ok := request["history"].(bool); ok && history {
		query = query.Where("status IN ?", []string{"resolved", "cancelled", "invalid"})
	}
	if before := requestInt64(request, "before_created_at"); before > 0 {
		if beforeID := requestString(request, "before_id"); beforeID != "" {
			query = query.Where("(created_at < ? OR (created_at = ? AND id < ?))", before, before, beforeID)
		} else {
			query = query.Where("created_at < ?", before)
		}
	}
	limit := int(requestInt64(request, "limit"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	var decisions []Decision
	if err := query.Order("created_at desc, id desc").Limit(limit + 1).Find(&decisions).Error; err != nil {
		p.reply(frame, nil, err)
		return
	}
	hasMore := len(decisions) > limit
	if hasMore {
		decisions = decisions[:limit]
	}
	chats, err := p.store.chats()
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	chatNames := make(map[string]string, len(chats))
	for _, chat := range chats {
		chatNames[chat.ID] = chat.Name
	}
	workspace, err := p.workspace(p.ctx)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	roleNames := make(map[string]string, len(workspace.Roles))
	for _, role := range workspace.Roles {
		roleNames[role.ID] = role.Name
	}
	taskTitles := map[string]string{}
	values := make([]map[string]any, 0, len(decisions))
	for index := range decisions {
		decision := &decisions[index]
		value := decisionPayload(decision)
		if _, ok := taskTitles[decision.TaskID]; !ok {
			if task, taskErr := p.store.task(decision.TaskID); taskErr == nil && task != nil {
				taskTitles[decision.TaskID] = task.Title
			}
		}
		value["chat_name"] = chatNames[decision.ChatID]
		value["task_title"] = taskTitles[decision.TaskID]
		value["role_name"] = roleNames[decision.RoleID]
		values = append(values, value)
	}
	result := map[string]any{"decisions": values, "has_more": hasMore}
	if include, _ := request["include_pending_count"].(bool); include {
		var pendingCount int64
		if err := p.store.db.Model(&Decision{}).Where("status IN ?", []string{"pending", "dispatching"}).Count(&pendingCount).Error; err != nil {
			p.reply(frame, nil, err)
			return
		}
		result["pending_count"] = pendingCount
	}
	if hasMore && len(decisions) > 0 {
		last := decisions[len(decisions)-1]
		result["next_created_at"] = last.CreatedAt
		result["next_id"] = last.ID
	}
	p.reply(frame, result, nil)
}

// associateDecision joins an ordinary user reply to the same continuation as
// the dedicated Decision response. With no explicit id, only a unique pending
// Decision is associated; multiple pending Decisions are never guessed.
// The bool reports a new pending→dispatching claim owned by this dispatch.
func (p *Plugin) associateDecision(taskID string, request *dispatchRequest) (bool, error) {
	if request.DecisionID == "" {
		var pending []Decision
		if err := p.store.db.Where("task_id = ? AND status = ?", taskID, "pending").Order("created_at, id").Limit(2).Find(&pending).Error; err != nil {
			return false, err
		}
		if len(pending) != 1 {
			return false, nil
		}
		request.DecisionID = pending[0].ID
	}
	decision, err := p.decision(request.DecisionID)
	if err != nil || decision.TaskID != taskID {
		if err == nil {
			err = errors.New("decision does not belong to the task")
		}
		return false, err
	}
	if decision.Status == "dispatching" {
		// Only the dedicated response path may enter an already claimed
		// Decision. Public duplicate sends must not attach another message.
		if request.SourcePlugin != "chat" || request.ContinueTurnID != decision.SourceTurnID {
			return false, errors.New("decision was already responded to")
		}
		return false, nil
	}
	if decision.Status != "pending" {
		return false, errors.New("decision version or status conflict")
	}
	claimed := p.store.db.Model(&Decision{}).Where("id = ? AND status = ? AND version = ?", decision.ID, "pending", decision.Version).
		Updates(map[string]any{"status": "dispatching", "response": request.Message, "version": decision.Version + 1, "updated_at": nowMillis()})
	if claimed.Error != nil {
		return false, claimed.Error
	}
	if claimed.RowsAffected != 1 {
		return false, errors.New("decision was already responded to")
	}
	request.ContinueTurnID = decision.SourceTurnID
	request.RoleIDs = []string{decision.RoleID}
	request.ForceNewSession = false
	request.ParallelDispatch = false
	if request.CausationID == "" {
		request.CausationID = decision.ID
	}
	if request.CorrelationID == "" {
		request.CorrelationID = decision.ID
	}
	return true, nil
}

func (p *Plugin) finishOrdinaryDecision(decisionID string, dispatchID string, claimed bool, operationErr error) error {
	if !claimed {
		return nil
	}
	decision, err := p.decision(decisionID)
	if err != nil {
		return err
	}
	if operationErr != nil {
		update := p.store.db.Model(&Decision{}).Where("id = ? AND status = ?", decision.ID, "dispatching").
			Updates(map[string]any{"status": "pending", "version": decision.Version + 1, "updated_at": nowMillis()})
		if refreshed, refreshErr := p.decision(decisionID); refreshErr == nil {
			p.publishDecision(refreshed)
		}
		return update.Error
	}
	decision.Status = "resolved"
	decision.ResolvedDispatchID = dispatchID
	decision.Version++
	decision.UpdatedAt = nowMillis()
	if err := p.store.db.Save(decision).Error; err != nil {
		return err
	}
	p.publishDecision(decision)
	return nil
}

func (p *Plugin) handleDecisionRespond(frame busclient.Frame) {
	if callerPlugin(frame) != "gateway" {
		p.reply(frame, nil, errors.New("decision responses require the human gateway channel"))
		return
	}
	request, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	decisionID := requestString(request, "decision_id")
	response := strings.TrimSpace(requestString(request, "response"))
	if decisionID == "" || response == "" {
		p.reply(frame, nil, errors.New("decision_id and response are required"))
		return
	}
	p.operationMu.Lock()
	defer p.operationMu.Unlock()
	if existing, found, err := p.existingOperation("chat", "decision", decisionID, request); found || err != nil {
		p.reply(frame, existing, err)
		return
	}
	decision, err := p.decision(decisionID)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	expected := requestInt64(request, "expected_version")
	if decision.Status != "pending" || expected > 0 && int(expected) != decision.Version {
		p.reply(frame, nil, errors.New("decision version or status conflict"))
		return
	}
	task, err := p.store.task(decision.TaskID)
	if err != nil || task == nil || task.ended() {
		if err == nil {
			err = errTaskEnded
		}
		p.reply(frame, nil, err)
		return
	}
	if optionID := requestString(request, "option_id"); optionID != "" {
		options, optionErr := parseDecisionOptions(decision.OptionsJSON)
		if optionErr != nil {
			p.reply(frame, nil, optionErr)
			return
		}
		var selected *decisionOption
		for index := range options {
			if options[index].ID == optionID {
				selected = &options[index]
				break
			}
		}
		if selected == nil {
			p.reply(frame, nil, errors.New("decision option not found"))
			return
		}
		if selected.Action == "end_current_task" {
			p.respondEndTask(frame, request, decision, task, selected)
			return
		}
	}
	chat, err := p.store.chat(decision.ChatID)
	if err != nil || chat == nil {
		p.reply(frame, nil, errors.New("chat not found"))
		return
	}
	workspace, err := p.workspace(p.ctx)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	now := nowMillis()
	update := p.store.db.Model(&Decision{}).Where("id = ? AND status = ? AND version = ?", decision.ID, "pending", decision.Version).
		Updates(map[string]any{"status": "dispatching", "response": response, "version": decision.Version + 1, "updated_at": now})
	if update.Error != nil || update.RowsAffected != 1 {
		p.reply(frame, nil, errors.New("decision was already responded to"))
		return
	}
	dispatchID := newID()
	dispatch := dispatchRequest{IdempotencyKey: "decision:" + decision.ID, ChatID: chat.ID, TaskID: task.ID, Message: response,
		DecisionID: decision.ID, DispatchID: dispatchID, SourcePlugin: "chat", SourceEventID: decision.SourceMessageID,
		RoleIDs: []string{decision.RoleID}, ContinueTurnID: decision.SourceTurnID,
		CausationID: decision.ID, CorrelationID: decision.ID}
	if err := p.startOperation("chat", "decision", decision.ID, request, chat.ID, task.ID, dispatchID); err != nil {
		p.reply(frame, nil, err)
		return
	}
	reply, _, dispatchErr := p.dispatchMessage(chat, workspace, dispatch, request)
	if dispatchErr != nil {
		_ = p.store.db.Model(&Decision{}).Where("id = ?", decision.ID).Updates(map[string]any{"status": "pending", "version": decision.Version + 2, "updated_at": nowMillis()}).Error
		_ = p.store.db.Where("caller_plugin = ? AND operation = ? AND idempotency_key = ?", "chat", "decision", decision.ID).Delete(&OperationReceipt{}).Error
		p.reply(frame, nil, dispatchErr)
		return
	}
	decision, _ = p.decision(decision.ID)
	decision.Status = "resolved"
	decision.ResolvedDispatchID = dispatchID
	decision.Version++
	decision.UpdatedAt = nowMillis()
	if err := p.store.db.Save(decision).Error; err != nil {
		p.reply(frame, nil, err)
		return
	}
	reply["decision"] = decisionPayload(decision)
	_ = p.finishOperation("chat", "decision", decision.ID, reply, "resolved", nil)
	p.publishDecision(decision)
	p.reply(frame, reply, nil)
}

// respondEndTask handles the single built-in Decision control action. The
// caller holds operationMu and has already checked idempotency, version and
// the stored option identity; no option text is dispatched to the Agent.
func (p *Plugin) respondEndTask(frame busclient.Frame, request map[string]any, decision *Decision, task *Task, option *decisionOption) {
	if err := p.startOperation("chat", "decision", decision.ID, request, task.ChatID, task.ID, task.ID); err != nil {
		p.reply(frame, nil, err)
		return
	}
	cancelled, err := p.finishTaskEnd(task, decision, option)
	if err != nil {
		_ = p.store.db.Where("caller_plugin = ? AND operation = ? AND idempotency_key = ?", "chat", "decision", decision.ID).Delete(&OperationReceipt{}).Error
		p.reply(frame, nil, err)
		return
	}
	reply := map[string]any{"task": task.payload(), "decision": decisionPayload(decision)}
	_ = p.finishOperation("chat", "decision", decision.ID, reply, "resolved", nil)
	p.publishTaskEnd(task, decision, cancelled)
	p.reply(frame, reply, nil)
}

func (p *Plugin) handleDecisionCancel(frame busclient.Frame) {
	if callerPlugin(frame) != "gateway" {
		p.reply(frame, nil, errors.New("decision cancellation requires the human gateway channel"))
		return
	}
	request, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	decision, err := p.decision(requestString(request, "decision_id"))
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	expected := requestInt64(request, "expected_version")
	if decision.Status != "pending" || expected > 0 && int(expected) != decision.Version {
		p.reply(frame, nil, errors.New("decision version or status conflict"))
		return
	}
	decision.Status = "cancelled"
	decision.Version++
	decision.UpdatedAt = nowMillis()
	if err := p.store.db.Save(decision).Error; err != nil {
		p.reply(frame, nil, err)
		return
	}
	p.publishDecision(decision)
	p.reply(frame, map[string]any{"decision": decisionPayload(decision)}, nil)
}

func (p *Plugin) handleDecisionOpen(frame busclient.Frame) {
	request, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	decision, err := p.decision(requestString(request, "decision_id"))
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	message, err := p.store.message(decision.SourceMessageID)
	if err != nil || message == nil {
		p.reply(frame, nil, errors.New("decision source message not found"))
		return
	}
	p.reply(frame, map[string]any{"task_id": decision.TaskID, "source_turn_id": decision.SourceTurnID,
		"source_message_id": decision.SourceMessageID, "created_at": message.CreatedAt,
		"marker_start": decision.MarkerStart, "marker_end": decision.MarkerEnd}, nil)
}

func (p *Plugin) handleTaskExport(frame busclient.Frame) {
	request, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	task, err := p.store.task(requestString(request, "task_id"))
	if err != nil || task == nil {
		p.reply(frame, nil, errors.New("task not found"))
		return
	}
	var messages []Message
	if err := p.store.db.Where("task_id = ?", task.ID).Order("created_at, id").Find(&messages).Error; err != nil {
		p.reply(frame, nil, err)
		return
	}
	var decisions []Decision
	if err := p.store.db.Where("task_id = ?", task.ID).Order("created_at, id").Find(&decisions).Error; err != nil {
		p.reply(frame, nil, err)
		return
	}
	var summary TaskSummary
	_ = p.store.db.Limit(1).Find(&summary, "task_id = ?", task.ID).Error
	format := fallback(requestString(request, "format"), "markdown")
	if format == "json" {
		result := map[string]any{"task": task.payload(), "summary": summary, "messages": messages, "decisions": decisions}
		if include, _ := request["include_events"].(bool); include {
			var events []TurnEvent
			if err := p.store.db.Where("task_id = ?", task.ID).Order("occurred_at, id").Find(&events).Error; err == nil {
				result["events"] = events
			}
		}
		p.reply(frame, result, nil)
		return
	}
	lines := []string{"# " + task.Title, "", fmt.Sprintf("Status: %s", task.payload()["status"])}
	if summary.Text != "" {
		lines = append(lines, "", "## Summary", "", summary.Text)
	}
	lines = append(lines, "", "## Messages")
	for _, message := range messages {
		label := "User"
		if message.Role == "assistant" {
			label = fallback(message.RoleName, "Agent")
		}
		lines = append(lines, "", "### "+label, "", message.Text)
	}
	if len(decisions) > 0 {
		lines = append(lines, "", "## Decisions")
		for _, decision := range decisions {
			lines = append(lines, "", fmt.Sprintf("- %s — %s; response: %s", decision.Question, decision.Status, decision.Response))
		}
	}
	if include, _ := request["include_events"].(bool); include {
		var events []TurnEvent
		if err := p.store.db.Where("task_id = ?", task.ID).Order("occurred_at, id").Find(&events).Error; err == nil {
			lines = append(lines, "", "## Raw events", "", "```json")
			for _, event := range events {
				lines = append(lines, event.RawJSON)
			}
			lines = append(lines, "```")
		}
	}
	p.reply(frame, map[string]any{"task_id": task.ID, "format": "markdown", "content": strings.Join(lines, "\n")}, nil)
}

func transitionDecision(tx *gorm.DB, id, from, to string, version int) error {
	return tx.Model(&Decision{}).Where("id = ? AND status = ? AND version = ?", id, from, version).Updates(map[string]any{"status": to, "version": version + 1}).Error
}
