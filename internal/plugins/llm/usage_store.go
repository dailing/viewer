package llm

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"viewer/internal/agentdriver"
)

type LLMCall struct {
	ID             string `gorm:"primaryKey"`
	Transport      string `gorm:"index"`
	CallerPlugin   string `gorm:"index"`
	Purpose        string `gorm:"index"`
	ChatID         string `gorm:"index"`
	TaskID         string `gorm:"index"`
	TurnID         string `gorm:"index"`
	DispatchID     string `gorm:"index"`
	RequestedModel string
	StartedAt      int64 `gorm:"index"`
	EndedAt        int64
	Status         string `gorm:"index"`
	Error          string
}

func (LLMCall) TableName() string { return "llm_calls" }

type LLMCallAttempt struct {
	ID                    string `gorm:"primaryKey"`
	CallID                string `gorm:"uniqueIndex:idx_llm_attempt_call,priority:1;index"`
	Attempt               int    `gorm:"uniqueIndex:idx_llm_attempt_call,priority:2"`
	Profile               string
	RequestedModel        string
	Model                 string `gorm:"index"`
	InputTokens           *int64
	CachedInputTokens     *int64
	CacheWriteInputTokens *int64
	OutputTokens          *int64
	ReasoningOutputTokens *int64
	TotalTokens           *int64
	StartedAt             int64 `gorm:"index"`
	EndedAt               int64
	LatencyMS             int64
	Status                string `gorm:"index"`
	Error                 string
}

func (LLMCallAttempt) TableName() string { return "llm_call_attempts" }

type usageStore struct{ db *gorm.DB }

func openUsageStore(dataDir string) (*usageStore, error) {
	path := filepath.Join(dataDir, "llm.sqlite3")
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate", filepath.ToSlash(path))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open llm metrics database: %w", err)
	}
	if err := db.AutoMigrate(&LLMCall{}, &LLMCallAttempt{}); err != nil {
		return nil, fmt.Errorf("migrate llm metrics database: %w", err)
	}
	return &usageStore{db: db}, nil
}

func (s *usageStore) close() error {
	if s == nil {
		return nil
	}
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func trimMetricError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	if len(value) > 1000 {
		return value[:1000]
	}
	return value
}

func (p *Plugin) beginCall(transport, caller, purpose, requestedModel string, tracking callTracking) string {
	if p.usageDB == nil {
		return ""
	}
	callID := newRequestID()
	if transport == "" {
		transport = "rpc"
	}
	if purpose == "" {
		purpose = "other"
	}
	call := &LLMCall{ID: callID, Transport: transport, CallerPlugin: caller, Purpose: purpose,
		ChatID: tracking.ChatID, TaskID: tracking.TaskID, TurnID: tracking.TurnID, DispatchID: tracking.DispatchID,
		RequestedModel: requestedModel, StartedAt: time.Now().UnixMilli(), Status: "running"}
	if err := p.usageDB.db.Create(call).Error; err != nil {
		return ""
	}
	return callID
}

func (p *Plugin) finishCall(callID string, err error) {
	if p.usageDB == nil || callID == "" {
		return
	}
	status := "succeeded"
	if err != nil {
		status = "failed"
	}
	_ = p.usageDB.db.Model(&LLMCall{}).Where("id = ?", callID).Updates(map[string]any{
		"status": status, "error": trimMetricError(err), "ended_at": time.Now().UnixMilli(),
	}).Error
}

func (p *Plugin) recordAttempt(callID string, attempt int, profile, requestedModel, model string, startedAt time.Time, err error, usage *agentdriver.TokenUsage) {
	if p.usageDB == nil || callID == "" {
		return
	}
	endedAt := time.Now()
	status := "succeeded"
	if err != nil {
		status = "failed"
	}
	row := &LLMCallAttempt{ID: newRequestID(), CallID: callID, Attempt: attempt, Profile: profile,
		RequestedModel: requestedModel, Model: model, StartedAt: startedAt.UnixMilli(), EndedAt: endedAt.UnixMilli(),
		LatencyMS: endedAt.Sub(startedAt).Milliseconds(), Status: status, Error: trimMetricError(err)}
	if usage != nil {
		row.InputTokens, row.CachedInputTokens, row.CacheWriteInputTokens = usage.InputTokens, usage.CachedInputTokens, usage.CacheWriteInputTokens
		row.OutputTokens, row.ReasoningOutputTokens, row.TotalTokens = usage.OutputTokens, usage.ReasoningOutputTokens, usage.TotalTokens
	}
	_ = p.usageDB.db.Create(row).Error
}
