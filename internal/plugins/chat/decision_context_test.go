package chat

import (
	"path/filepath"
	"testing"
)

func TestDecisionTurnMessagesIncludeOnlyVisibleSourceTurnContent(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	now := nowMillis()
	endedAt := now + 4
	task := &Task{ID: "task-a", ChatID: "chat-a", Title: "Task", CreatedAt: now, UpdatedAt: now}
	if err := s.createTask(task); err != nil {
		t.Fatal(err)
	}
	turn := &Turn{ID: "turn-a", ChatID: task.ChatID, TaskID: task.ID, RoleID: "role-a", RoleName: "Agent", DispatchID: "dispatch-a", StartedAt: now + 1, EndedAt: &endedAt}
	if err := s.db.Create(turn).Error; err != nil {
		t.Fatal(err)
	}
	decision := &Decision{ID: "decision-a", ChatID: task.ChatID, TaskID: task.ID, SourceTurnID: turn.ID, SourceMessageID: "message-assistant", RoleID: turn.RoleID, Version: 1, Kind: "choice", OptionsJSON: "[]", Status: "pending", CreatedAt: now + 4, UpdatedAt: now + 4}
	if err := s.db.Create(decision).Error; err != nil {
		t.Fatal(err)
	}
	for _, message := range []Message{
		{ID: "message-user", ChatID: task.ChatID, TaskID: task.ID, TurnID: turn.DispatchID, Role: "user", Text: "do the work", SenderFrom: "user", CreatedAt: now},
		{ID: "message-assistant", ChatID: task.ChatID, TaskID: task.ID, TurnID: turn.ID, Role: "assistant", Text: "visible result", SenderFrom: "role", RoleID: turn.RoleID, CreatedAt: now + 3},
		{ID: "message-other", ChatID: task.ChatID, TaskID: task.ID, TurnID: "turn-other", Role: "assistant", Text: "other turn", SenderFrom: "role", RoleID: turn.RoleID, CreatedAt: now + 2},
	} {
		if err := s.addMessage(&message); err != nil {
			t.Fatal(err)
		}
	}
	for _, block := range []MessageBlock{
		{ID: "tool-a", EventID: "event-tool", ChatID: task.ChatID, TaskID: task.ID, TurnID: turn.ID, Kind: "tool_call", Text: "private tool", OccurredAt: now + 2},
		{ID: "thinking-a", EventID: "event-thinking", ChatID: task.ChatID, TaskID: task.ID, TurnID: turn.ID, Kind: "thinking", Text: "private reasoning", OccurredAt: now + 2},
	} {
		if err := s.addMessageBlock(&block); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := (&Plugin{store: s}).decisionTurnMessages(decision)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].ID != "message-user" || messages[1].ID != "message-assistant" {
		t.Fatalf("visible source Turn messages=%#v", messages)
	}
}
