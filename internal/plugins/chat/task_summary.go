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
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// enqueueTaskSummary coalesces new completed turns per Task. A completed
// summary is a base revision, not a terminal state: later turns must roll it
// forward. One worker per Task keeps source_turn_ids exact and serialized.
func (p *Plugin) enqueueTaskSummary(taskID string) {
	if taskID == "" {
		return
	}
	p.mu.Lock()
	p.taskSummaryPending[taskID] = true
	if p.taskSummaryRunning[taskID] {
		p.mu.Unlock()
		return
	}
	p.taskSummaryRunning[taskID] = true
	p.mu.Unlock()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			p.mu.Lock()
			delete(p.taskSummaryPending, taskID)
			p.mu.Unlock()
			p.generateTaskSummary(taskID)
			p.mu.Lock()
			if !p.taskSummaryPending[taskID] {
				delete(p.taskSummaryRunning, taskID)
				p.mu.Unlock()
				return
			}
			p.mu.Unlock()
		}
	}()
}

type taskSummarySource struct {
	at     int64
	turnID string
	text   string
}

func (p *Plugin) generateTaskSummary(taskID string) {
	task, err := p.store.task(taskID)
	if err != nil || task == nil {
		return
	}
	var existing TaskSummary
	lookup := p.store.db.Limit(1).Find(&existing, "task_id = ?", taskID)
	if lookup.Error != nil {
		return
	}
	config := p.summaryConfig(p.ctx)
	row := TaskSummary{TaskID: task.ID, ChatID: task.ChatID, CreatedAt: nowMillis()}
	if lookup.RowsAffected > 0 {
		row = existing
	}
	row.Status = "pending"
	row.Final = task.ended() || lookup.RowsAffected > 0 && existing.Final
	row.Error = ""
	row.UpdatedAt = nowMillis()
	if err := p.upsertTaskSummary(&row); err != nil {
		return
	}
	p.publishTaskSummary(&row)

	source, newIDs, allIDs, revision, err := p.taskSummaryTranscript(task.ID, existing, config.TaskSummaryWordBudget, config.TaskSummaryByteBudget)
	if err != nil || strings.TrimSpace(source) == "" {
		row.Status = "failed"
		if err != nil {
			row.Error = truncateText(err.Error(), 1000)
		} else {
			row.Error = "no completed turn summaries or previous Task summary"
		}
		row.UpdatedAt = nowMillis()
		_ = p.upsertTaskSummary(&row)
		p.publishTaskSummary(&row)
		return
	}
	_ = newIDs // allIDs is the exact previous ∪ newly covered set.
	prompt := fmt.Sprintf(`Summarize the current progress of this Task for another agent.
State the goal, work already done, current state, unresolved questions and suggested next steps.
Use only the supplied material; keep concrete file names, commands and outcomes.

TASK MATERIAL:
%s`, source)
	if row.Final {
		prompt = fmt.Sprintf(`Write the final summary of this ended Task for another agent.
State the overall conclusion, all completed work, decisions made, unresolved questions and suggested next steps.
Use only the supplied material; keep concrete file names, commands and verification outcomes.

TASK MATERIAL:
%s`, source)
	}
	ctx, cancel := context.WithTimeout(p.ctx, time.Duration(config.TimeoutSeconds)*time.Second)
	result, completeErr := p.llmFn(ctx, []map[string]string{{"role": "system", "content": "You write concise factual Task summaries in the same language as the source material."}, {"role": "user", "content": prompt}}, false, config.TimeoutSeconds)
	cancel()

	// Ending a Task while an update is in flight changes final. Dropping this
	// stale result prevents a running preview from masquerading as final.
	var latest TaskSummary
	current := p.store.db.Limit(1).Find(&latest, "task_id = ?", taskID)
	if current.Error != nil || current.RowsAffected == 0 || latest.SummaryRevision != row.SummaryRevision || latest.Final != row.Final {
		p.enqueueTaskSummary(taskID)
		return
	}
	row.UpdatedAt = nowMillis()
	if completeErr != nil {
		// Preserve the previous text, revision and covered ids. Newly uncovered
		// summaries remain eligible for the explicit retry/coalesced next run.
		row.Status = "failed"
		row.Error = truncateText(completeErr.Error(), 1000)
	} else {
		row.Status = "completed"
		row.Text = strings.TrimSpace(result.Content)
		row.SourceTurnIDs = encodeJSON(allIDs)
		row.SourceRevision = revision
		row.Provider = "llm"
		row.Model = result.Model
		row.Error = ""
		row.SummaryRevision++
	}
	if err := p.upsertTaskSummary(&row); err == nil {
		p.publishTaskSummary(&row)
	}
}

// taskSummaryTranscript rolls the previous Task Summary forward using only
// completed Turn Summaries not already in its exact source_turn_ids set. Raw
// Messages are deliberately not re-read and cannot fill summary gaps.
func (p *Plugin) taskSummaryTranscript(taskID string, previous TaskSummary, wordBudget, byteBudget int) (string, []string, []string, int64, error) {
	if wordBudget <= 0 {
		wordBudget = 16000
	}
	if byteBudget <= 0 {
		byteBudget = 96 * 1024
	}
	previousIDs := decodeStrings(previous.SourceTurnIDs)
	covered := make(map[string]bool, len(previousIDs))
	for _, id := range previousIDs {
		covered[id] = true
	}
	var summaries []TurnSummary
	query := p.store.db.Where("task_id = ? AND status = ?", taskID, "completed")
	if len(previousIDs) > 0 {
		query = query.Where("turn_id NOT IN ?", previousIDs)
	}
	if err := query.Order("occurred_at, turn_id").Find(&summaries).Error; err != nil {
		return "", nil, nil, 0, err
	}
	parts := []string{}
	usedWords, usedBytes := 0, 0
	if strings.TrimSpace(previous.Text) != "" {
		base := "[previous Task summary]\n" + strings.TrimSpace(previous.Text)
		base = truncateUTF8Suffix(base, byteBudget)
		parts = append(parts, base)
		usedWords = len(splitWords(base))
		usedBytes = len(base)
	}
	newIDs := []string{}
	allIDs := append([]string{}, previousIDs...)
	revision := previous.SourceRevision
	for _, summary := range summaries {
		entry := fmt.Sprintf("[%s %s turn summary]\n%s", formatSummaryTime(summary.OccurredAt), fallback(summary.RoleName, "agent"), summary.Summary)
		words, bytes := len(splitWords(entry)), len(entry)+2
		if len(parts) > 0 && (usedWords+words > wordBudget || usedBytes+bytes > byteBudget) {
			break
		}
		parts = append(parts, entry)
		usedWords += words
		usedBytes += bytes
		newIDs = append(newIDs, summary.TurnID)
		allIDs = append(allIDs, summary.TurnID)
		if summary.OccurredAt > revision {
			revision = summary.OccurredAt
		}
	}
	if len(parts) == 0 {
		return "", nil, nil, 0, nil
	}
	sort.Strings(allIDs)
	return strings.Join(parts, "\n\n"), newIDs, allIDs, revision, nil
}

func (p *Plugin) upsertTaskSummary(row *TaskSummary) error {
	return p.store.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "task_id"}}, UpdateAll: true}).Create(row).Error
}

func (p *Plugin) publishTaskSummary(row *TaskSummary) {
	p.publish("chat:_:task-summary", map[string]any{"task_id": row.TaskID, "chat_id": row.ChatID,
		"text": row.Text, "source_turn_ids": decodeStrings(row.SourceTurnIDs), "summary_revision": row.SummaryRevision,
		"source_revision": row.SourceRevision, "provider": row.Provider, "model": row.Model,
		"status": row.Status, "final": row.Final, "error": row.Error, "updated_at": row.UpdatedAt})
}

func (p *Plugin) maybeGenerateTaskTitle(task *Task, message *Message) {
	if task == nil || message == nil || task.TitleSource != "default" || task.TitleQueryMessageID != "" || message.Role != "user" {
		return
	}
	normalized := strings.Join(strings.Fields(message.Text), " ")
	runes := []rune(normalized)
	if len(runes) > 20 {
		runes = runes[:20]
	}
	title := string(runes)
	if title == "" {
		return
	}
	now := nowMillis()
	result := p.store.db.Model(&Task{}).Where("id = ? AND title_source = ? AND title_query_message_id = ?", task.ID, "default", "").
		Updates(map[string]any{"title": title, "title_source": "query", "title_revision": gorm.Expr("title_revision + 1"), "title_query_message_id": message.ID, "updated_at": now})
	if result.Error != nil || result.RowsAffected != 1 {
		return
	}
	task.Title, task.TitleSource, task.TitleQueryMessageID, task.TitleRevision, task.UpdatedAt = title, "query", message.ID, task.TitleRevision+1, now
	p.publishTask(task, "title")
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.generateTaskTitle(task.ID, message.ID)
	}()
}

func (p *Plugin) generateTaskTitle(taskID, messageID string) {
	task, err := p.store.task(taskID)
	if err != nil || task == nil || task.TitleSource != "query" || task.TitleQueryMessageID != messageID {
		return
	}
	message, err := p.store.message(messageID)
	if err != nil || message == nil || message.TaskID != taskID {
		return
	}
	config := p.summaryConfig(p.ctx)
	query := truncateWords(message.Text, config.TitleWordBudget)
	ctx, cancel := context.WithTimeout(p.ctx, time.Duration(config.TimeoutSeconds)*time.Second)
	result, err := p.llmFn(ctx, []map[string]string{{"role": "system", "content": "Generate a concise Task title in the same language as the user's message. Return only JSON: {\"title\":\"...\"}. The title must contain at most 40 Unicode characters and no newline."}, {"role": "user", "content": query}}, false, config.TimeoutSeconds)
	cancel()
	if err != nil {
		slog.Warn("chat task title generation failed", "task_id", taskID, "error", err)
		return
	}
	var parsed struct {
		Title string `json:"title"`
	}
	content := strings.TrimSpace(strings.Trim(strings.TrimSpace(result.Content), "`"))
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		slog.Warn("chat task title returned invalid JSON", "task_id", taskID, "error", err)
		return
	}
	title := strings.Trim(strings.TrimSpace(parsed.Title), "\"'“”‘’")
	if title == "" || strings.ContainsAny(title, "\r\n") || utf8.RuneCountInString(title) > 40 {
		slog.Warn("chat task title failed validation", "task_id", taskID, "title", title)
		return
	}
	now := nowMillis()
	update := p.store.db.Model(&Task{}).Where("id = ? AND title_source = ? AND title_query_message_id = ?", taskID, "query", messageID).
		Updates(map[string]any{"title": title, "title_source": "llm", "title_revision": gorm.Expr("title_revision + 1"), "updated_at": now})
	if update.Error == nil && update.RowsAffected == 1 {
		task.Title, task.TitleSource, task.TitleRevision, task.UpdatedAt = title, "llm", task.TitleRevision+1, now
		p.publishTask(task, "title")
	}
}

func truncateWords(value string, budget int) string {
	if budget <= 0 {
		return ""
	}
	words := splitWords(value)
	if len(words) <= budget {
		return value
	}
	return strings.Join(words[:budget], " ")
}

func recoverSummaryError(status string) error {
	if status == "completed" {
		return errors.New("completed summary cannot be recovered")
	}
	return nil
}
