package chat

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type Chat struct {
	ID                       string `gorm:"primaryKey" json:"id"`
	Name                     string `json:"name"`
	Type                     string `json:"type"`
	Pinned                   bool   `json:"pinned"`
	Root                     string `json:"root"`
	CommonPrompt             string `json:"common_prompt"`
	MemberRoleIDsJSON        string `gorm:"column:member_role_ids" json:"-"`
	RoleRoutingOverridesJSON string `gorm:"column:role_routing_policy_overrides" json:"-"`
	CreatedAt                int64  `json:"created_at"`
	UpdatedAt                int64  `json:"updated_at"`
	// M6a columns remain mapped so AutoMigrate is non-destructive.
	Title             *string `json:"-"`
	Provider          string  `json:"-"`
	ProviderProfile   string  `json:"-"`
	ProviderSessionID string  `json:"-"`
	CWD               string  `json:"-"`
}

func (c Chat) payload() map[string]any {
	return map[string]any{
		"id": c.ID, "name": c.Name, "type": c.Type, "pinned": c.Pinned, "root": c.Root,
		"common_prompt": c.CommonPrompt, "member_role_ids": decodeStrings(c.MemberRoleIDsJSON),
		"role_routing_policy_overrides": decodeStringMap(c.RoleRoutingOverridesJSON),
		"created_at":                    c.CreatedAt, "updated_at": c.UpdatedAt,
	}
}

type RoleSession struct {
	ChatID            string `gorm:"primaryKey"`
	RoleID            string `gorm:"primaryKey"`
	Provider          string
	ProviderProfile   string
	ProviderSessionID string
	CWD               string
	UpdatedAt         int64
}

type PluginState struct {
	Key   string `gorm:"primaryKey"`
	Value string
}

type Message struct {
	ID         string `gorm:"primaryKey"`
	ChatID     string `gorm:"index;not null"`
	TurnID     string `gorm:"index;not null"`
	Role       string `gorm:"not null"`
	Text       string `gorm:"not null"`
	SenderFrom string
	RoleID     string `gorm:"index"`
	RoleName   string
	CreatedAt  int64
}

func (m Message) payload() map[string]any {
	sender := map[string]any{"from": m.SenderFrom}
	if m.RoleID != "" {
		sender["role_id"] = m.RoleID
	}
	if m.RoleName != "" {
		sender["role_name"] = m.RoleName
	}
	return map[string]any{
		"id": m.ID, "chat_id": m.ChatID, "turn_id": m.TurnID, "role": m.Role,
		"text": m.Text, "created_at": m.CreatedAt, "sender": sender,
	}
}

type Turn struct {
	ID         string `gorm:"primaryKey"`
	ChatID     string `gorm:"index;not null"`
	RoleID     string `gorm:"index"`
	RoleName   string
	StartedAt  int64
	EndedAt    *int64
	StopReason *string
}

type store struct{ db *gorm.DB }

func openStore(dataDir string) (*store, error) {
	path := filepath.Join(dataDir, "chat.sqlite3")
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", filepath.ToSlash(path))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open chat database: %w", err)
	}
	if err := db.AutoMigrate(&Chat{}, &RoleSession{}, &Message{}, &Turn{}, &PluginState{}); err != nil {
		return nil, fmt.Errorf("migrate chat database: %w", err)
	}
	return &store{db: db}, nil
}

func nowMillis() int64 { return time.Now().UnixMilli() }

func (s *store) chat(id string) (*Chat, error) {
	var value Chat
	result := s.db.Limit(1).Find(&value, "id = ?", id)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &value, nil
}

func (s *store) chats() ([]Chat, error) {
	var values []Chat
	err := s.db.Order("pinned desc, updated_at desc, created_at desc").Find(&values).Error
	return values, err
}

func (s *store) saveChat(value *Chat) error { return s.db.Save(value).Error }

func (s *store) deleteChat(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		for _, model := range []any{&Message{}, &Turn{}, &RoleSession{}} {
			if err := tx.Where("chat_id = ?", id).Delete(model).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&Chat{}, "id = ?", id).Error
	})
}

func (s *store) roleSession(chatID, roleID string) (*RoleSession, error) {
	var value RoleSession
	result := s.db.Limit(1).Find(&value, "chat_id = ? AND role_id = ?", chatID, roleID)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &value, nil
}

func (s *store) saveRoleSession(value *RoleSession) error { return s.db.Save(value).Error }

func (s *store) activeChatID() (string, error) {
	var value PluginState
	result := s.db.Limit(1).Find(&value, "key = ?", "active_chat_id")
	if result.Error != nil || result.RowsAffected == 0 {
		return "", result.Error
	}
	return value.Value, nil
}

func (s *store) setActiveChatID(id string) error {
	return s.db.Save(&PluginState{Key: "active_chat_id", Value: id}).Error
}

func (s *store) beginTurn(turn *Turn) error        { return s.db.Create(turn).Error }
func (s *store) addMessage(message *Message) error { return s.db.Create(message).Error }

func (s *store) message(id string) (*Message, error) {
	var value Message
	result := s.db.Limit(1).Find(&value, "id = ?", id)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &value, nil
}

func (s *store) completeTurn(id, reason string) error {
	ended := nowMillis()
	return s.db.Model(&Turn{}).Where("id = ?", id).Updates(map[string]any{"ended_at": ended, "stop_reason": reason}).Error
}

func (s *store) history(chatID string, before int64, wordBudget int) ([]Message, error) {
	var values []Message
	query := s.db.Where("chat_id = ?", chatID)
	if before > 0 {
		query = query.Where("created_at < ?", before)
	}
	if err := query.Order("created_at desc").Limit(1000).Find(&values).Error; err != nil {
		return nil, err
	}
	used, start := 0, len(values)
	for i, value := range values {
		words := len(splitWords(value.Text))
		if wordBudget > 0 && used+words > wordBudget && start < len(values) {
			break
		}
		used += words
		start = i
	}
	if start == len(values) {
		return nil, nil
	}
	selected := append([]Message(nil), values[:start+1]...)
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	return selected, nil
}

func (s *store) close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func encodeJSON(value any) string { data, _ := json.Marshal(value); return string(data) }
func decodeStrings(raw string) []string {
	var value []string
	if json.Unmarshal([]byte(raw), &value) != nil {
		return []string{}
	}
	return value
}
func decodeStringMap(raw string) map[string]string {
	var value map[string]string
	if json.Unmarshal([]byte(raw), &value) != nil || value == nil {
		return map[string]string{}
	}
	return value
}
func splitWords(value string) []string { return strings.Fields(value) }
