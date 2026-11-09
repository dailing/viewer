package chat

// This file contains the deliberately standalone, one-shot upgrade from the
// retired Branch/DAG database. The normal runtime never calls it and never
// opens a legacy database. Run MigrateChatLevelSettings only with Viewer
// stopped, after the Task-first implementation has been fully verified.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type legacyChatSettings struct {
	ID                       string `gorm:"column:id"`
	Name                     string `gorm:"column:name"`
	Type                     string `gorm:"column:type"`
	Pinned                   bool   `gorm:"column:pinned"`
	Root                     string `gorm:"column:root"`
	CommonPrompt             string `gorm:"column:common_prompt"`
	MemberRoleIDsJSON        string `gorm:"column:member_role_ids"`
	RoleRoutingOverridesJSON string `gorm:"column:role_routing_policy_overrides"`
	CreatedAt                int64  `gorm:"column:created_at"`
	UpdatedAt                int64  `gorm:"column:updated_at"`
}

func (legacyChatSettings) TableName() string { return "chats" }

// MigrateChatLevelSettings copies only Chat/Role/routing settings into a new
// schema. It never reads old messages, branches, sessions, summaries or Loop
// state. The source is opened read-only; a consistent offline file backup is
// left beside it before the new database atomically replaces chat.sqlite3.
func MigrateChatLevelSettings(dataDir string) (backupPath string, err error) {
	sourcePath := filepath.Join(dataDir, "chat.sqlite3")
	backupPath = filepath.Join(dataDir, "chat.pre-task-first.sqlite3")
	if _, err := os.Stat(sourcePath); err != nil {
		return "", err
	}
	if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return "", fmt.Errorf("migration backup already exists: %s", backupPath)
		}
		return "", err
	}
	temporaryPath := filepath.Join(dataDir, ".chat.task-first-"+newID()+".sqlite3")
	defer os.Remove(temporaryPath)
	if err := migrateChatLevelSettings(sourcePath, temporaryPath); err != nil {
		return "", err
	}
	if err := copyFileAtomic(sourcePath, backupPath); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, sourcePath); err != nil {
		return "", fmt.Errorf("replace chat database: %w", err)
	}
	return backupPath, nil
}

func migrateChatLevelSettings(sourcePath, destinationPath string) (err error) {
	if sourcePath == destinationPath {
		return errors.New("migration source and destination must differ")
	}
	if _, statErr := os.Stat(destinationPath); !errors.Is(statErr, os.ErrNotExist) {
		if statErr == nil {
			return errors.New("migration destination already exists")
		}
		return statErr
	}
	legacy, err := gorm.Open(sqlite.Open("file:"+filepath.ToSlash(sourcePath)+"?mode=ro"), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("open legacy settings read-only: %w", err)
	}
	defer func() { err = errors.Join(err, closeGORM(legacy)) }()
	if !legacy.Migrator().HasTable("chats") {
		return errors.New("legacy chats table not found")
	}

	var legacyChats []legacyChatSettings
	if err := legacy.Raw(`SELECT id, name, type, pinned, root, common_prompt,
		member_role_ids, role_routing_policy_overrides, created_at, updated_at FROM chats`).Scan(&legacyChats).Error; err != nil {
		return fmt.Errorf("read legacy chats: %w", err)
	}
	var roles []SuperRole
	if legacy.Migrator().HasTable("roles") {
		if err := legacy.Raw(`SELECT id, name, description, prompt, cwd, routing_policy_id,
			session_policy, context_recycle_percent, context_recycle_tokens, created_at, updated_at FROM roles`).Scan(&roles).Error; err != nil {
			return fmt.Errorf("read legacy roles: %w", err)
		}
	}
	var policies []RoutingPolicyRow
	if legacy.Migrator().HasTable("routing_policies") {
		if err := legacy.Raw(`SELECT id, name, candidates_json, auto_failover, max_attempts,
			created_at, updated_at FROM routing_policies`).Scan(&policies).Error; err != nil {
			return fmt.Errorf("read legacy routing policies: %w", err)
		}
	}
	settings := map[string]string{}
	if legacy.Migrator().HasTable(&PluginState{}) {
		var rows []PluginState
		if err := legacy.Raw(`SELECT key, value FROM plugin_states WHERE key IN ?`, []string{"active_chat_id", "default_routing_policy_id"}).Scan(&rows).Error; err != nil {
			return fmt.Errorf("read legacy plugin settings: %w", err)
		}
		for _, row := range rows {
			settings[row.Key] = row.Value
		}
	}

	fresh, err := openStorePath(destinationPath)
	if err != nil {
		return fmt.Errorf("create fresh Task schema: %w", err)
	}
	defer func() { err = errors.Join(err, fresh.close()) }()
	validChats := map[string]bool{}
	err = fresh.db.Transaction(func(tx *gorm.DB) error {
		for _, old := range legacyChats {
			if old.ID == "" {
				return errors.New("legacy chat with empty id")
			}
			validChats[old.ID] = true
			value := Chat{ID: old.ID, Name: old.Name, Type: old.Type, Pinned: old.Pinned, Root: old.Root,
				CommonPrompt: old.CommonPrompt, MemberRoleIDsJSON: old.MemberRoleIDsJSON,
				RoleRoutingOverridesJSON: old.RoleRoutingOverridesJSON, CreatedAt: old.CreatedAt, UpdatedAt: old.UpdatedAt}
			if err := tx.Create(&value).Error; err != nil {
				return err
			}
			task := &Task{ID: newID(), ChatID: old.ID, Title: "主线", TitleSource: "default",
				CreatedBy: "chat-level-migration", Pinned: old.Pinned, CreatedAt: nowMillis(), UpdatedAt: nowMillis()}
			if err := tx.Create(task).Error; err != nil {
				return err
			}
		}
		for index := range roles {
			if err := tx.Create(&roles[index]).Error; err != nil {
				return err
			}
		}
		for index := range policies {
			if err := tx.Create(&policies[index]).Error; err != nil {
				return err
			}
		}
		if value := settings["default_routing_policy_id"]; value != "" {
			if err := tx.Save(&PluginState{Key: "default_routing_policy_id", Value: value}).Error; err != nil {
				return err
			}
		}
		if value := settings["active_chat_id"]; validChats[value] {
			if err := tx.Save(&PluginState{Key: "active_chat_id", Value: value}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return validateMigratedChatSettings(fresh, len(legacyChats))
}

func validateMigratedChatSettings(s *store, chatCount int) error {
	chats, err := s.chats()
	if err != nil {
		return err
	}
	if len(chats) != chatCount {
		return fmt.Errorf("migrated chat count=%d, want %d", len(chats), chatCount)
	}
	for _, chat := range chats {
		tasks, err := s.chatTasks(chat.ID)
		if err != nil {
			return err
		}
		if len(tasks) != 1 || tasks[0].TitleSource != "default" || tasks[0].ended() || tasks[0].Pinned != chat.Pinned {
			return fmt.Errorf("chat %s does not have exactly one default open Task", chat.ID)
		}
	}
	for _, model := range []any{&Message{}, &Turn{}, &TurnEvent{}, &MessageBlock{}, &TurnSummary{}, &RoleSession{}, &Draft{}, &OperationReceipt{}, &Decision{}, &ContextBuild{}} {
		var count int64
		if err := s.db.Model(model).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("migrated content table %T is not empty", model)
		}
	}
	var taskCount, summaryCount int64
	if err := s.db.Model(&Task{}).Count(&taskCount).Error; err != nil {
		return err
	}
	if err := s.db.Model(&TaskSummary{}).Count(&summaryCount).Error; err != nil {
		return err
	}
	if taskCount != int64(chatCount) || summaryCount != 0 {
		return fmt.Errorf("unexpected task/summary counts: %d/%d", taskCount, summaryCount)
	}
	for _, table := range []string{"history_nodes", "history_edges", "line_heads", "automation_leases", "automation_gates", "dispatch_receipts"} {
		if s.db.Migrator().HasTable(table) {
			return fmt.Errorf("retired table was created: %s", table)
		}
	}
	return nil
}

func copyFileAtomic(sourcePath, destinationPath string) (err error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	temporary := destinationPath + ".tmp-" + newID()
	defer os.Remove(temporary)
	destination, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, destination.Close())
		}
	}()
	if _, err = io.Copy(destination, source); err != nil {
		return err
	}
	if err = destination.Sync(); err != nil {
		return err
	}
	if err = destination.Close(); err != nil {
		return err
	}
	closed = true
	return os.Rename(temporary, destinationPath)
}
