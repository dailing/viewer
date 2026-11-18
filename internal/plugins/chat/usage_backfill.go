package chat

import (
	"encoding/json"
	"fmt"
	"log"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"viewer/internal/codexserver"
)

const codexUsageBackfillVersion = "1"

// backfillCodexTokenUsage migrates the already-persisted raw Codex events into
// the accounting table once. Cumulative tokenUsage.total is never consulted.
func (p *Plugin) backfillCodexTokenUsage() error {
	var state PluginState
	lookup := p.store.db.Limit(1).Find(&state, "key = ?", "codex_usage_backfill_version")
	if lookup.Error != nil {
		return lookup.Error
	}
	if lookup.RowsAffected > 0 && state.Value == codexUsageBackfillVersion {
		return nil
	}
	rows := 0
	var events []TurnEvent
	err := p.store.db.Model(&TurnEvent{}).Where("LOWER(kind) LIKE ?", "%tokenusage%").Order("occurred_at, id").
		FindInBatches(&events, 500, func(tx *gorm.DB, _ int) error {
			turnIDs := make([]string, 0, len(events))
			for _, event := range events {
				turnIDs = append(turnIDs, event.TurnID)
			}
			var turns []Turn
			if len(turnIDs) > 0 {
				if err := tx.Where("id IN ?", turnIDs).Find(&turns).Error; err != nil {
					return err
				}
			}
			turnByID := map[string]Turn{}
			for _, turn := range turns {
				turnByID[turn.ID] = turn
			}
			for _, event := range events {
				var raw struct {
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				if json.Unmarshal([]byte(event.RawJSON), &raw) != nil {
					continue
				}
				usage := codexserver.ParseTokenUsage(raw.Method, raw.Params)
				if usage == nil {
					continue
				}
				turn := turnByID[event.TurnID]
				row := &AgentTokenUsage{ID: newID(), EventID: event.ID, ChatID: event.ChatID, TaskID: event.TaskID, TurnID: event.TurnID,
					Agent: turn.Agent, Provider: turn.Provider, Model: turn.Model, InputTokens: usage.InputTokens,
					CachedInputTokens: usage.CachedInputTokens, CacheWriteInputTokens: usage.CacheWriteInputTokens,
					OutputTokens: usage.OutputTokens, ReasoningOutputTokens: usage.ReasoningOutputTokens,
					TotalTokens: usage.TotalTokens, OccurredAt: event.OccurredAt}
				if row.Provider == "" {
					row.Provider = event.Provider
				}
				if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "event_id"}}, DoNothing: true}).Create(row).Error; err != nil {
					return err
				}
				rows++
			}
			return nil
		}).Error
	if err != nil {
		return fmt.Errorf("backfill Codex token usage: %w", err)
	}
	if err := p.store.db.Save(&PluginState{Key: "codex_usage_backfill_version", Value: codexUsageBackfillVersion}).Error; err != nil {
		return err
	}
	if rows > 0 {
		log.Printf("viewer-chat Codex token usage backfill completed events=%d", rows)
	}
	return nil
}
