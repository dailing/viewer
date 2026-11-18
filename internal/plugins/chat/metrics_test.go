package chat

import (
	"testing"
	"time"
)

func metricPointer(value int64) *int64 { return &value }

func TestAggregateChatMetricsSeparatesActivityAndTokenSubsets(t *testing.T) {
	location := time.UTC
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, location).UnixMilli()
	to := time.Date(2026, 11, 3, 0, 0, 0, 0, location).UnixMilli()
	ended := from + 60_000
	stop := "error"
	tasks := []Task{{ID: "task-a", ChatID: "chat-a", Title: "A"}, {ID: "task-b", ChatID: "chat-a", Title: "B"}}
	created := []Task{{ID: "task-a", CreatedAt: from + 1}}
	prompts := []metricMessage{{ID: "message-a", TaskID: "task-a", TurnID: "turn-a", CreatedAt: from + 2}}
	turns := []Turn{{ID: "turn-a", TaskID: "task-a", RoleID: "role-a", RoleName: "Coder", Provider: "codex", Model: "model-a", StartedAt: from + 3, EndedAt: &ended, StopReason: &stop}}
	decisions := []metricDecision{{TaskID: "task-a", Status: "resolved", CreatedAt: from + 4}}
	blocks := []metricBlock{{TaskID: "task-a", TurnID: "turn-a", Payload: `{"name":"terminal"}`, OccurredAt: from + 5}}
	usages := []AgentTokenUsage{{TaskID: "task-a", TurnID: "turn-a", OccurredAt: from + 6,
		InputTokens: metricPointer(100), CachedInputTokens: metricPointer(40), OutputTokens: metricPointer(25), ReasoningOutputTokens: metricPointer(8), TotalTokens: metricPointer(125)}}
	result := aggregateChatMetrics(tasks, created, prompts, turns, decisions, blocks, usages, from, to, location, "UTC")
	summary := result["summary"].(map[string]any)
	if summary["tasks"] != 1 || summary["prompts"] != 1 || summary["turns"] != 1 || summary["decisions"] != 1 || summary["tool_calls"] != 1 {
		t.Fatalf("activity summary = %#v", summary)
	}
	if summary["total_tokens"] != int64(125) || summary["cached_input_tokens"] != int64(40) || summary["reasoning_output_tokens"] != int64(8) {
		t.Fatalf("token summary = %#v", summary)
	}
	if summary["total_tokens"] == summary["input_tokens"].(int64)+summary["cached_input_tokens"].(int64)+summary["output_tokens"].(int64)+summary["reasoning_output_tokens"].(int64) {
		t.Fatal("subset fields were incorrectly added into total")
	}
	if summary["failed_turns"] != int64(1) || summary["average_turn_latency_ms"] != int64(59997) {
		t.Fatalf("turn summary = %#v", summary)
	}
	topTasks := result["top_tasks"].([]metricTaskRow)
	topTurns := result["top_turns"].([]metricTurnRow)
	topRoles := result["top_roles"].([]metricRoleRow)
	topTools := result["top_tools"].([]metricToolRow)
	if len(topTasks) != 1 || topTasks[0].Title != "A" || topTasks[0].TotalTokens != 125 {
		t.Fatalf("top tasks = %#v", topTasks)
	}
	if len(topTurns) != 1 || topTurns[0].ToolCalls != 1 || len(topRoles) != 1 || topRoles[0].Name != "Coder" || len(topTools) != 1 || topTools[0].Name != "terminal" {
		t.Fatalf("rankings = %#v %#v %#v", topTurns, topRoles, topTools)
	}
	days := result["daily"].([]chatMetricDay)
	if len(days) != 2 || days[0].TotalTokens != 125 || days[1].TotalTokens != 0 {
		t.Fatalf("daily = %#v", days)
	}
}

func TestMetricsDashboardAppliesChatAndTaskFilters(t *testing.T) {
	store, err := openStorePath(t.TempDir() + "/chat.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer closeGORM(store.db)
	now := time.Now()
	for _, task := range []Task{{ID: "task-a", ChatID: "chat-a", Title: "A", CreatedAt: now.UnixMilli()}, {ID: "task-b", ChatID: "chat-b", Title: "B", CreatedAt: now.UnixMilli()}} {
		if err := store.db.Create(&task).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := store.db.Create(&Message{ID: "message-a", ChatID: "chat-a", TaskID: "task-a", Role: "user", Text: "hello", CreatedAt: now.UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&Message{ID: "message-b", ChatID: "chat-b", TaskID: "task-b", Role: "user", Text: "other", CreatedAt: now.UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	for _, block := range []MessageBlock{
		{ID: "block-a", ChatID: "chat-a", TaskID: "task-a", TurnID: "turn-a", Kind: "tool_call", Payload: `{"name":"read"}`, OccurredAt: now.UnixMilli()},
		{ID: "block-b", ChatID: "chat-a", TaskID: "task-a", TurnID: "turn-a", Kind: "command", Payload: `{"name":"bash"}`, OccurredAt: now.UnixMilli()},
		{ID: "block-c", ChatID: "chat-a", TaskID: "task-a", TurnID: "turn-a", Kind: "tool_result", Payload: `{"name":"read"}`, OccurredAt: now.UnixMilli()},
	} {
		if err := store.db.Create(&block).Error; err != nil {
			t.Fatal(err)
		}
	}
	plugin := &Plugin{store: store}
	result, err := plugin.metricsDashboard(chatMetricsRequest{From: now.Add(-time.Hour).UnixMilli(), To: now.Add(time.Hour).UnixMilli(), Timezone: "UTC", ChatID: "chat-a", TaskID: "task-a"})
	if err != nil {
		t.Fatal(err)
	}
	summary := result["summary"].(map[string]any)
	if summary["prompts"] != 1 || summary["tasks"] != 1 || summary["tool_calls"] != 2 {
		t.Fatalf("filtered summary = %#v", summary)
	}
}

func TestCodexUsageBackfillUsesLastAndIsIdempotent(t *testing.T) {
	store, err := openStorePath(t.TempDir() + "/chat.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer closeGORM(store.db)
	now := time.Now().UnixMilli()
	if err := store.db.Create(&Turn{ID: "turn-a", ChatID: "chat-a", TaskID: "task-a", Agent: "codex-app-server", Provider: "openai", Model: "gpt-test", StartedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	raw := `{"method":"thread/tokenUsage/updated","params":{"tokenUsage":{"last":{"inputTokens":100,"cachedInputTokens":60,"outputTokens":20,"totalTokens":120},"total":{"inputTokens":9000,"outputTokens":1000,"totalTokens":10000}}}}`
	event := TurnEvent{ID: "event-a", ChatID: "chat-a", TaskID: "task-a", TurnID: "turn-a", Provider: "openai", SessionID: "session-a", Kind: "thread/tokenUsage/updated", RawJSON: raw, OccurredAt: now}
	if err := store.db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	plugin := &Plugin{store: store}
	for range 2 {
		if err := plugin.backfillCodexTokenUsage(); err != nil {
			t.Fatal(err)
		}
	}
	var usages []AgentTokenUsage
	if err := store.db.Find(&usages).Error; err != nil {
		t.Fatal(err)
	}
	if len(usages) != 1 || usages[0].TotalTokens == nil || *usages[0].TotalTokens != 120 || usages[0].Model != "gpt-test" {
		t.Fatalf("usages = %#v", usages)
	}
}
