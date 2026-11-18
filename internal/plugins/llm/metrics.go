package llm

import (
	"encoding/json"
	"sort"
	"time"

	"gorm.io/gorm"

	"viewer/internal/metrics"
	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

type metricsRequest struct {
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	Timezone string `json:"timezone"`
	ChatID   string `json:"chat_id"`
	TaskID   string `json:"task_id"`
}

type llmDailyMetric struct {
	Date         string `json:"date"`
	Calls        int64  `json:"calls"`
	Attempts     int64  `json:"attempts"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
}

type llmGroupMetric struct {
	Name        string `json:"name"`
	Calls       int64  `json:"calls,omitempty"`
	Attempts    int64  `json:"attempts"`
	TotalTokens int64  `json:"total_tokens"`
}

type llmTaskMetric struct {
	TaskID      string `json:"task_id"`
	Attempts    int64  `json:"attempts"`
	TotalTokens int64  `json:"total_tokens"`
}

func (p *Plugin) handleMetricsDashboard(frame busclient.Frame) {
	raw, ok := pluginrpc.Object(frame)
	if !ok {
		_ = pluginrpc.RespondError(p.client, frame, "bad_request", "payload must be an object")
		return
	}
	var request metricsRequest
	encoded, _ := json.Marshal(raw)
	if err := json.Unmarshal(encoded, &request); err != nil {
		_ = pluginrpc.RespondError(p.client, frame, "bad_request", err.Error())
		return
	}
	response, err := p.metricsDashboard(request)
	if err != nil {
		_ = pluginrpc.RespondError(p.client, frame, "metrics_error", err.Error())
		return
	}
	_ = pluginrpc.Respond(p.client, frame, response)
}

func (p *Plugin) metricsDashboard(request metricsRequest) (map[string]any, error) {
	if p.usageDB == nil {
		return map[string]any{"available": false, "summary": map[string]any{}, "daily": []any{}, "by_purpose": []any{}, "by_model": []any{}, "top_tasks": []any{}}, nil
	}
	from, to, location, err := metrics.NormalizeRange(request.From, request.To, request.Timezone, time.Now())
	if err != nil {
		return nil, err
	}
	callQuery := p.usageDB.db.Model(&LLMCall{}).Where("started_at >= ? AND started_at < ?", from, to)
	if request.ChatID != "" {
		callQuery = callQuery.Where("chat_id = ?", request.ChatID)
	}
	if request.TaskID != "" {
		callQuery = callQuery.Where("task_id = ?", request.TaskID)
	}
	var calls []LLMCall
	if err := callQuery.Find(&calls).Error; err != nil {
		return nil, err
	}
	attemptQuery := p.usageDB.db.Model(&LLMCallAttempt{}).Where("started_at >= ? AND started_at < ?", from, to)
	if request.ChatID != "" || request.TaskID != "" {
		attemptQuery = attemptQuery.Where("call_id IN (?)", p.filteredCallSubquery(request))
	}
	var attempts []LLMCallAttempt
	if err := attemptQuery.Find(&attempts).Error; err != nil {
		return nil, err
	}
	return aggregateLLMMetrics(calls, attempts, from, to, location, request.Timezone), nil
}

func (p *Plugin) filteredCallSubquery(request metricsRequest) *gorm.DB {
	query := p.usageDB.db.Model(&LLMCall{}).Select("id")
	if request.ChatID != "" {
		query = query.Where("chat_id = ?", request.ChatID)
	}
	if request.TaskID != "" {
		query = query.Where("task_id = ?", request.TaskID)
	}
	return query
}

func aggregateLLMMetrics(calls []LLMCall, attempts []LLMCallAttempt, from, to int64, location *time.Location, timezone string) map[string]any {
	daily := map[string]*llmDailyMetric{}
	for _, date := range metrics.DayKeys(from, to, location) {
		daily[date] = &llmDailyMetric{Date: date}
	}
	callByID := map[string]LLMCall{}
	purposeCalls := map[string]int64{}
	var succeeded, failed int64
	for _, call := range calls {
		callByID[call.ID] = call
		purpose := call.Purpose
		if purpose == "" {
			purpose = "other"
		}
		purposeCalls[purpose]++
		if call.Status == "succeeded" {
			succeeded++
		} else if call.Status != "running" {
			failed++
		}
		if point := daily[metrics.DayKey(call.StartedAt, location)]; point != nil {
			point.Calls++
		}
	}
	var input, cached, cacheWrite, output, reasoning, total, latency, usageAttempts int64
	purposeGroups := map[string]*llmGroupMetric{}
	modelGroups := map[string]*llmGroupMetric{}
	taskGroups := map[string]*llmTaskMetric{}
	for _, attempt := range attempts {
		input += valueOrZero(attempt.InputTokens)
		cached += valueOrZero(attempt.CachedInputTokens)
		cacheWrite += valueOrZero(attempt.CacheWriteInputTokens)
		output += valueOrZero(attempt.OutputTokens)
		reasoning += valueOrZero(attempt.ReasoningOutputTokens)
		total += valueOrZero(attempt.TotalTokens)
		latency += attempt.LatencyMS
		if attempt.TotalTokens != nil {
			usageAttempts++
		}
		if point := daily[metrics.DayKey(attempt.StartedAt, location)]; point != nil {
			point.Attempts++
			point.InputTokens += valueOrZero(attempt.InputTokens)
			point.OutputTokens += valueOrZero(attempt.OutputTokens)
			point.TotalTokens += valueOrZero(attempt.TotalTokens)
		}
		call := callByID[attempt.CallID]
		purpose := call.Purpose
		if purpose == "" {
			purpose = "other"
		}
		group := purposeGroups[purpose]
		if group == nil {
			group = &llmGroupMetric{Name: purpose}
			purposeGroups[purpose] = group
		}
		group.Attempts++
		group.TotalTokens += valueOrZero(attempt.TotalTokens)
		model := attempt.Model
		if model == "" {
			model = "unknown"
		}
		modelGroup := modelGroups[model]
		if modelGroup == nil {
			modelGroup = &llmGroupMetric{Name: model}
			modelGroups[model] = modelGroup
		}
		modelGroup.Attempts++
		modelGroup.TotalTokens += valueOrZero(attempt.TotalTokens)
		if call.TaskID != "" {
			task := taskGroups[call.TaskID]
			if task == nil {
				task = &llmTaskMetric{TaskID: call.TaskID}
				taskGroups[call.TaskID] = task
			}
			task.Attempts++
			task.TotalTokens += valueOrZero(attempt.TotalTokens)
		}
	}
	for purpose, count := range purposeCalls {
		group := purposeGroups[purpose]
		if group == nil {
			group = &llmGroupMetric{Name: purpose}
			purposeGroups[purpose] = group
		}
		group.Calls = count
	}
	dailyList := make([]llmDailyMetric, 0, len(daily))
	for _, date := range metrics.DayKeys(from, to, location) {
		dailyList = append(dailyList, *daily[date])
	}
	purposes := make([]llmGroupMetric, 0, len(purposeGroups))
	for _, group := range purposeGroups {
		purposes = append(purposes, *group)
	}
	models := make([]llmGroupMetric, 0, len(modelGroups))
	for _, group := range modelGroups {
		models = append(models, *group)
	}
	tasks := make([]llmTaskMetric, 0, len(taskGroups))
	for _, task := range taskGroups {
		tasks = append(tasks, *task)
	}
	sort.Slice(purposes, func(i, j int) bool { return purposes[i].TotalTokens > purposes[j].TotalTokens })
	sort.Slice(models, func(i, j int) bool { return models[i].TotalTokens > models[j].TotalTokens })
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].TotalTokens == tasks[j].TotalTokens {
			return tasks[i].TaskID < tasks[j].TaskID
		}
		return tasks[i].TotalTokens > tasks[j].TotalTokens
	})
	if len(tasks) > 10 {
		tasks = tasks[:10]
	}
	averageLatency := int64(0)
	if len(attempts) > 0 {
		averageLatency = latency / int64(len(attempts))
	}
	coverage := float64(0)
	if len(attempts) > 0 {
		coverage = float64(usageAttempts) * 100 / float64(len(attempts))
	}
	if timezone == "" {
		timezone = location.String()
	}
	return map[string]any{
		"available": true, "from": from, "to": to, "timezone": timezone,
		"summary": map[string]any{
			"calls": len(calls), "succeeded_calls": succeeded, "failed_calls": failed, "attempts": len(attempts),
			"input_tokens": input, "cached_input_tokens": cached, "cache_write_input_tokens": cacheWrite,
			"output_tokens": output, "reasoning_output_tokens": reasoning, "total_tokens": total,
			"average_latency_ms": averageLatency, "usage_attempts": usageAttempts, "usage_coverage_percent": coverage,
		},
		"daily": dailyList, "by_purpose": purposes, "by_model": models, "top_tasks": tasks,
	}
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
