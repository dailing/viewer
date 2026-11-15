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

func TestTaskRoleBridgeDoesNotReplayTheSameRolesLastTurn(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	chat := Chat{ID: "chat-a", Root: t.TempDir()}
	started, ended := int64(10), int64(20)
	if err := s.db.Create(&Turn{ID: "turn-a", ChatID: chat.ID, TaskID: "task-a", RoleID: "role-a", StartedAt: started, EndedAt: &ended}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&TurnSummary{TurnID: "turn-a", ChatID: chat.ID, TaskID: "task-a", RoleID: "role-a", Status: "completed", Summary: "same Role previous Turn", OccurredAt: 19}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.addMessage(&Message{ID: "message-a", ChatID: chat.ID, TaskID: "task-a", TurnID: "turn-a", Role: "assistant", Text: "same Role previous reply", CreatedAt: 19}); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{store: s, ctx: context.Background()}
	if bridge := p.buildTaskRoleBridge(chat, "task-a", "role-a", "next request", 30, "dispatch-next"); bridge != "" {
		t.Fatalf("same Role's completed Turn was replayed as missed work:\n%s", bridge)
	}

	otherEnded := int64(25)
	if err := s.db.Create(&Turn{ID: "turn-b", ChatID: chat.ID, TaskID: "task-a", RoleID: "role-b", StartedAt: 21, EndedAt: &otherEnded}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.addMessage(&Message{ID: "message-b", ChatID: chat.ID, TaskID: "task-a", TurnID: "turn-b", Role: "assistant", Text: "genuinely missed other Role reply", CreatedAt: 24}); err != nil {
		t.Fatal(err)
	}
	bridge := p.buildTaskRoleBridge(chat, "task-a", "role-a", "next request", 30, "dispatch-after-other-role")
	if !strings.Contains(bridge, "genuinely missed other Role reply") || strings.Contains(bridge, "same Role previous reply") {
		t.Fatalf("missed-work bridge has wrong coverage:\n%s", bridge)
	}
}

func TestSummaryPromptsRequireCompactOutcomesWithoutHandoffSections(t *testing.T) {
	assertCompact := func(name, prompt string) {
		t.Helper()
		for _, expected := range []string{"under 400 characters", "work that was accomplished", "resulting current state", "Do not include unresolved questions, next steps"} {
			if !strings.Contains(prompt, expected) {
				t.Errorf("%s prompt missing %q:\n%s", name, expected, prompt)
			}
		}
		for _, forbidden := range []string{"Write a summary with exactly these four sections", "State the goal, work already done", "all completed work, decisions made"} {
			if strings.Contains(prompt, forbidden) {
				t.Errorf("%s prompt retains handoff instruction %q:\n%s", name, forbidden, prompt)
			}
		}
	}
	assertCompact("running Task", taskSummaryPrompt("task material", false))
	assertCompact("final Task", taskSummaryPrompt("task material", true))

	var messages []map[string]string
	_, err := summarizeTranscript(context.Background(), func(_ context.Context, got []map[string]string, _ bool, _ int) (completionResult, error) {
		messages = got
		return completionResult{Content: "short", Model: "test"}, nil
	}, "turn transcript", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("Turn summary messages=%v", messages)
	}
	if !strings.Contains(messages[0]["content"], "under 400 characters") {
		t.Errorf("Turn system prompt has no compact length requirement: %s", messages[0]["content"])
	}
	assertCompact("Turn user", messages[1]["content"])
}

func TestEndedTaskSummarySectionUsesOnlyFiveNewest(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	ids := []string{"task-1", "task-2", "task-3", "task-4", "task-5", "task-6"}
	for index, id := range ids {
		if err := s.db.Create(&TaskSummary{TaskID: id, ChatID: "chat-a", Text: "SUMMARY " + id, Status: "completed", Final: true, UpdatedAt: int64(index + 1)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := &Plugin{store: s}
	coverage := &contextCoverage{}
	section := p.endedTaskSummarySection("chat-a", "current-task", 3000, coverage)
	if strings.Contains(section, "task-1") {
		t.Fatalf("sixth-newest summary entered context:\n%s", section)
	}
	for _, id := range ids[1:] {
		if !strings.Contains(section, "SUMMARY "+id) {
			t.Errorf("missing %s:\n%s", id, section)
		}
	}
	if got, want := strings.Join(coverage.TaskSummaries, ","), "task-6,task-5,task-4,task-3,task-2"; got != want {
		t.Fatalf("coverage=%s, want %s", got, want)
	}
}

func TestEndedTaskSummarySectionHonorsCharBudget(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	for index, id := range []string{"task-1", "task-2", "task-3", "task-4", "task-5"} {
		if err := s.db.Create(&TaskSummary{TaskID: id, ChatID: "chat-a", Text: strings.Repeat("x", 900), Status: "completed", Final: true, UpdatedAt: int64(index + 1)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := &Plugin{store: s}
	coverage := &contextCoverage{}
	section := p.endedTaskSummarySection("chat-a", "current-task", 2000, coverage)
	if got, want := strings.Join(coverage.TaskSummaries, ","), "task-5,task-4"; got != want {
		t.Fatalf("coverage=%s, want %s; section:\n%s", got, want, section)
	}
}

func TestSummaryConfigDefaultsEndedTaskBudgetTo3000(t *testing.T) {
	p := &Plugin{}
	if got := p.summaryConfig(context.Background()).EndedTaskSummaryCharBudget; got != 3000 {
		t.Fatalf("EndedTaskSummaryCharBudget=%d, want 3000", got)
	}
}
