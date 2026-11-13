package chat

import (
	"path/filepath"
	"testing"
)

func TestResumeSessionForRoleHonorsPolicyAndExplicitSource(t *testing.T) {
	store, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	now := nowMillis()
	task := &Task{ID: "task-a", ChatID: "chat-a", Title: "Task", CreatedAt: now, UpdatedAt: now}
	if err := store.createTask(task); err != nil {
		t.Fatal(err)
	}
	turn := &Turn{ID: "turn-a", ChatID: task.ChatID, TaskID: task.ID, RoleID: "role-a", RoleName: "Role", StartedAt: now}
	if err := store.beginTurn(turn); err != nil {
		t.Fatal(err)
	}
	if err := store.setTurnSession(turn.ID, "latest-session"); err != nil {
		t.Fatal(err)
	}
	plugin := &Plugin{store: store}
	role := SuperRole{ID: "role-a", SessionPolicy: "reuse"}
	if got := plugin.resumeSessionForRole(task.ChatID, task.ID, "", false, role); got != "latest-session" {
		t.Fatalf("reuse selected %q", got)
	}
	if got := plugin.resumeSessionForRole(task.ChatID, task.ID, "", true, role); got != "" {
		t.Fatalf("force-new selected %q", got)
	}
	role.SessionPolicy = "new_each_run"
	if got := plugin.resumeSessionForRole(task.ChatID, task.ID, "", false, role); got != "" {
		t.Fatalf("new_each_run selected %q", got)
	}
	if got := plugin.resumeSessionForRole(task.ChatID, task.ID, "source-session", false, role); got != "source-session" {
		t.Fatalf("explicit source selected %q", got)
	}
}
