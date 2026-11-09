package chat

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openLegacyFixture(path string) (*gorm.DB, error) {
	return gorm.Open(sqlite.Open("file:"+filepath.ToSlash(path)), &gorm.Config{})
}

func TestChatLevelMigrationCopiesOnlySettings(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy.sqlite3")
	destination := filepath.Join(dir, "fresh.sqlite3")
	legacy, err := openLegacyFixture(source)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE chats (id TEXT PRIMARY KEY, name TEXT, type TEXT, pinned BOOLEAN, root TEXT, common_prompt TEXT, member_role_ids TEXT, role_routing_policy_overrides TEXT, provider TEXT, provider_session_id TEXT, created_at INTEGER, updated_at INTEGER)`,
		`CREATE TABLE roles (id TEXT PRIMARY KEY, name TEXT, description TEXT, prompt TEXT, cwd TEXT, routing_policy_id TEXT, session_policy TEXT, context_recycle_percent REAL, context_recycle_tokens INTEGER, created_at INTEGER, updated_at INTEGER)`,
		`CREATE TABLE routing_policies (id TEXT PRIMARY KEY, name TEXT, candidates_json TEXT, auto_failover BOOLEAN, max_attempts INTEGER, created_at INTEGER, updated_at INTEGER)`,
		`CREATE TABLE plugin_states (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE branches (id TEXT PRIMARY KEY, chat_id TEXT, name TEXT)`,
		`CREATE TABLE messages (id TEXT PRIMARY KEY, chat_id TEXT, branch_id TEXT, text TEXT)`,
		`INSERT INTO chats VALUES ('chat-a','Project','project',1,'/srv/project','project prompt','["role-a"]','{"role-a":"policy-a"}','legacy-provider','legacy-session',10,20)`,
		`INSERT INTO roles VALUES ('role-a','Researcher','research','role prompt','/srv/project','policy-a','reuse',80,100000,11,21)`,
		`INSERT INTO routing_policies VALUES ('policy-a','Default','[{"agent":"hermes","provider":"kimi","model":"k3"}]',1,2,12,22)`,
		`INSERT INTO plugin_states VALUES ('active_chat_id','chat-a')`,
		`INSERT INTO plugin_states VALUES ('default_routing_policy_id','policy-a')`,
		`INSERT INTO branches VALUES ('old-branch','chat-a','old')`,
		`INSERT INTO messages VALUES ('old-message','chat-a','old-branch','must not migrate')`,
	}
	for _, statement := range statements {
		if err := legacy.Exec(statement).Error; err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := closeGORM(legacy); err != nil {
		t.Fatal(err)
	}
	bankBefore := hindsightBankID("bank", "chat-a")
	if err := migrateChatLevelSettings(source, destination); err != nil {
		t.Fatal(err)
	}
	if hindsightBankID("bank", "chat-a") != bankBefore {
		t.Fatal("Chat-level migration changed the Hindsight bank ID")
	}
	fresh, err := openStorePath(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.close()
	chat, err := fresh.chat("chat-a")
	if err != nil || chat == nil {
		t.Fatalf("chat=%#v err=%v", chat, err)
	}
	if chat.Name != "Project" || chat.Root != "/srv/project" || chat.CommonPrompt != "project prompt" || chat.CreatedAt != 10 || chat.UpdatedAt != 20 {
		t.Fatalf("chat settings were not preserved: %#v", chat)
	}
	if got := decodeStrings(chat.MemberRoleIDsJSON); len(got) != 1 || got[0] != "role-a" {
		t.Fatalf("member roles=%v", got)
	}
	tasks, err := fresh.chatTasks(chat.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%v err=%v", tasks, err)
	}
	if tasks[0].ID == "old-branch" || tasks[0].Title != "主线" || !tasks[0].Pinned || tasks[0].ended() {
		t.Fatalf("default task=%#v", tasks[0])
	}
	roles, err := fresh.roles()
	if err != nil || len(roles) != 1 || roles[0].Name != "Researcher" || roles[0].CWD != "/srv/project" {
		t.Fatalf("roles=%v err=%v", roles, err)
	}
	policies, err := fresh.routingPolicies()
	if err != nil || len(policies) != 1 || policies[0].ID != "policy-a" || len(policies[0].Candidates) != 1 {
		t.Fatalf("policies=%v err=%v", policies, err)
	}
	active, err := fresh.activeChatID()
	if err != nil || active != "chat-a" {
		t.Fatalf("active=%q err=%v", active, err)
	}
	for _, model := range []any{&Message{}, &Turn{}, &TurnEvent{}, &MessageBlock{}, &TurnSummary{}, &TaskSummary{}, &RoleSession{}, &Draft{}, &OperationReceipt{}, &Decision{}, &ContextBuild{}} {
		var count int64
		if err := fresh.db.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("content %T count=%d err=%v", model, count, err)
		}
	}
	for _, table := range []string{"branches", "history_nodes", "history_edges", "line_heads", "automation_leases", "automation_gates", "dispatch_receipts"} {
		if fresh.db.Migrator().HasTable(table) {
			t.Fatalf("legacy/DAG table exists in new schema: %s", table)
		}
	}
}

func TestChatLevelMigrationRejectsInvalidActiveChat(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy.sqlite3")
	legacy, err := openLegacyFixture(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE chats (id TEXT PRIMARY KEY, name TEXT, type TEXT, pinned BOOLEAN, root TEXT, common_prompt TEXT, member_role_ids TEXT, role_routing_policy_overrides TEXT, created_at INTEGER, updated_at INTEGER)`,
		`CREATE TABLE plugin_states (key TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO chats VALUES ('chat-a','Project','project',0,'','','[]','{}',1,2)`,
		`INSERT INTO plugin_states VALUES ('active_chat_id','missing')`,
	} {
		if err := legacy.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := closeGORM(legacy); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "fresh.sqlite3")
	if err := migrateChatLevelSettings(source, destination); err != nil {
		t.Fatal(err)
	}
	fresh, err := openStorePath(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.close()
	active, err := fresh.activeChatID()
	if err != nil || active != "" {
		t.Fatalf("invalid active chat was copied: %q err=%v", active, err)
	}
}
