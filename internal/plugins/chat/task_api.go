package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"viewer/sdk/go/busclient"
)

var errSummaryImmutable = errors.New("completed summaries are immutable")

func callerPlugin(frame busclient.Frame) string {
	if frame.Origin != nil && strings.TrimSpace(frame.Origin.Plugin) != "" {
		return frame.Origin.Plugin
	}
	return "unknown"
}

func operationFingerprint(value any) string {
	// Bus reply routing changes on every call and is not part of the
	// idempotent operation. Ignore it before hashing the request payload.
	if object, ok := value.(map[string]any); ok {
		payload := make(map[string]any, len(object))
		for key, item := range object {
			if key != "_reply_to" && key != "_corr" {
				payload[key] = item
			}
		}
		value = payload
	}
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (p *Plugin) existingOperation(caller, operation, key string, request any) (map[string]any, bool, error) {
	var receipt OperationReceipt
	result := p.store.db.Limit(1).Find(&receipt, "caller_plugin = ? AND operation = ? AND idempotency_key = ?", caller, operation, key)
	if result.Error != nil {
		return nil, true, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, false, nil
	}
	if receipt.Fingerprint != operationFingerprint(request) {
		return nil, true, errors.New("idempotency key was already used with a different request")
	}
	if receipt.Failure != "" {
		return nil, true, errors.New(receipt.Failure)
	}
	if receipt.ResultJSON != "" {
		var value map[string]any
		if err := json.Unmarshal([]byte(receipt.ResultJSON), &value); err != nil {
			return nil, true, err
		}
		return value, true, nil
	}
	// A crash after admission but before the receipt update must not execute
	// the operation again. Return the durable identity as an accepted result.
	value := map[string]any{"status": "accepted"}
	if operation == "create" {
		value["task_id"] = receipt.ResultID
	} else {
		value["dispatch_id"] = receipt.ResultID
		value["task_id"] = receipt.TaskID
	}
	return value, true, nil
}

func (p *Plugin) startOperation(caller, operation, key string, request any, chatID, taskID, resultID string) error {
	receipt := &OperationReceipt{CallerPlugin: caller, Operation: operation, IdempotencyKey: key,
		ResultID: resultID, ChatID: chatID, TaskID: taskID, Fingerprint: operationFingerprint(request),
		Status: "dispatching", CreatedAt: nowMillis()}
	return p.store.db.Create(receipt).Error
}

func (p *Plugin) finishOperation(caller, operation, key string, reply map[string]any, status string, operationErr error) error {
	updates := map[string]any{"status": status, "result_json": encodeJSON(reply), "failure": ""}
	if operationErr != nil {
		updates["failure"] = operationErr.Error()
	}
	return p.store.db.Model(&OperationReceipt{}).Where("caller_plugin = ? AND operation = ? AND idempotency_key = ?", caller, operation, key).Updates(updates).Error
}

func (p *Plugin) handleTaskCreate(frame busclient.Frame) {
	request, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	chatID := requestString(request, "chat_id")
	key := strings.TrimSpace(requestString(request, "idempotency_key"))
	if chatID == "" || key == "" {
		p.reply(frame, nil, errors.New("chat_id and idempotency_key are required"))
		return
	}
	chat, err := p.store.chat(chatID)
	if err != nil || chat == nil {
		if err == nil {
			err = errors.New("chat not found")
		}
		p.reply(frame, nil, err)
		return
	}
	caller := callerPlugin(frame)
	p.operationMu.Lock()
	defer p.operationMu.Unlock()
	if existing, found, err := p.existingOperation(caller, "create", key, request); found || err != nil {
		p.reply(frame, existing, err)
		return
	}
	now := nowMillis()
	title, titleSource := "未命名 Task", "default"
	if explicit := strings.TrimSpace(requestString(request, "title")); explicit != "" {
		title, titleSource = explicit, "manual"
	}
	task := &Task{ID: newID(), ChatID: chatID, Title: title, TitleSource: titleSource,
		CreatedBy: caller, CausationID: requestString(request, "causation_id"),
		CorrelationID: requestString(request, "correlation_id"), CreatedAt: now, UpdatedAt: now}
	if titleSource == "manual" {
		task.TitleRevision = 1
	}
	if err := p.store.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(task).Error; err != nil {
			return err
		}
		receipt := &OperationReceipt{CallerPlugin: caller, Operation: "create", IdempotencyKey: key,
			ResultID: task.ID, ResultJSON: encodeJSON(map[string]any{"task": task.payload()}),
			ChatID: chatID, TaskID: task.ID, Fingerprint: operationFingerprint(request), Status: "accepted", CreatedAt: now}
		return tx.Create(receipt).Error
	}); err != nil {
		p.reply(frame, nil, err)
		return
	}
	p.publishTask(task, "created")
	p.reply(frame, map[string]any{"task": task.payload()}, nil)
}

func (p *Plugin) publishTask(task *Task, action string) {
	if task == nil {
		return
	}
	payload := task.payload()
	payload["action"] = action
	p.publish("chat:_:task", payload)
}

func (p *Plugin) touchTask(taskID string, at int64) {
	if at == 0 {
		at = nowMillis()
	}
	_ = p.store.db.Model(&Task{}).Where("id = ?", taskID).Update("updated_at", at).Error
}

func (p *Plugin) runningTaskIDSet() map[string]bool {
	result := map[string]bool{}
	for _, turn := range p.runningTurns("") {
		if taskID, _ := turn["task_id"].(string); taskID != "" {
			result[taskID] = true
		}
	}
	p.mu.Lock()
	for _, block := range p.background {
		result[block.taskID] = true
	}
	p.mu.Unlock()
	return result
}

func (p *Plugin) handleTasksList(frame busclient.Frame) {
	request, _ := frameObject(frame)
	running := p.runningTaskIDSet()
	currentTaskIDs := map[string]bool{}
	if current, ok := request["current_task_ids"].([]any); ok {
		for _, value := range current {
			if id, ok := value.(string); ok && id != "" {
				currentTaskIDs[id] = true
			}
		}
	}
	idList := func(set map[string]bool) []string {
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		return ids
	}
	query := p.store.db.Model(&Task{})
	if chatID := requestString(request, "chat_id"); chatID != "" {
		query = query.Where("chat_id = ?", chatID)
	}
	switch requestString(request, "status") {
	case "open":
		query = query.Where("ended_at IS NULL")
	case "ended":
		query = query.Where("ended_at IS NOT NULL")
	}
	if pinned, ok := request["pinned"].(bool); ok {
		query = query.Where("pinned = ?", pinned)
	}
	if requested, ok := request["running"].(bool); ok {
		ids := []string{}
		for id, active := range running {
			if active == requested {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			query = query.Where("1 = 0")
		} else {
			query = query.Where("id IN ?", ids)
		}
	}
	switch filter := requestString(request, "summary_status"); filter {
	case "":
	case "missing":
		query = query.Where("NOT EXISTS (SELECT 1 FROM task_summaries WHERE task_summaries.task_id = tasks.id)")
	default:
		query = query.Where("id IN (SELECT task_id FROM task_summaries WHERE status = ?)", filter)
	}
	switch filter := requestString(request, "decision_status"); filter {
	case "pending":
		query = query.Where("EXISTS (SELECT 1 FROM decisions WHERE decisions.task_id = tasks.id AND decisions.status IN ?)", []string{"pending", "dispatching"})
	case "none":
		query = query.Where("NOT EXISTS (SELECT 1 FROM decisions WHERE decisions.task_id = tasks.id AND decisions.status IN ?)", []string{"pending", "dispatching"})
	}
	if before := requestInt64(request, "before_updated_at"); before > 0 {
		query = query.Where("updated_at < ? OR (updated_at = ? AND id < ?)", before, before, requestString(request, "before_id"))
	}
	if search := strings.ToLower(strings.TrimSpace(requestString(request, "search"))); search != "" {
		like := "%" + search + "%"
		query = query.Where(`LOWER(title) LIKE ? OR id IN (SELECT task_id FROM task_summaries WHERE LOWER(text) LIKE ?)`, like, like)
	}
	scope := requestString(request, "scope")
	if scope == "sidebar" {
		query = query.Where("ended_at IS NULL")
		ids := idList(currentTaskIDs)
		for id := range running {
			if !currentTaskIDs[id] {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			query = query.Where("pinned = ?", true)
		} else {
			query = query.Where("pinned = ? OR id IN ?", true, ids)
		}
	}
	limit, hasMore := 0, false
	var tasks []Task
	ordered := query.Order("updated_at desc, id desc")
	if scope != "sidebar" {
		limit = int(requestInt64(request, "limit"))
		if limit <= 0 || limit > 100 {
			limit = 50
		}
		ordered = ordered.Limit(limit + 1)
	}
	if err := ordered.Find(&tasks).Error; err != nil {
		p.reply(frame, nil, err)
		return
	}
	if limit > 0 && len(tasks) > limit {
		hasMore = true
		tasks = tasks[:limit]
	}
	chats, err := p.store.chats()
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	chatNames := map[string]string{}
	for _, chat := range chats {
		chatNames[chat.ID] = chat.Name
	}
	values := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		payload := task.payload()
		payload["chat_name"] = chatNames[task.ChatID]
		payload["running"] = running[task.ID]
		var summary TaskSummary
		lookup := p.store.db.Limit(1).Find(&summary, "task_id = ?", task.ID)
		if lookup.Error != nil {
			p.reply(frame, nil, lookup.Error)
			return
		}
		if lookup.RowsAffected > 0 {
			payload["summary_status"] = summary.Status
			payload["summary_final"] = summary.Final
			payload["summary_preview"] = truncateText(summary.Text, 500)
			payload["summary_revision"] = summary.SummaryRevision
		} else {
			payload["summary_status"] = "missing"
			payload["summary_final"] = false
			payload["summary_preview"] = ""
		}
		var pending int64
		if err := p.store.db.Model(&Decision{}).Where("task_id = ? AND status IN ?", task.ID, []string{"pending", "dispatching"}).Count(&pending).Error; err != nil {
			p.reply(frame, nil, err)
			return
		}
		payload["pending_decisions"] = pending
		values = append(values, payload)
	}
	result := map[string]any{"tasks": values, "has_more": hasMore}
	if scope != "sidebar" && len(tasks) > 0 {
		last := tasks[len(tasks)-1]
		result["next_updated_at"] = last.UpdatedAt
		result["next_id"] = last.ID
	}
	p.reply(frame, result, nil)
}

func (p *Plugin) taskForWrite(taskID string) (*Task, error) {
	task, err := p.store.task(taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, errors.New("task not found")
	}
	return task, nil
}

func (p *Plugin) handleTaskPatch(frame busclient.Frame) {
	request, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	task, err := p.taskForWrite(requestString(request, "task_id"))
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	title := strings.TrimSpace(requestString(request, "title"))
	if title == "" {
		p.reply(frame, nil, errors.New("title is required"))
		return
	}
	if expected := requestInt64(request, "title_revision"); expected > 0 && expected != task.TitleRevision {
		p.reply(frame, nil, errors.New("title revision conflict"))
		return
	}
	task.Title = title
	task.TitleSource = "manual"
	task.TitleRevision++
	task.UpdatedAt = nowMillis()
	if err := p.store.saveTask(task); err != nil {
		p.reply(frame, nil, err)
		return
	}
	p.publishTask(task, "updated")
	p.reply(frame, map[string]any{"task": task.payload()}, nil)
}

func (p *Plugin) handleTaskPin(frame busclient.Frame) {
	request, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	task, err := p.taskForWrite(requestString(request, "task_id"))
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	pinned, ok := request["pinned"].(bool)
	if !ok || task.ended() && pinned {
		p.reply(frame, nil, errors.New("an ended Task cannot be pinned"))
		return
	}
	task.Pinned = pinned
	task.UpdatedAt = nowMillis()
	if err := p.store.saveTask(task); err != nil {
		p.reply(frame, nil, err)
		return
	}
	p.publishTask(task, "pin")
	p.reply(frame, map[string]any{"task": task.payload()}, nil)
}

func (p *Plugin) taskBusy(taskID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, runningTaskID := range p.turnTasks {
		if runningTaskID == taskID {
			return true
		}
	}
	for _, queue := range p.queues {
		for _, entry := range queue {
			if entry.taskID == taskID {
				return true
			}
		}
	}
	for _, block := range p.background {
		if block.taskID == taskID {
			return true
		}
	}
	return false
}

// reopenTask continues an ended Task after an explicit human send. The caller
// serializes the transition with handleTaskEnd through operationMu. The old
// summary remains as historical context but is no longer marked final; ending
// the Task again will generate a fresh final summary.
func (p *Plugin) reopenTask(taskID string) (*Task, error) {
	task, err := p.taskForWrite(taskID)
	if err != nil || !task.ended() {
		return task, err
	}
	now := nowMillis()
	task.EndedAt = nil
	task.UpdatedAt = now
	if err := p.store.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(task).Error; err != nil {
			return err
		}
		return tx.Model(&TaskSummary{}).Where("task_id = ?", task.ID).Updates(map[string]any{"final": false, "updated_at": now}).Error
	}); err != nil {
		return nil, err
	}
	p.publishTask(task, "reopened")
	return task, nil
}

func (p *Plugin) handleTaskEnd(frame busclient.Frame) {
	request, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	p.operationMu.Lock()
	defer p.operationMu.Unlock()
	task, err := p.taskForWrite(requestString(request, "task_id"))
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	if task.ended() {
		p.reply(frame, map[string]any{"task": task.payload()}, nil)
		return
	}
	cancelled, err := p.finishTaskEnd(task, nil, nil)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	p.publishTaskEnd(task, nil, cancelled)
	p.reply(frame, map[string]any{"task": task.payload()}, nil)
}

// finishTaskEnd serializes the Task, optional resolving Decision, final
// summary and other pending Decisions. Callers hold operationMu so an ordinary
// Task end and a Decision control option cannot race each other.
func (p *Plugin) finishTaskEnd(task *Task, resolved *Decision, option *decisionOption) ([]Decision, error) {
	if p.taskBusy(task.ID) {
		return nil, errTaskBusy
	}
	now := nowMillis()
	task.EndedAt = &now
	task.Pinned = false
	task.UpdatedAt = now
	var decisions []Decision
	if err := p.store.db.Transaction(func(tx *gorm.DB) error {
		if resolved != nil {
			if option == nil || option.Action != "end_current_task" {
				return errors.New("invalid Task-ending Decision option")
			}
			update := tx.Model(&Decision{}).Where("id = ? AND status = ? AND version = ?", resolved.ID, "pending", resolved.Version).
				Updates(map[string]any{"status": "resolved", "response": option.Label, "version": resolved.Version + 1, "updated_at": now})
			if update.Error != nil || update.RowsAffected != 1 {
				return errors.New("decision version or status conflict")
			}
			resolved.Status = "resolved"
			resolved.Response = option.Label
			resolved.Version++
			resolved.UpdatedAt = now
		}
		if err := tx.Save(task).Error; err != nil {
			return err
		}
		summary := &TaskSummary{TaskID: task.ID, ChatID: task.ChatID, Status: "pending", Final: true, CreatedAt: now, UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "task_id"}}, DoUpdates: clause.Assignments(map[string]any{"status": "pending", "final": true, "error": "", "updated_at": now})}).Create(summary).Error; err != nil {
			return err
		}
		cancel := tx.Model(&Decision{}).Where("task_id = ? AND status = ?", task.ID, "pending")
		if resolved != nil {
			cancel = cancel.Where("id <> ?", resolved.ID)
		}
		if err := cancel.Updates(map[string]any{"status": "cancelled", "updated_at": now}).Error; err != nil {
			return err
		}
		query := tx.Where("task_id = ? AND status = ?", task.ID, "cancelled")
		if resolved != nil {
			query = query.Where("id <> ?", resolved.ID)
		}
		return query.Find(&decisions).Error
	}); err != nil {
		return nil, err
	}
	return decisions, nil
}

func (p *Plugin) publishTaskEnd(task *Task, resolved *Decision, cancelled []Decision) {
	p.publishTask(task, "ended")
	if resolved != nil {
		p.publishDecision(resolved)
	}
	for _, decision := range cancelled {
		p.publish("chat:_:decision", map[string]any{"id": decision.ID, "task_id": task.ID, "status": "cancelled", "updated_at": task.UpdatedAt})
	}
	p.enqueueTaskSummary(task.ID)
}

func (p *Plugin) handleTaskSummaryRetry(frame busclient.Frame) {
	request, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	task, err := p.taskForWrite(requestString(request, "task_id"))
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	var summary TaskSummary
	result := p.store.db.Limit(1).Find(&summary, "task_id = ?", task.ID)
	if result.Error != nil {
		p.reply(frame, nil, result.Error)
		return
	}
	if result.RowsAffected > 0 && summary.Status == "completed" {
		p.reply(frame, nil, errSummaryImmutable)
		return
	}
	p.enqueueTaskSummary(task.ID)
	p.reply(frame, map[string]any{"task_id": task.ID, "status": "pending"}, nil)
}

func (p *Plugin) handleTurnSummaryRetry(frame busclient.Frame) {
	request, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	turnID := requestString(request, "turn_id")
	turn, err := p.store.turn(turnID)
	if err != nil || turn == nil {
		if err == nil {
			err = errors.New("turn not found")
		}
		p.reply(frame, nil, err)
		return
	}
	var summary TurnSummary
	result := p.store.db.Limit(1).Find(&summary, "turn_id = ?", turnID)
	if result.Error != nil {
		p.reply(frame, nil, result.Error)
		return
	}
	if result.RowsAffected > 0 && summary.Status == "completed" {
		p.reply(frame, nil, errSummaryImmutable)
		return
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.generateTurnSummary(turnID, turn.Agent)
	}()
	p.reply(frame, map[string]any{"turn_id": turnID, "status": "pending"}, nil)
}

func sortedTaskIDs(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func operationDebug(receipt OperationReceipt) string {
	return fmt.Sprintf("%s:%s:%s", receipt.CallerPlugin, receipt.Operation, receipt.IdempotencyKey)
}
