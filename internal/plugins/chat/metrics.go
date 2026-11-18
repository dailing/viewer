package chat

import (
	"encoding/json"
	"sort"
	"time"

	"gorm.io/gorm"

	"viewer/internal/agentdriver"
	"viewer/internal/metrics"
	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

type chatMetricsRequest struct {
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	Timezone string `json:"timezone"`
	ChatID   string `json:"chat_id"`
	TaskID   string `json:"task_id"`
}

type chatMetricDay struct {
	Date         string `json:"date"`
	Tasks        int64  `json:"tasks"`
	Prompts      int64  `json:"prompts"`
	Turns        int64  `json:"turns"`
	Decisions    int64  `json:"decisions"`
	ToolCalls    int64  `json:"tool_calls"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
}

type metricTaskRow struct {
	TaskID       string `json:"task_id"`
	ChatID       string `json:"chat_id"`
	Title        string `json:"title"`
	Prompts      int64  `json:"prompts"`
	Turns        int64  `json:"turns"`
	Decisions    int64  `json:"decisions"`
	ToolCalls    int64  `json:"tool_calls"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
}

type metricTurnRow struct {
	TurnID      string `json:"turn_id"`
	TaskID      string `json:"task_id"`
	TaskTitle   string `json:"task_title"`
	RoleName    string `json:"role_name"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	StartedAt   int64  `json:"started_at"`
	DurationMS  int64  `json:"duration_ms"`
	TotalTokens int64  `json:"total_tokens"`
	ToolCalls   int64  `json:"tool_calls"`
}

type metricRoleRow struct {
	Name        string `json:"name"`
	Turns       int64  `json:"turns"`
	TotalTokens int64  `json:"total_tokens"`
}

type metricToolRow struct {
	Name  string `json:"name"`
	Calls int64  `json:"calls"`
}

type metricMessage struct {
	ID, TaskID, TurnID string
	CreatedAt          int64
}

type metricBlock struct {
	TaskID, TurnID, Payload string
	OccurredAt              int64
}

type metricDecision struct {
	TaskID    string
	Status    string
	CreatedAt int64
}

func (p *Plugin) handleMetricsDashboard(frame busclient.Frame) {
	raw, ok := pluginrpc.Object(frame)
	if !ok {
		p.reply(frame, nil, errBadRequest)
		return
	}
	var request chatMetricsRequest
	encoded, _ := json.Marshal(raw)
	if err := json.Unmarshal(encoded, &request); err != nil {
		p.reply(frame, nil, errBadRequest)
		return
	}
	response, err := p.metricsDashboard(request)
	p.reply(frame, response, err)
}

func (p *Plugin) metricsDashboard(request chatMetricsRequest) (map[string]any, error) {
	from, to, location, err := metrics.NormalizeRange(request.From, request.To, request.Timezone, time.Now())
	if err != nil {
		return nil, errBadRequest
	}
	var tasks []Task
	if err := metricTaskFilter(p.store.db.Model(&Task{}), request).Find(&tasks).Error; err != nil {
		return nil, err
	}
	var prompts []metricMessage
	if err := metricRange(metricFilter(p.store.db.Model(&Message{}), request), "created_at", from, to).
		Where("role = ?", "user").Select("id, task_id, turn_id, created_at").Scan(&prompts).Error; err != nil {
		return nil, err
	}
	var turns []Turn
	if err := metricRange(metricFilter(p.store.db.Model(&Turn{}), request), "started_at", from, to).Find(&turns).Error; err != nil {
		return nil, err
	}
	var decisions []metricDecision
	if err := metricRange(metricFilter(p.store.db.Model(&Decision{}), request), "created_at", from, to).
		Select("task_id, status, created_at").Scan(&decisions).Error; err != nil {
		return nil, err
	}
	var blocks []metricBlock
	if err := metricRange(metricFilter(p.store.db.Model(&MessageBlock{}), request), "occurred_at", from, to).
		Where("kind IN ?", []string{agentdriver.KindToolCall, agentdriver.KindCommand}).Select("task_id, turn_id, payload, occurred_at").Scan(&blocks).Error; err != nil {
		return nil, err
	}
	var usages []AgentTokenUsage
	if err := metricRange(metricFilter(p.store.db.Model(&AgentTokenUsage{}), request), "occurred_at", from, to).Find(&usages).Error; err != nil {
		return nil, err
	}
	var createdTasks []Task
	if err := metricRange(metricTaskFilter(p.store.db.Model(&Task{}), request), "created_at", from, to).Find(&createdTasks).Error; err != nil {
		return nil, err
	}
	return aggregateChatMetrics(tasks, createdTasks, prompts, turns, decisions, blocks, usages, from, to, location, request.Timezone), nil
}

func metricFilter(query *gorm.DB, request chatMetricsRequest) *gorm.DB {
	if request.ChatID != "" {
		query = query.Where("chat_id = ?", request.ChatID)
	}
	if request.TaskID != "" {
		query = query.Where("task_id = ?", request.TaskID)
	}
	return query
}

func metricTaskFilter(query *gorm.DB, request chatMetricsRequest) *gorm.DB {
	if request.ChatID != "" {
		query = query.Where("chat_id = ?", request.ChatID)
	}
	if request.TaskID != "" {
		query = query.Where("id = ?", request.TaskID)
	}
	return query
}

func metricRange(query *gorm.DB, column string, from, to int64) *gorm.DB {
	return query.Where(column+" >= ? AND "+column+" < ?", from, to)
}

func aggregateChatMetrics(tasks, createdTasks []Task, prompts []metricMessage, turns []Turn, decisions []metricDecision, blocks []metricBlock, usages []AgentTokenUsage, from, to int64, location *time.Location, timezone string) map[string]any {
	days := map[string]*chatMetricDay{}
	for _, date := range metrics.DayKeys(from, to, location) {
		days[date] = &chatMetricDay{Date: date}
	}
	day := func(timestamp int64) *chatMetricDay { return days[metrics.DayKey(timestamp, location)] }
	taskRows := map[string]*metricTaskRow{}
	turnRows := map[string]*metricTurnRow{}
	roleRows := map[string]*metricRoleRow{}
	toolRows := map[string]*metricToolRow{}
	turnByID := map[string]Turn{}
	taskByID := map[string]Task{}
	for _, task := range tasks {
		taskByID[task.ID] = task
	}
	taskRow := func(taskID string) *metricTaskRow {
		row := taskRows[taskID]
		if row == nil {
			task := taskByID[taskID]
			row = &metricTaskRow{TaskID: taskID, ChatID: task.ChatID, Title: task.Title}
			if row.Title == "" {
				row.Title = taskID
			}
			taskRows[taskID] = row
		}
		return row
	}
	for _, task := range createdTasks {
		if point := day(task.CreatedAt); point != nil {
			point.Tasks++
		}
	}
	for _, message := range prompts {
		if point := day(message.CreatedAt); point != nil {
			point.Prompts++
		}
		taskRow(message.TaskID).Prompts++
	}
	var completedTurns, failedTurns, turnLatency, completedLatency int64
	for _, turn := range turns {
		turnByID[turn.ID] = turn
		if point := day(turn.StartedAt); point != nil {
			point.Turns++
		}
		taskRow(turn.TaskID).Turns++
		duration := int64(0)
		if turn.EndedAt != nil {
			completedTurns++
			duration = *turn.EndedAt - turn.StartedAt
			turnLatency += duration
			completedLatency++
		}
		if turn.StopReason != nil && *turn.StopReason == "error" {
			failedTurns++
		}
		turnRows[turn.ID] = &metricTurnRow{TurnID: turn.ID, TaskID: turn.TaskID, TaskTitle: taskRow(turn.TaskID).Title,
			RoleName: turn.RoleName, Provider: turn.Provider, Model: turn.Model, StartedAt: turn.StartedAt, DurationMS: duration}
		role := turn.RoleName
		if role == "" {
			role = turn.RoleID
		}
		if role == "" {
			role = "unknown"
		}
		roleRow := roleRows[role]
		if roleRow == nil {
			roleRow = &metricRoleRow{Name: role}
			roleRows[role] = roleRow
		}
		roleRow.Turns++
	}
	var resolvedDecisions, pendingDecisions int64
	for _, decision := range decisions {
		if point := day(decision.CreatedAt); point != nil {
			point.Decisions++
		}
		taskRow(decision.TaskID).Decisions++
		if decision.Status == "resolved" {
			resolvedDecisions++
		} else if decision.Status == "pending" {
			pendingDecisions++
		}
	}
	for _, block := range blocks {
		if point := day(block.OccurredAt); point != nil {
			point.ToolCalls++
		}
		taskRow(block.TaskID).ToolCalls++
		if row := turnRows[block.TurnID]; row != nil {
			row.ToolCalls++
		}
		name := payloadString(block.Payload, "name")
		if name == "" {
			name = "unknown"
		}
		row := toolRows[name]
		if row == nil {
			row = &metricToolRow{Name: name}
			toolRows[name] = row
		}
		row.Calls++
	}
	var input, cached, cacheWrite, output, reasoning, total, usageEvents, tokenTurns int64
	seenTokenTurn := map[string]bool{}
	for _, usage := range usages {
		usageEvents++
		input += metricInt64(usage.InputTokens)
		cached += metricInt64(usage.CachedInputTokens)
		cacheWrite += metricInt64(usage.CacheWriteInputTokens)
		output += metricInt64(usage.OutputTokens)
		reasoning += metricInt64(usage.ReasoningOutputTokens)
		total += metricInt64(usage.TotalTokens)
		if point := day(usage.OccurredAt); point != nil {
			point.InputTokens += metricInt64(usage.InputTokens)
			point.OutputTokens += metricInt64(usage.OutputTokens)
			point.TotalTokens += metricInt64(usage.TotalTokens)
		}
		task := taskRow(usage.TaskID)
		task.InputTokens += metricInt64(usage.InputTokens)
		task.OutputTokens += metricInt64(usage.OutputTokens)
		task.TotalTokens += metricInt64(usage.TotalTokens)
		if row := turnRows[usage.TurnID]; row != nil {
			row.TotalTokens += metricInt64(usage.TotalTokens)
			if !seenTokenTurn[usage.TurnID] {
				seenTokenTurn[usage.TurnID] = true
				tokenTurns++
			}
			turn := turnByID[usage.TurnID]
			role := turn.RoleName
			if role == "" {
				role = turn.RoleID
			}
			if role == "" {
				role = "unknown"
			}
			if roleRow := roleRows[role]; roleRow != nil {
				roleRow.TotalTokens += metricInt64(usage.TotalTokens)
			}
		}
	}
	daily := make([]chatMetricDay, 0, len(days))
	for _, date := range metrics.DayKeys(from, to, location) {
		daily = append(daily, *days[date])
	}
	topTasks := make([]metricTaskRow, 0, len(taskRows))
	for _, row := range taskRows {
		topTasks = append(topTasks, *row)
	}
	topTurns := make([]metricTurnRow, 0, len(turnRows))
	for _, row := range turnRows {
		topTurns = append(topTurns, *row)
	}
	topRoles := make([]metricRoleRow, 0, len(roleRows))
	for _, row := range roleRows {
		topRoles = append(topRoles, *row)
	}
	topTools := make([]metricToolRow, 0, len(toolRows))
	for _, row := range toolRows {
		topTools = append(topTools, *row)
	}
	sort.Slice(topTasks, func(i, j int) bool {
		return metricMore(topTasks[i].TotalTokens, topTasks[i].Turns+topTasks[i].Prompts, topTasks[i].TaskID, topTasks[j].TotalTokens, topTasks[j].Turns+topTasks[j].Prompts, topTasks[j].TaskID)
	})
	sort.Slice(topTurns, func(i, j int) bool {
		return metricMore(topTurns[i].TotalTokens, topTurns[i].ToolCalls, topTurns[i].TurnID, topTurns[j].TotalTokens, topTurns[j].ToolCalls, topTurns[j].TurnID)
	})
	sort.Slice(topRoles, func(i, j int) bool {
		return metricMore(topRoles[i].TotalTokens, topRoles[i].Turns, topRoles[i].Name, topRoles[j].TotalTokens, topRoles[j].Turns, topRoles[j].Name)
	})
	sort.Slice(topTools, func(i, j int) bool {
		return metricMore(topTools[i].Calls, 0, topTools[i].Name, topTools[j].Calls, 0, topTools[j].Name)
	})
	topTasks = topTasks[:min(len(topTasks), 10)]
	topTurns = topTurns[:min(len(topTurns), 10)]
	topRoles = topRoles[:min(len(topRoles), 10)]
	topTools = topTools[:min(len(topTools), 10)]
	averageLatency := int64(0)
	if completedLatency > 0 {
		averageLatency = turnLatency / completedLatency
	}
	coverage := float64(0)
	if len(turns) > 0 {
		coverage = float64(tokenTurns) * 100 / float64(len(turns))
	}
	if timezone == "" {
		timezone = location.String()
	}
	return map[string]any{
		"from": from, "to": to, "timezone": timezone,
		"summary": map[string]any{
			"tasks": len(createdTasks), "prompts": len(prompts), "turns": len(turns), "completed_turns": completedTurns,
			"failed_turns": failedTurns, "decisions": len(decisions), "resolved_decisions": resolvedDecisions,
			"pending_decisions": pendingDecisions, "tool_calls": len(blocks), "usage_events": usageEvents,
			"input_tokens": input, "cached_input_tokens": cached, "cache_write_input_tokens": cacheWrite,
			"output_tokens": output, "reasoning_output_tokens": reasoning, "total_tokens": total,
			"average_turn_latency_ms": averageLatency, "turns_with_usage": tokenTurns, "usage_coverage_percent": coverage,
		},
		"daily": daily, "top_tasks": topTasks, "top_turns": topTurns, "top_roles": topRoles, "top_tools": topTools,
	}
}

func metricInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func metricMore(a, aTie int64, aID string, b, bTie int64, bID string) bool {
	if a != b {
		return a > b
	}
	if aTie != bTie {
		return aTie > bTie
	}
	return aID < bID
}
