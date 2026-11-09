package chat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskSummaryRollsForwardOnlyUncoveredTurnSummaries(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	for _, turnID := range []string{"turn-a", "turn-b", "turn-c"} {
		if err := s.db.Create(&TurnSummary{TurnID: turnID, ChatID: "chat-a", TaskID: "task-a", Status: "completed", Summary: "summary " + turnID, OccurredAt: int64(len(turnID))}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.addMessage(&Message{ID: "raw-must-not-be-read", ChatID: "chat-a", TaskID: "task-a", Role: "assistant", Text: "RAW TRANSCRIPT MUST NOT ENTER ROLLING SUMMARY"}); err != nil {
		t.Fatal(err)
	}
	previous := TaskSummary{Text: "previous compact state", SourceTurnIDs: encodeJSON([]string{"turn-a"}), SourceRevision: 1}
	p := &Plugin{store: s}
	source, newIDs, allIDs, _, err := p.taskSummaryTranscript("task-a", previous, 1000, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(source, "RAW TRANSCRIPT") || !strings.Contains(source, "previous compact state") || !strings.Contains(source, "summary turn-b") || !strings.Contains(source, "summary turn-c") || strings.Contains(source, "summary turn-a\n") {
		t.Fatalf("rolling source mismatch:\n%s", source)
	}
	if strings.Join(newIDs, ",") != "turn-b,turn-c" || strings.Join(allIDs, ",") != "turn-a,turn-b,turn-c" {
		t.Fatalf("coverage ids new=%v all=%v", newIDs, allIDs)
	}
}

func TestGenerateTaskSummaryAdvancesCompletedRevision(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if err := s.createTask(&Task{ID: "task-a", ChatID: "chat-a", Title: "A"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&TurnSummary{TurnID: "turn-a", ChatID: "chat-a", TaskID: "task-a", Status: "completed", Summary: "first turn", OccurredAt: 1}).Error; err != nil {
		t.Fatal(err)
	}
	prompts := []string{}
	p := &Plugin{store: s, ctx: context.Background()}
	p.llmFn = func(_ context.Context, messages []map[string]string, _ bool, _ int) (completionResult, error) {
		prompts = append(prompts, messages[len(messages)-1]["content"])
		return completionResult{Content: "rolling result", Model: "test"}, nil
	}
	p.generateTaskSummary("task-a")
	var first TaskSummary
	if err := s.db.Limit(1).Find(&first, "task_id = ?", "task-a").Error; err != nil || first.Status != "completed" || first.SummaryRevision != 1 {
		t.Fatalf("first summary=%#v err=%v", first, err)
	}
	if err := s.db.Create(&TurnSummary{TurnID: "turn-b", ChatID: "chat-a", TaskID: "task-a", Status: "completed", Summary: "second turn", OccurredAt: 2}).Error; err != nil {
		t.Fatal(err)
	}
	p.generateTaskSummary("task-a")
	var second TaskSummary
	if err := s.db.Limit(1).Find(&second, "task_id = ?", "task-a").Error; err != nil {
		t.Fatal(err)
	}
	if second.SummaryRevision != 2 || strings.Join(decodeStrings(second.SourceTurnIDs), ",") != "turn-a,turn-b" {
		t.Fatalf("second revision did not advance: %#v", second)
	}
	if len(prompts) != 2 || !strings.Contains(prompts[1], "rolling result") || !strings.Contains(prompts[1], "second turn") || strings.Contains(prompts[1], "first turn") {
		t.Fatalf("second prompt was not previous+uncovered: %v", prompts)
	}
}

func TestTaskContextLayersAndRawTailDoNotOverlap(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	for index, id := range []string{"turn-1", "turn-2", "turn-3", "turn-4"} {
		at := int64(index + 1)
		if err := s.db.Create(&Turn{ID: id, ChatID: "chat-a", TaskID: "task-a", RoleID: "role-a", StartedAt: at, EndedAt: &at}).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.Create(&TurnSummary{TurnID: id, ChatID: "chat-a", TaskID: "task-a", Status: "completed", Summary: "summary " + id, OccurredAt: at}).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.addMessage(&Message{ID: "message-" + id, ChatID: "chat-a", TaskID: "task-a", TurnID: id, Role: "assistant", Text: "raw " + id, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Create(&TaskSummary{TaskID: "task-ended", ChatID: "chat-a", Text: "FINAL ENDED CONTEXT", Status: "completed", Final: true, UpdatedAt: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&TaskSummary{TaskID: "task-running", ChatID: "chat-a", Text: "RUNNING PREVIEW MUST NOT ENTER", Status: "completed", Final: false, UpdatedAt: 2}).Error; err != nil {
		t.Fatal(err)
	}
	p := &Plugin{store: s, ctx: context.Background()}
	config := SummaryConfig{ContextEnabled: true, EndedTaskSummaryCharBudget: 1000, SummaryCharBudget: 1000, TailTurns: 3, TailWordBudget: 1000, TailByteBudget: 10000}
	coverage, sections := p.buildTaskSections("chat-a", "task-a", "query", 100, 0, false, config)
	joined := strings.Join(sections, "\n\n")
	for _, expected := range []string{"FINAL ENDED CONTEXT", "summary turn-1", "raw turn-2", "raw turn-3", "raw turn-4"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("missing %q in:\n%s", expected, joined)
		}
	}
	for _, forbidden := range []string{"RUNNING PREVIEW MUST NOT ENTER", "summary turn-2", "summary turn-3", "summary turn-4", "raw turn-1"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("unexpected %q in:\n%s", forbidden, joined)
		}
	}
	if strings.Join(coverage.TaskSummaries, ",") != "task-ended" || strings.Join(coverage.Summarized, ",") != "turn-1" || strings.Join(coverage.Raw, ",") != "turn-2,turn-3,turn-4" {
		t.Fatalf("coverage mismatch: %#v", coverage)
	}
}
