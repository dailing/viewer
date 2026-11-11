package chat

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type chatOrderLegacyRow struct {
	ID        string `gorm:"primaryKey"`
	Pinned    bool
	CreatedAt int64
	UpdatedAt int64
}

func (chatOrderLegacyRow) TableName() string { return "chats" }

func openChatSchemaFixture(path string) (*gorm.DB, error) {
	return gorm.Open(sqlite.Open(path), &gorm.Config{})
}

func chatIDs(chats []Chat) []string {
	ids := make([]string, 0, len(chats))
	for _, chat := range chats {
		ids = append(ids, chat.ID)
	}
	return ids
}

func TestChatOrderInitializesLegacyRowsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.sqlite3")
	legacy, err := openChatSchemaFixture(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.AutoMigrate(&chatOrderLegacyRow{}, &PluginState{}); err != nil {
		t.Fatal(err)
	}
	for _, chat := range []chatOrderLegacyRow{
		{ID: "older", Pinned: false, CreatedAt: 1, UpdatedAt: 20},
		{ID: "pinned", Pinned: true, CreatedAt: 2, UpdatedAt: 10},
		{ID: "newer", Pinned: false, CreatedAt: 3, UpdatedAt: 30},
	} {
		if err := legacy.Create(&chat).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.Create(&PluginState{Key: "chat_schema_version", Value: chatSchemaVersion}).Error; err != nil {
		t.Fatal(err)
	}
	if err := closeGORM(legacy); err != nil {
		t.Fatal(err)
	}

	s, err := openStorePath(path)
	if err != nil {
		t.Fatalf("open schema without chats.position: %v", err)
	}
	defer s.close()
	chats, err := s.chats()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := chatIDs(chats), []string{"pinned", "newer", "older"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("initialized order=%v, want %v", got, want)
	}
	for index, chat := range chats {
		if chat.Position != index+1 {
			t.Fatalf("Chat %s position=%d, want %d", chat.ID, chat.Position, index+1)
		}
	}
}

func TestChatOrderAppendsAndPersistsManualReorder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.sqlite3")
	s, err := openStorePath(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		chat := &Chat{ID: id, Name: id, Root: "/tmp"}
		task := &Task{ID: "task-" + id, ChatID: id, Title: "主线"}
		if err := s.createChatWithDefaultTask(chat, task); err != nil {
			t.Fatal(err)
		}
	}
	chats, err := s.chats()
	if err != nil || !reflect.DeepEqual(chatIDs(chats), []string{"a", "b", "c"}) {
		t.Fatalf("append order=%v err=%v", chatIDs(chats), err)
	}
	chat, err := s.chat("a")
	if err != nil {
		t.Fatal(err)
	}
	chat.Pinned = true
	chat.UpdatedAt = 999
	if err := s.saveChat(chat); err != nil {
		t.Fatal(err)
	}
	reordered, err := s.reorderChats([]string{"c", "a", "b"})
	if err != nil || !reflect.DeepEqual(chatIDs(reordered), []string{"c", "a", "b"}) {
		t.Fatalf("reordered=%v err=%v", chatIDs(reordered), err)
	}
	if _, err := s.reorderChats([]string{"c", "c", "a"}); err == nil {
		t.Fatal("duplicate Chat IDs must be rejected")
	}
	if _, err := s.reorderChats([]string{"c", "missing", "a"}); err == nil {
		t.Fatal("unknown Chat IDs must be rejected")
	}
	newChat := &Chat{ID: "d", Name: "d", Root: "/tmp"}
	if err := s.createChatWithDefaultTask(newChat, &Task{ID: "task-d", ChatID: "d", Title: "主线"}); err != nil {
		t.Fatal(err)
	}
	if newChat.Position != 4 {
		t.Fatalf("new Chat position=%d, want 4", newChat.Position)
	}
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStorePath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	chats, err = s.chats()
	if err != nil || !reflect.DeepEqual(chatIDs(chats), []string{"c", "a", "b", "d"}) {
		t.Fatalf("persisted order=%v err=%v", chatIDs(chats), err)
	}
}
