package chat

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestTaskSchemaStartsCleanAndPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.sqlite3")
	s, err := openStorePath(path)
	if err != nil {
		t.Fatal(err)
	}
	now := nowMillis()
	chat := &Chat{ID: "chat-a", Name: "Project", Type: "project", Root: "/tmp/project", CreatedAt: now, UpdatedAt: now}
	if err := s.saveChat(chat); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureDefaultTasks(); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.chatTasks(chat.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("default tasks=%v err=%v", tasks, err)
	}
	if tasks[0].Title != "主线" || tasks[0].TitleSource != "default" || tasks[0].ended() {
		t.Fatalf("unexpected default task: %#v", tasks[0])
	}
	duplicate := &Task{ID: "task-duplicate", ChatID: chat.ID, Title: tasks[0].Title, TitleSource: "manual", CreatedBy: "test", CreatedAt: now, UpdatedAt: now}
	if err := s.createTask(duplicate); err != nil {
		t.Fatalf("duplicate titles must be allowed: %v", err)
	}
	if err := s.addMessage(&Message{ID: "message-a", ChatID: chat.ID, TaskID: tasks[0].ID, TurnID: "dispatch-a", Role: "user", Text: "kept", SenderFrom: "user", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.close(); err != nil {
		t.Fatal(err)
	}

	s, err = openStorePath(path)
	if err != nil {
		t.Fatalf("new schema must reopen without reset: %v", err)
	}
	defer s.close()
	message, err := s.message("message-a")
	if err != nil || message == nil || message.Text != "kept" {
		t.Fatalf("message after reopen=%#v err=%v", message, err)
	}
	tasks, err = s.chatTasks(chat.ID)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("tasks were recreated or lost: %v err=%v", tasks, err)
	}
	for _, check := range []struct {
		model any
		name  string
	}{
		{&MessageBlock{}, "idx_message_blocks_task_occurred"},
		{&Message{}, "idx_messages_task_cursor"},
		{&Turn{}, "idx_turns_task_started"},
		{&Decision{}, "idx_decisions_status_cursor"},
	} {
		if !s.db.Migrator().HasIndex(check.model, check.name) {
			t.Errorf("ORM-managed index %s is missing", check.name)
		}
	}
}

func TestTaskStoreRejectsLegacySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite3")
	legacy, err := openChatSchemaFixture(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.AutoMigrate(&chatOrderLegacyRow{}); err != nil {
		t.Fatal(err)
	}
	if err := closeGORM(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := openStorePath(path); !errors.Is(err, ErrLegacyChatSchema) {
		t.Fatalf("open legacy schema err=%v, want ErrLegacyChatSchema", err)
	}
}

func TestEndedTaskAcceptsDraftAndExplicitSendReopens(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	now := nowMillis()
	chat := &Chat{ID: "chat-a", Name: "Project", Type: "project", Root: "/tmp/project", CreatedAt: now, UpdatedAt: now}
	if err := s.saveChat(chat); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureDefaultTasks(); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.chatTasks(chat.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%v err=%v", tasks, err)
	}
	task := tasks[0]
	task.EndedAt = &now
	if err := s.saveTask(&task); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&TaskSummary{TaskID: task.ID, ChatID: chat.ID, Text: "historical", Status: "completed", Final: true, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	p := &Plugin{store: s}
	if err := p.draftLine(chat.ID, task.ID); err != nil {
		t.Fatalf("ended Task must retain an unsent draft: %v", err)
	}
	if _, err := p.resolveTask(*chat, task.ID); !errors.Is(err, errTaskEnded) {
		t.Fatalf("ordinary open-only operation err=%v, want errTaskEnded", err)
	}
	if _, err := p.resolveDispatchTask(*chat, task.ID, false); !errors.Is(err, errTaskEnded) {
		t.Fatalf("dispatch without explicit continuation err=%v, want errTaskEnded", err)
	}
	reopened, err := p.resolveDispatchTask(*chat, task.ID, true)
	if err != nil || reopened.ended() {
		t.Fatalf("explicit continuation task=%#v err=%v", reopened, err)
	}
	var summary TaskSummary
	result := s.db.Limit(1).Find(&summary, "task_id = ?", task.ID)
	if result.Error != nil || result.RowsAffected != 1 || summary.Final || summary.Text != "historical" {
		t.Fatalf("summary=%#v rows=%d err=%v", summary, result.RowsAffected, result.Error)
	}
}

func TestTaskScopedKeysAndContent(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	for _, id := range []string{"task-a", "task-b"} {
		if err := s.createTask(&Task{ID: id, ChatID: "chat-a", Title: "same", TitleSource: "manual", CreatedBy: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.saveRoleSession(&RoleSession{ChatID: "chat-a", TaskID: "task-a", RoleID: "role-a", ProviderSessionID: "session-a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.saveRoleSession(&RoleSession{ChatID: "chat-a", TaskID: "task-b", RoleID: "role-a", ProviderSessionID: "session-b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.saveDraft(&Draft{ChatID: "chat-a", InputInstanceID: "view-a", TaskID: "task-a", Text: "A"}); err != nil {
		t.Fatal(err)
	}
	if err := s.saveDraft(&Draft{ChatID: "chat-a", InputInstanceID: "view-a", TaskID: "task-b", Text: "B"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ task, session, draft string }{{"task-a", "session-a", "A"}, {"task-b", "session-b", "B"}} {
		session, err := s.roleSession("chat-a", tc.task, "role-a")
		if err != nil || session == nil || session.ProviderSessionID != tc.session {
			t.Fatalf("session %s=%#v err=%v", tc.task, session, err)
		}
		draft, err := s.draft("view-a", tc.task)
		if err != nil || draft == nil || draft.Text != tc.draft {
			t.Fatalf("draft %s=%#v err=%v", tc.task, draft, err)
		}
	}
}
