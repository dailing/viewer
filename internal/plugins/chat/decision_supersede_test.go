package chat

import (
	"path/filepath"
	"testing"
)

func TestDetectDecisionSupersedesOnlyPendingInSameTask(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	now := nowMillis()
	task := &Task{ID: "task-a", ChatID: "chat-a", Title: "后台完成后只保留最新 Decision", CreatedAt: now, UpdatedAt: now}
	otherTask := &Task{ID: "task-b", ChatID: "chat-a", Title: "其他 Task", CreatedAt: now, UpdatedAt: now}
	if err := s.createTask(task); err != nil {
		t.Fatal(err)
	}
	if err := s.createTask(otherTask); err != nil {
		t.Fatal(err)
	}
	for _, item := range []Decision{
		{ID: "decision-pending", ChatID: task.ChatID, TaskID: task.ID, SourceTurnID: "turn-pending", RoleID: "role-a", Version: 2, Kind: "choice", OptionsJSON: "[]", Status: "pending", CreatedAt: now - 4, UpdatedAt: now - 4},
		{ID: "decision-dispatching", ChatID: task.ChatID, TaskID: task.ID, SourceTurnID: "turn-dispatching", RoleID: "role-a", Version: 3, Kind: "choice", OptionsJSON: "[]", Status: "dispatching", CreatedAt: now - 3, UpdatedAt: now - 3},
		{ID: "decision-resolved", ChatID: task.ChatID, TaskID: task.ID, SourceTurnID: "turn-resolved", RoleID: "role-a", Version: 2, Kind: "choice", OptionsJSON: "[]", Status: "resolved", CreatedAt: now - 2, UpdatedAt: now - 2},
		{ID: "decision-other-task", ChatID: otherTask.ChatID, TaskID: otherTask.ID, SourceTurnID: "turn-other-task", RoleID: "role-a", Version: 4, Kind: "choice", OptionsJSON: "[]", Status: "pending", CreatedAt: now - 1, UpdatedAt: now - 1},
	} {
		item := item
		if err := s.db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}
	turn := &Turn{ID: "turn-new", ChatID: task.ChatID, TaskID: task.ID, RoleID: "role-a", DispatchID: "dispatch-new", StartedAt: now + 1}
	if err := s.db.Create(turn).Error; err != nil {
		t.Fatal(err)
	}
	for _, message := range []Message{
		{ID: "message-user", ChatID: task.ChatID, TaskID: task.ID, TurnID: turn.DispatchID, Role: "user", Text: "后台任务已完成", SenderFrom: "user", CreatedAt: now},
		{ID: "message-assistant", ChatID: task.ChatID, TaskID: task.ID, TurnID: turn.ID, Role: "assistant", Text: "后台结果已处理。\n<viewer-decision>\nversion: 1\nkind: choice\nquestion: 选择后台结果后的下一步\noption: 继续\n</viewer-decision>", SenderFrom: "role", RoleID: turn.RoleID, CreatedAt: now + 2},
	} {
		if err := s.addMessage(&message); err != nil {
			t.Fatal(err)
		}
	}

	p := &Plugin{store: s}
	p.detectDecision(turn)
	p.detectDecision(turn) // A repeated turn-ended notification must not cancel the new Decision or bump old Versions again.

	current, err := p.decision("decision-pending")
	if err != nil || current.Status != "cancelled" || current.Version != 3 || current.UpdatedAt <= now-4 {
		t.Fatalf("superseded pending Decision=%#v err=%v", current, err)
	}
	dispatching, err := p.decision("decision-dispatching")
	if err != nil || dispatching.Status != "dispatching" || dispatching.Version != 3 || dispatching.UpdatedAt != now-3 {
		t.Fatalf("dispatching Decision changed=%#v err=%v", dispatching, err)
	}
	resolved, err := p.decision("decision-resolved")
	if err != nil || resolved.Status != "resolved" || resolved.Version != 2 {
		t.Fatalf("resolved Decision changed=%#v err=%v", resolved, err)
	}
	other, err := p.decision("decision-other-task")
	if err != nil || other.Status != "pending" || other.Version != 4 || other.UpdatedAt != now-1 {
		t.Fatalf("other Task Decision changed=%#v err=%v", other, err)
	}
	var latest Decision
	if err := s.db.Where("source_turn_id = ?", turn.ID).Limit(1).Find(&latest).Error; err != nil {
		t.Fatal(err)
	}
	if latest.Status != "pending" || latest.Version != 1 {
		t.Fatalf("latest Decision=%#v", latest)
	}
	var pending int64
	if err := s.db.Model(&Decision{}).Where("task_id = ? AND status = ?", task.ID, "pending").Count(&pending).Error; err != nil || pending != 1 {
		t.Fatalf("same-Task pending Decisions=%d err=%v", pending, err)
	}
}
