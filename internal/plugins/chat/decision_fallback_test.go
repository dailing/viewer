package chat

import (
	"path/filepath"
	"testing"
)

func TestDetectDecisionCreatesTaskTitledFallbackWithoutMarker(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	now := nowMillis()
	task := &Task{ID: "task-a", ChatID: "chat-a", Title: "提交 Decision 路由与 UI 改造", CreatedAt: now, UpdatedAt: now}
	if err := s.createTask(task); err != nil {
		t.Fatal(err)
	}
	turn := &Turn{ID: "turn-a", ChatID: task.ChatID, TaskID: task.ID, RoleID: "role-a", DispatchID: "dispatch-a", StartedAt: now + 1}
	if err := s.db.Create(turn).Error; err != nil {
		t.Fatal(err)
	}
	for _, message := range []Message{
		{ID: "message-user", ChatID: task.ChatID, TaskID: task.ID, TurnID: turn.DispatchID, Role: "user", Text: "完成提交", SenderFrom: "user", CreatedAt: now},
		{ID: "message-assistant", ChatID: task.ChatID, TaskID: task.ID, TurnID: turn.ID, Role: "assistant", Text: "已提交全部改动。", SenderFrom: "role", RoleID: turn.RoleID, CreatedAt: now + 2},
	} {
		if err := s.addMessage(&message); err != nil {
			t.Fatal(err)
		}
	}

	p := &Plugin{store: s}
	p.detectDecision(turn)
	p.detectDecision(turn) // source_turn_id keeps fallback creation idempotent

	var decisions []Decision
	if err := s.db.Where("source_turn_id = ?", turn.ID).Find(&decisions).Error; err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 {
		t.Fatalf("fallback Decisions=%d, want 1", len(decisions))
	}
	decision := decisions[0]
	if decision.Status != "pending" || decision.Question != task.Title || decision.SourceMessageID != "message-assistant" {
		t.Fatalf("fallback Decision=%#v", decision)
	}
	if decision.MarkerStart != 0 || decision.MarkerEnd != 0 {
		t.Fatalf("plain assistant text must remain unchanged, offsets=%d,%d", decision.MarkerStart, decision.MarkerEnd)
	}
	options, err := parseDecisionOptions(decision.OptionsJSON)
	if err != nil || len(options) != 1 || options[0].ID != decisionEndTaskOptionID || options[0].Action != "end_current_task" {
		t.Fatalf("fallback options=%#v err=%v", options, err)
	}
	messages, err := p.decisionTurnMessages(&decision)
	if err != nil || len(messages) != 2 || messages[0].ID != "message-user" || messages[1].ID != "message-assistant" {
		t.Fatalf("fallback source messages=%#v err=%v", messages, err)
	}
	var blocks int64
	if err := s.db.Model(&MessageBlock{}).Where("turn_id = ? AND kind = ?", turn.ID, "decision").Count(&blocks).Error; err != nil || blocks != 1 {
		t.Fatalf("Decision blocks=%d err=%v", blocks, err)
	}
}

func TestDetectDecisionFallbackWithoutAssistantUsesDispatchUser(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	now := nowMillis()
	task := &Task{ID: "task-a", ChatID: "chat-a", Title: "处理失败的 Agent Turn", CreatedAt: now, UpdatedAt: now}
	if err := s.createTask(task); err != nil {
		t.Fatal(err)
	}
	turn := &Turn{ID: "turn-a", ChatID: task.ChatID, TaskID: task.ID, RoleID: "role-a", DispatchID: "dispatch-a", StartedAt: now + 1}
	if err := s.db.Create(turn).Error; err != nil {
		t.Fatal(err)
	}
	user := &Message{ID: "message-user", ChatID: task.ChatID, TaskID: task.ID, TurnID: turn.DispatchID, Role: "user", Text: "运行任务", SenderFrom: "user", CreatedAt: now}
	if err := s.addMessage(user); err != nil {
		t.Fatal(err)
	}

	(&Plugin{store: s}).detectDecision(turn)

	var decision Decision
	if err := s.db.Where("source_turn_id = ?", turn.ID).Limit(1).Find(&decision).Error; err != nil {
		t.Fatal(err)
	}
	if decision.Status != "pending" || decision.Question != task.Title || decision.SourceMessageID != user.ID {
		t.Fatalf("fallback without assistant=%#v", decision)
	}
	options, err := parseDecisionOptions(decision.OptionsJSON)
	if err != nil || len(options) != 1 || options[0].Action != "end_current_task" {
		t.Fatalf("fallback options=%#v err=%v", options, err)
	}
}

func TestDetectDecisionKeepsMalformedMarkerInvalid(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	now := nowMillis()
	task := &Task{ID: "task-a", ChatID: "chat-a", Title: "保留非法 marker 诊断", CreatedAt: now, UpdatedAt: now}
	if err := s.createTask(task); err != nil {
		t.Fatal(err)
	}
	turn := &Turn{ID: "turn-a", ChatID: task.ChatID, TaskID: task.ID, RoleID: "role-a", DispatchID: "dispatch-a", StartedAt: now + 1}
	if err := s.db.Create(turn).Error; err != nil {
		t.Fatal(err)
	}
	assistant := &Message{ID: "message-assistant", ChatID: task.ChatID, TaskID: task.ID, TurnID: turn.ID, Role: "assistant", Text: "<viewer-decision>\nversion: 1\n", SenderFrom: "role", RoleID: turn.RoleID, CreatedAt: now + 2}
	if err := s.addMessage(assistant); err != nil {
		t.Fatal(err)
	}

	(&Plugin{store: s}).detectDecision(turn)

	var decision Decision
	if err := s.db.Where("source_turn_id = ?", turn.ID).Limit(1).Find(&decision).Error; err != nil {
		t.Fatal(err)
	}
	if decision.Status != "invalid" || decision.ParseWarning == "" || decision.Question != "" {
		t.Fatalf("malformed marker Decision=%#v", decision)
	}
}
