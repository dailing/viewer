package chat

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type Chat struct {
	ID                string `gorm:"primaryKey"`
	CreatedAt         int64
	Title             *string
	Provider          string
	ProviderProfile   string
	ProviderSessionID string
	CWD               string
}

type Message struct {
	ID        string `gorm:"primaryKey"`
	ChatID    string `gorm:"index;not null"`
	TurnID    string `gorm:"index;not null"`
	Role      string `gorm:"not null"`
	Text      string `gorm:"not null"`
	CreatedAt int64
}

type Turn struct {
	ID         string `gorm:"primaryKey"`
	ChatID     string `gorm:"index;not null"`
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
	if err := db.AutoMigrate(&Chat{}, &Message{}, &Turn{}); err != nil {
		return nil, fmt.Errorf("migrate chat database: %w", err)
	}
	return &store{db: db}, nil
}

func nowMillis() int64 { return time.Now().UnixMilli() }

func (s *store) chat(id string) (*Chat, error) {
	var chat Chat
	result := s.db.Limit(1).Find(&chat, "id = ?", id)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &chat, nil
}

func (s *store) saveChat(chat *Chat) error { return s.db.Save(chat).Error }

func (s *store) beginTurn(chat *Chat, turn *Turn, message *Message) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(chat).Error; err != nil {
			return err
		}
		if err := tx.Create(turn).Error; err != nil {
			return err
		}
		return tx.Create(message).Error
	})
}

func (s *store) addMessage(message *Message) error { return s.db.Create(message).Error }

func (s *store) completeTurn(id, reason string) error {
	ended := nowMillis()
	return s.db.Model(&Turn{}).Where("id = ?", id).Updates(map[string]any{"ended_at": ended, "stop_reason": reason}).Error
}

func (s *store) close() error {
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}
