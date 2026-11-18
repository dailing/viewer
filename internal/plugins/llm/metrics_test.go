package llm

import (
	"errors"
	"strings"
	"testing"
	"time"

	"viewer/internal/agentdriver"
	"viewer/internal/metrics"
)

func int64Pointer(value int64) *int64 { return &value }

func TestUsageStoreRecordsFallbackAttemptsAndAggregatesRealUsage(t *testing.T) {
	store, err := openUsageStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	plugin := &Plugin{usageDB: store}
	started := time.Now().Add(-time.Second)
	callID := plugin.beginCall("rpc", "viewer-chat", "turn_summary", "requested", callTracking{ChatID: "chat-1", TaskID: "task-1", TurnID: "turn-1"})
	if callID == "" {
		t.Fatal("call was not recorded")
	}
	plugin.recordAttempt(callID, 1, "primary", "requested", "primary-model", started, errors.New("http 502"), nil)
	plugin.recordAttempt(callID, 2, "fallback", "requested", "fallback-model", started, nil, &agentdriver.TokenUsage{
		InputTokens: int64Pointer(100), CachedInputTokens: int64Pointer(40), OutputTokens: int64Pointer(20), ReasoningOutputTokens: int64Pointer(5), TotalTokens: int64Pointer(120),
	})
	plugin.finishCall(callID, nil)
	var attempts []LLMCallAttempt
	if err := store.db.Order("attempt").Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[0].Status != "failed" || attempts[1].Status != "succeeded" {
		t.Fatalf("attempts = %#v", attempts)
	}
	if attempts[0].TotalTokens != nil {
		t.Fatal("failed attempt without upstream usage must stay missing")
	}
	from := time.Now().Add(-time.Hour).UnixMilli()
	to := time.Now().Add(time.Hour).UnixMilli()
	result, err := plugin.metricsDashboard(metricsRequest{From: from, To: to, Timezone: "UTC", ChatID: "chat-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	summary := result["summary"].(map[string]any)
	if summary["calls"] != 1 || summary["attempts"] != 2 || summary["total_tokens"] != int64(120) || summary["cached_input_tokens"] != int64(40) {
		t.Fatalf("summary = %#v", summary)
	}
	if summary["usage_coverage_percent"] != float64(50) {
		t.Fatalf("coverage = %#v", summary["usage_coverage_percent"])
	}
	if len(result["top_tasks"].([]llmTaskMetric)) != 1 || len(result["by_purpose"].([]llmGroupMetric)) != 1 {
		t.Fatalf("groups = %#v", result)
	}
}

func TestLLMDailySeriesFillsEmptyLocalDays(t *testing.T) {
	location := time.UTC
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, location).UnixMilli()
	to := time.Date(2026, 11, 4, 0, 0, 0, 0, location).UnixMilli()
	result := aggregateLLMMetrics(nil, nil, from, to, location, "UTC")
	days := result["daily"].([]llmDailyMetric)
	if len(days) != 3 || days[0].Date != "2026-11-01" || days[2].Date != "2026-11-03" {
		t.Fatalf("days = %#v", days)
	}
	if got := metrics.DayKey(from, location); got != "2026-11-01" {
		t.Fatal(got)
	}
}

func TestResponseUsageCaptureReadsFinalSSEUsage(t *testing.T) {
	capture := &responseUsageCapture{}
	prefix := strings.Repeat("x", responseUsageTailBytes+17)
	_, _ = capture.Write([]byte(prefix))
	_, _ = capture.Write([]byte("\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"total_tokens\":15}}\n\ndata: [DONE]\n"))
	usage := usageFromResponseBytes(capture.Bytes())
	if usage == nil || usage.TotalTokens == nil || *usage.TotalTokens != 15 || usage.InputTokens == nil || *usage.InputTokens != 12 {
		t.Fatalf("usage = %#v", usage)
	}
	if len(capture.Bytes()) != responseUsageTailBytes {
		t.Fatalf("capture length = %d", len(capture.Bytes()))
	}
}
