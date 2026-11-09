package chat

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// contextCoverage records exact source ids for every fresh/bridge build.
// TaskSummaries are ended-Task final summaries; Summarized and Raw are Turn
// ids from the active Task. Omissions are explicit rather than silently
// backfilled with unbounded raw history.
type contextCoverage struct {
	TaskSummaries        []string `json:"task_summaries"`
	Summarized           []string `json:"summarized"`
	Raw                  []string `json:"raw"`
	RawMessages          []string `json:"raw_messages"`
	Omitted              []string `json:"omitted"`
	OmittedTaskSummaries []string `json:"omitted_task_summaries"`
}

func (p *Plugin) buildTaskContext(chat Chat, taskID, roleID, query string, occurredAt int64, dispatchID string) string {
	config := p.summaryConfig(p.ctx)
	coverage, sections := p.buildTaskSections(chat.ID, taskID, query, occurredAt, 0, false, config)
	p.recordContextBuild(chat.ID, taskID, dispatchID, "fresh", coverage, config)
	return capRecentContext(strings.Join(sections, "\n\n"), config.ContextByteBudget)
}

func (p *Plugin) buildTaskRoleBridge(chat Chat, taskID, roleID, query string, occurredAt int64, dispatchID string) string {
	config := p.summaryConfig(p.ctx)
	last, err := p.store.latestRoleActivity(chat.ID, taskID, roleID, occurredAt)
	if err != nil {
		slog.Warn("chat task role activity lookup failed", "task_id", taskID, "role_id", roleID, "error", err)
		return ""
	}
	coverage, sections := p.buildTaskSections(chat.ID, taskID, query, occurredAt, last, true, config)
	p.recordContextBuild(chat.ID, taskID, dispatchID, "bridge", coverage, config)
	if len(sections) == 0 {
		return ""
	}
	bridge := "While you were away, this Task received work that your session did not see. Catch up from this context:\n\n" + strings.Join(sections, "\n\n")
	return capRecentContext(bridge, config.ContextByteBudget)
}

func (p *Plugin) buildTaskSections(chatID, taskID, query string, occurredAt, after int64, bridge bool, config SummaryConfig) (*contextCoverage, []string) {
	coverage := &contextCoverage{TaskSummaries: []string{}, Summarized: []string{}, Raw: []string{}, RawMessages: []string{}, Omitted: []string{}, OmittedTaskSummaries: []string{}}
	if !config.ContextEnabled {
		return coverage, nil
	}
	// Determine the raw tail first so those exact Turns can be excluded from
	// the summary layer even when word/byte budgets truncate their messages.
	tail, rawTurnIDs := p.taskTailSection(taskID, occurredAt, after, config.TailTurns, config.TailWordBudget, config.TailByteBudget, coverage)
	summaries := p.taskSummarySection(taskID, occurredAt, after, rawTurnIDs, config.SummaryCharBudget, coverage)
	sections := []string{}
	if !bridge {
		if ended := p.endedTaskSummarySection(chatID, taskID, config.EndedTaskSummaryCharBudget, coverage); ended != "" {
			sections = append(sections, ended)
		}
	}
	sections = append(sections, nonEmpty(summaries, tail)...)
	if recall := p.buildHindsightRecallSection(chatID, query, lastString(sections), occurredAt); recall != "" {
		sections = append(sections, recall)
	}
	p.taskContextOmissions(taskID, occurredAt, after, coverage)
	coverage.TaskSummaries = uniqueSorted(coverage.TaskSummaries)
	coverage.Summarized = uniqueSorted(coverage.Summarized)
	coverage.Raw = uniqueSorted(coverage.Raw)
	coverage.RawMessages = uniqueSorted(coverage.RawMessages)
	coverage.Omitted = uniqueSorted(coverage.Omitted)
	coverage.OmittedTaskSummaries = uniqueSorted(coverage.OmittedTaskSummaries)
	return coverage, sections
}

func (p *Plugin) endedTaskSummarySection(chatID, currentTaskID string, charBudget int, coverage *contextCoverage) string {
	if charBudget <= 0 {
		return ""
	}
	var summaries []TaskSummary
	if err := p.store.db.Where("chat_id = ? AND task_id <> ? AND status = ? AND final = ?", chatID, currentTaskID, "completed", true).
		Order("updated_at desc, task_id desc").Limit(50).Find(&summaries).Error; err != nil {
		slog.Warn("chat ended-task summary context failed", "chat_id", chatID, "task_id", currentTaskID, "error", err)
		return ""
	}
	picked, used := []TaskSummary{}, 0
	for _, summary := range summaries {
		length := len([]rune(summary.Text))
		if used+length > charBudget && len(picked) > 0 {
			coverage.OmittedTaskSummaries = append(coverage.OmittedTaskSummaries, summary.TaskID)
			continue
		}
		if length > charBudget-used {
			summary.Text = truncateText(summary.Text, charBudget-used)
		}
		used += len([]rune(summary.Text))
		picked = append(picked, summary)
		coverage.TaskSummaries = append(coverage.TaskSummaries, summary.TaskID)
		if used >= charBudget {
			break
		}
	}
	if len(picked) == 0 {
		return ""
	}
	lines := []string{"Final summaries of other ended Tasks in this Chat (most recent last):"}
	for index := len(picked) - 1; index >= 0; index-- {
		lines = append(lines, fmt.Sprintf("- [Task %s]\n%s", picked[index].TaskID, picked[index].Text))
	}
	return strings.Join(lines, "\n\n")
}

func (p *Plugin) taskSummarySection(taskID string, before, after int64, rawTurnIDs map[string]bool, charBudget int, coverage *contextCoverage) string {
	if charBudget <= 0 {
		return ""
	}
	var summaries []TurnSummary
	query := p.store.db.Model(&TurnSummary{}).Joins("JOIN turns ON turns.id = turn_summaries.turn_id").
		Where("turns.task_id = ? AND turns.ended_at IS NOT NULL AND turn_summaries.status = ? AND turn_summaries.occurred_at < ?", taskID, "completed", before)
	if after > 0 {
		query = query.Where("turn_summaries.occurred_at > ?", after)
	}
	if len(rawTurnIDs) > 0 {
		ids := make([]string, 0, len(rawTurnIDs))
		for id := range rawTurnIDs {
			ids = append(ids, id)
		}
		query = query.Where("turn_summaries.turn_id NOT IN ?", ids)
	}
	if err := query.Order("turn_summaries.occurred_at desc, turn_summaries.turn_id desc").Find(&summaries).Error; err != nil {
		slog.Warn("chat task summary context failed", "task_id", taskID, "error", err)
		return ""
	}
	picked, used := []TurnSummary{}, 0
	for _, summary := range summaries {
		length := len([]rune(summary.Summary))
		if used+length > charBudget && len(picked) > 0 {
			break
		}
		if length > charBudget-used {
			summary.Summary = truncateText(summary.Summary, charBudget-used)
		}
		used += len([]rune(summary.Summary))
		picked = append(picked, summary)
		if used >= charBudget {
			break
		}
	}
	if len(picked) == 0 {
		return ""
	}
	lines := []string{"Summaries of earlier Turns in this Task outside the raw tail (most recent last):"}
	for index := len(picked) - 1; index >= 0; index-- {
		summary := picked[index]
		coverage.Summarized = append(coverage.Summarized, summary.TurnID)
		label := fallback(summary.RoleName, fallback(summary.RoleID, "agent"))
		lines = append(lines, fmt.Sprintf("- [%s, role %q]\n%s", formatSummaryTime(summary.OccurredAt), label, summary.Summary))
	}
	return strings.Join(lines, "\n\n")
}

func (p *Plugin) taskTailSection(taskID string, before, after int64, turnLimit, wordBudget, byteBudget int, coverage *contextCoverage) (string, map[string]bool) {
	rawTurnIDs := map[string]bool{}
	if turnLimit <= 0 || wordBudget <= 0 || byteBudget <= 0 {
		return "", rawTurnIDs
	}
	var turns []Turn
	turnQuery := p.store.db.Where("task_id = ? AND ended_at IS NOT NULL AND started_at < ?", taskID, before)
	if after > 0 {
		turnQuery = turnQuery.Where("started_at > ?", after)
	}
	if err := turnQuery.Order("ended_at desc, id desc").Limit(turnLimit).Find(&turns).Error; err != nil {
		slog.Warn("chat task raw-turn lookup failed", "task_id", taskID, "error", err)
		return "", rawTurnIDs
	}
	ids := make([]string, 0, len(turns))
	for _, turn := range turns {
		rawTurnIDs[turn.ID] = true
		ids = append(ids, turn.ID)
	}
	var messages []Message
	query := p.store.db.Where("task_id = ? AND role IN ? AND created_at < ?", taskID, []string{"user", "assistant"}, before)
	if after > 0 {
		query = query.Where("created_at > ?", after)
	}
	if err := query.Order("created_at desc, id desc").Limit(2000).Find(&messages).Error; err != nil {
		slog.Warn("chat task raw context failed", "task_id", taskID, "error", err)
		return "", rawTurnIDs
	}
	earliest := int64(0)
	if len(turns) > 0 {
		earliest = turns[len(turns)-1].StartedAt
	}
	candidates := make([]Message, 0, len(messages))
	for _, message := range messages {
		if rawTurnIDs[message.TurnID] || message.Role == "user" && message.TurnID == "" && message.CreatedAt >= earliest {
			candidates = append(candidates, message)
		}
	}
	picked, used := []Message{}, 0
	for _, message := range candidates {
		if message.Role == "assistant" {
			if marker := parseDecisionMarker(message.Text); marker != nil {
				message.Text = message.Text[:marker.Start] + message.Text[marker.End:]
			}
		}
		words := len(splitWords(message.Text))
		if used+words > wordBudget && len(picked) > 0 {
			break
		}
		picked = append(picked, message)
		used += words
		if used >= wordBudget {
			break
		}
	}
	for left, right := 0, len(picked)-1; left < right; left, right = left+1, right-1 {
		picked[left], picked[right] = picked[right], picked[left]
	}
	for _, message := range picked {
		coverage.RawMessages = append(coverage.RawMessages, message.ID)
		if message.TurnID != "" {
			coverage.Raw = append(coverage.Raw, message.TurnID)
		}
	}
	heading := "Visible user/assistant messages from the most recent ended Turns in this Task:"
	if after > 0 {
		heading = "Visible Task messages since this role last ran:"
	}
	return renderRecentHistory(picked, heading, byteBudget), rawTurnIDs
}

func (p *Plugin) taskContextOmissions(taskID string, before, after int64, coverage *contextCoverage) {
	var turns []Turn
	query := p.store.db.Where("task_id = ? AND ended_at IS NOT NULL AND started_at < ?", taskID, before)
	if after > 0 {
		query = query.Where("started_at > ?", after)
	}
	if err := query.Find(&turns).Error; err != nil {
		return
	}
	included := map[string]bool{}
	for _, id := range append(append([]string{}, coverage.Summarized...), coverage.Raw...) {
		included[id] = true
	}
	for _, turn := range turns {
		if !included[turn.ID] {
			coverage.Omitted = append(coverage.Omitted, turn.ID)
		}
	}
}

func (p *Plugin) recordContextBuild(chatID, taskID, dispatchID, kind string, coverage *contextCoverage, config SummaryConfig) {
	row := &ContextBuild{ID: newID(), ChatID: chatID, TaskID: taskID, DispatchID: dispatchID, Kind: kind,
		Coverage: encodeJSON(coverage), SummaryCharBudget: config.SummaryCharBudget, EndedTaskSummaryCharBudget: config.EndedTaskSummaryCharBudget,
		TailTurns: config.TailTurns, TailWordBudget: config.TailWordBudget, TailByteBudget: config.TailByteBudget, ContextByteBudget: config.ContextByteBudget, CreatedAt: nowMillis()}
	if err := p.store.db.Create(row).Error; err != nil {
		slog.Warn("chat context build record failed", "chat_id", chatID, "dispatch_id", dispatchID, "error", err)
	}
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func sortMessages(messages []Message) {
	sort.Slice(messages, func(i, j int) bool {
		if messages[i].CreatedAt == messages[j].CreatedAt {
			return messages[i].ID < messages[j].ID
		}
		return messages[i].CreatedAt < messages[j].CreatedAt
	})
}
