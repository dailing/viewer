package chat

import (
	"errors"

	"viewer/sdk/go/busclient"
)

// Read-only Task APIs share the same Task identity as dispatch and history;
// none of them implicitly creates work or regenerates a summary.
func (p *Plugin) handleTaskGet(frame busclient.Frame) {
	request, err := frameObject(frame)
	task, lookupErr := p.taskForWrite(requestString(request, "task_id"))
	if err != nil || lookupErr != nil {
		p.reply(frame, nil, errors.Join(err, lookupErr))
		return
	}
	p.reply(frame, task.payload(), nil)
}

func (p *Plugin) handleTaskSummary(frame busclient.Frame) {
	request, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	taskID := requestString(request, "task_id")
	if _, err := p.taskForWrite(taskID); err != nil {
		p.reply(frame, nil, err)
		return
	}
	var summary TaskSummary
	result := p.store.db.Limit(1).Find(&summary, "task_id = ?", taskID)
	if result.Error != nil {
		p.reply(frame, nil, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		p.reply(frame, map[string]any{"task_id": taskID, "text": "", "source_turn_ids": []string{}, "summary_revision": 0, "source_revision": 0, "provider": "", "model": "", "status": "missing", "final": false, "error": "", "updated_at": 0}, nil)
		return
	}
	p.reply(frame, map[string]any{"task_id": summary.TaskID, "chat_id": summary.ChatID,
		"text": summary.Text, "source_turn_ids": decodeStrings(summary.SourceTurnIDs), "summary_revision": summary.SummaryRevision,
		"source_revision": summary.SourceRevision, "provider": summary.Provider, "model": summary.Model,
		"status": summary.Status, "final": summary.Final, "error": summary.Error, "updated_at": summary.UpdatedAt}, nil)
}

func (p *Plugin) handleTurnsList(frame busclient.Frame) {
	request, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	taskID := requestString(request, "task_id")
	if _, err := p.taskForWrite(taskID); err != nil {
		p.reply(frame, nil, err)
		return
	}
	limit := int(requestInt64(request, "limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := p.store.db.Where("task_id = ?", taskID)
	if before := requestInt64(request, "before"); before > 0 {
		query = query.Where("started_at < ?", before)
	}
	var turns []Turn
	if err := query.Order("started_at desc, id desc").Limit(limit + 1).Find(&turns).Error; err != nil {
		p.reply(frame, nil, err)
		return
	}
	hasMore := len(turns) > limit
	if hasMore {
		turns = turns[:limit]
	}
	values := make([]map[string]any, 0, len(turns))
	for _, turn := range turns {
		values = append(values, map[string]any{"turn_id": turn.ID, "chat_id": turn.ChatID, "task_id": turn.TaskID,
			"role_id": turn.RoleID, "role_name": turn.RoleName, "dispatch_id": turn.DispatchID,
			"session_id": turn.SessionID, "agent": turn.Agent, "provider": turn.Provider, "model": turn.Model,
			"started_at": turn.StartedAt, "ended_at": turn.EndedAt, "stop_reason": turn.StopReason})
	}
	p.reply(frame, map[string]any{"turns": values, "has_more": hasMore}, nil)
}

func (p *Plugin) handleRoleSessionInvalidate(frame busclient.Frame) {
	request, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	taskID, roleID := requestString(request, "task_id"), requestString(request, "role_id")
	task, err := p.taskForWrite(taskID)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	if roleID == "" {
		p.reply(frame, nil, errBadRequest)
		return
	}
	result := p.store.db.Where("chat_id = ? AND task_id = ? AND role_id = ?", task.ChatID, taskID, roleID).Delete(&RoleSession{})
	if result.Error != nil {
		p.reply(frame, nil, result.Error)
		return
	}
	p.publishTask(task, "session-invalidated")
	p.reply(frame, map[string]any{"task_id": taskID, "role_id": roleID, "invalidated": result.RowsAffected > 0}, nil)
}
