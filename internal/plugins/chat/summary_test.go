package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSummarizeTranscriptPromptAndParsing(t *testing.T) {
	// The HTTP layer is the llm plugin's job (tested there); here the
	// completer is faked and the prompt shape is asserted.
	var messages []map[string]string
	complete := func(_ context.Context, incoming []map[string]string, _ bool, _ int) (completionResult, error) {
		messages = incoming
		return completionResult{Content: "## 任务\n测试\n## 关键动作与改动\n- x\n## 结果\n好\n## 未决事项\n无", Model: "summary-model"}, nil
	}
	result, err := summarizeTranscript(context.Background(), complete, "### User query\n做事\n\n### Assistant\n完成", 60)
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "summary-model" || !strings.Contains(result.Content, "## 未决事项") {
		t.Fatalf("unexpected result %#v", result)
	}
	if len(messages) != 2 {
		t.Fatalf("messages=%#v", messages)
	}
	user := messages[1]["content"]
	for _, required := range []string{"exactly these four sections", "### User query", "### Assistant"} {
		if !strings.Contains(user, required) {
			t.Errorf("prompt missing %q", required)
		}
	}
}

func TestTurnSummaryBudgetBoundaries(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if got := p.buildTurnSummariesSection("empty", 100, 0, 100, ""); got != "" {
		t.Fatalf("empty DB section=%q", got)
	}
	for _, item := range []TurnSummary{{TurnID: "old", ChatID: "c", RoleName: "Old", Status: "completed", Summary: "old summary", OccurredAt: 10}, {TurnID: "new", ChatID: "c", RoleName: "New", Status: "completed", Summary: "newest summary is long", OccurredAt: 20}} {
		if err := p.store.saveTurnSummary(&item); err != nil {
			t.Fatal(err)
		}
	}
	if got := p.buildTurnSummariesSection("c", 100, 0, 0, ""); got != "" {
		t.Fatalf("zero budget=%q", got)
	}
	got := p.buildTurnSummariesSection("c", 100, 0, 6, "")
	if !strings.Contains(got, "newest") || strings.Contains(got, "old summary") || !strings.Contains(got, "truncated") {
		t.Fatalf("truncated section=%q", got)
	}
	got = p.buildTurnSummariesSection("c", 100, 0, 100, "")
	if strings.Index(got, "old summary") > strings.Index(got, "newest summary") {
		t.Fatalf("not chronological: %q", got)
	}
}

func TestHindsightRecallNormalTimeoutAndFiveXX(t *testing.T) {
	base := HindsightConfig{BankPrefix: "test", TimeoutSeconds: 1, MaxTokens: 123, Limit: 2}
	normal := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/memories/recall") {
			t.Errorf("path=%s", request.URL.Path)
		}
		var payload map[string]any
		_ = json.NewDecoder(request.Body).Decode(&payload)
		if payload["budget"] != "mid" || int(payload["max_tokens"].(float64)) != 123 {
			t.Errorf("payload=%#v", payload)
		}
		_, _ = writer.Write([]byte(`{"results":[{"text":"first"},{"content":"second"},{"memory":"third"}]}`))
	}))
	defer normal.Close()
	config := base
	config.Endpoint = normal.URL
	if got := recallChatMemories(context.Background(), normal.Client(), config, "chat", "continue", "tail", time.Now().UnixMilli()); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("normal=%#v", got)
	}
	failing := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "bad", http.StatusInternalServerError)
	}))
	defer failing.Close()
	config.Endpoint = failing.URL
	if got := recallChatMemories(context.Background(), failing.Client(), config, "chat", "q", "", time.Now().UnixMilli()); len(got) != 0 {
		t.Fatalf("5xx=%#v", got)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = writer.Write([]byte(`{"results":[{"text":"late"}]}`))
	}))
	defer slow.Close()
	config.Endpoint = slow.URL
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if got := recallChatMemories(ctx, slow.Client(), config, "chat", "q", "", time.Now().UnixMilli()); len(got) != 0 {
		t.Fatalf("timeout=%#v", got)
	}
}
