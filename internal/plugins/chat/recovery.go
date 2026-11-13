package chat

import (
	"encoding/json"
	"errors"
	"fmt"
)

// recoverTaskWork restores only resumable bookkeeping. In-flight agent Turns
// cannot be resumed after a process restart and become failed history.
func (p *Plugin) recoverTaskWork() error {
	now := nowMillis()
	if err := p.store.db.Model(&Turn{}).Where("ended_at IS NULL").Updates(map[string]any{
		"ended_at": now, "stop_reason": "error",
	}).Error; err != nil {
		return fmt.Errorf("recover interrupted turns: %w", err)
	}
	if err := p.store.db.Model(&TurnSummary{}).Where("status = ?", "pending").Updates(map[string]any{
		"status": "failed", "error": "interrupted by chat restart",
	}).Error; err != nil {
		return fmt.Errorf("recover interrupted turn summaries: %w", err)
	}
	if err := p.store.db.Model(&Decision{}).Where("status = ?", "dispatching").Updates(map[string]any{
		"status": "pending", "updated_at": now,
	}).Error; err != nil {
		return fmt.Errorf("recover dispatching decisions: %w", err)
	}
	var routing []OperationReceipt
	if err := p.store.db.Where("operation = ? AND status = ?", "dispatch", "routing").Find(&routing).Error; err != nil {
		return fmt.Errorf("list interrupted Auto Route dispatches: %w", err)
	}
	for _, receipt := range routing {
		reply := map[string]any{"dispatch_id": receipt.ResultID, "task_id": receipt.TaskID}
		if receipt.ResultJSON != "" {
			_ = decodeInto(json.RawMessage(receipt.ResultJSON), &reply)
		}
		interrupted := errors.New("interrupted by chat restart")
		reply["status"] = "failed"
		reply["error"] = interrupted.Error()
		if err := p.finishOperation(receipt.CallerPlugin, receipt.Operation, receipt.IdempotencyKey, reply, "failed", interrupted); err != nil {
			return fmt.Errorf("recover interrupted Auto Route dispatch %s: %w", receipt.ResultID, err)
		}
		p.emitDispatchFailure(receipt.ChatID, receipt.TaskID, receipt.ResultID, interrupted)
	}
	var taskIDs []string
	if err := p.store.db.Model(&TaskSummary{}).Where("status IN ?", []string{"pending", "failed"}).Pluck("task_id", &taskIDs).Error; err != nil {
		return fmt.Errorf("list recoverable Task summaries: %w", err)
	}
	for _, taskID := range taskIDs {
		p.enqueueTaskSummary(taskID)
	}
	var tasks []Task
	if err := p.store.db.Where("title_source = ? AND title_query_message_id <> ''", "default").Find(&tasks).Error; err != nil {
		return fmt.Errorf("list interrupted Task titles: %w", err)
	}
	for _, task := range tasks {
		p.wg.Add(1)
		go func(taskID, messageID string) {
			defer p.wg.Done()
			p.generateTaskTitle(taskID, messageID)
		}(task.ID, task.TitleQueryMessageID)
	}
	return nil
}
