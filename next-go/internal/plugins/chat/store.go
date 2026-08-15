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

type TurnEvent struct {
	ID         string `gorm:"primaryKey"`
	ChatID     string `gorm:"index;not null"`
	TurnID     string `gorm:"index;not null"`
	RoleID     string `gorm:"index"`
	Provider   string `gorm:"not null"`
	SessionID  string `gorm:"not null"`
	Seq        int    `gorm:"not null"`
	Kind       string `gorm:"not null"`
	RawJSON    string `gorm:"not null"`
	OccurredAt int64  `gorm:"index"`
}

type MessageBlock struct {
	ID         string `gorm:"primaryKey"`
	EventID    string `gorm:"index;not null"`
	ChatID     string `gorm:"index;not null"`
	TurnID     string `gorm:"index;not null"`
	Kind       string `gorm:"not null"`
	Text       string
	Payload    string `gorm:"not null"`
	OccurredAt int64
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

type TurnSummary struct {
	TurnID             string `gorm:"primaryKey"`
	ChatID             string `gorm:"index;not null"`
	RoleID             string `gorm:"index"`
	RoleName           string
	Provider           string
	Status             string
	Summary            string
	Model              string
	ProfileID          string
	SourceMessageCount int
	SourceCharCount    int
	LatencyMS          int
	Error              string
	OccurredAt         int64 `gorm:"index"`
	CreatedAt          int64
}

type store struct{ db *gorm.DB }

func openStore(dataDir string) (*store, error) {
	path := filepath.Join(dataDir, "chat.sqlite3")
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", filepath.ToSlash(path))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open chat database: %w", err)
	}
	if err := db.AutoMigrate(&Chat{}, &SuperRole{}, &RoutingPolicyRow{}, &RoleSession{}, &Message{}, &Turn{}, &TurnSummary{}, &TurnEvent{}, &MessageBlock{}, &PluginState{}); err != nil {
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

func (s *store) roles() ([]SuperRole, error) {
	var values []SuperRole
	err := s.db.Order("created_at, id").Find(&values).Error
	return values, err
}

func (s *store) saveRole(value *SuperRole) error { return s.db.Save(value).Error }

func (s *store) deleteRole(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("role_id = ?", id).Delete(&RoleSession{}).Error; err != nil {
			return err
		}
		return tx.Delete(&SuperRole{}, "id = ?", id).Error
	})
}

func policyRow(value RoutingPolicyConfig, createdAt int64) RoutingPolicyRow {
	candidates := make([]storedRoutingCandidate, 0, len(value.Candidates))
	for _, candidate := range value.Candidates {
		agent := strings.TrimSpace(candidate.AgentID)
		if agent == "" {
			agent = strings.TrimSpace(candidate.TargetID)
		}
		parameters := candidate.Parameters
		if parameters == nil {
			parameters = map[string]any{}
		}
		candidates = append(candidates, storedRoutingCandidate{Agent: agent, Provider: candidate.ProviderID, Model: candidate.ModelID, Parameters: parameters, Enabled: candidate.Enabled})
	}
	now := nowMillis()
	if createdAt == 0 {
		createdAt = now
	}
	return RoutingPolicyRow{ID: value.ID, Name: value.Name, CandidatesJSON: encodeJSON(candidates), AutoFailover: value.AutoFailover, MaxAttempts: value.MaxAttempts, CreatedAt: createdAt, UpdatedAt: now}
}

func policyConfig(value RoutingPolicyRow) RoutingPolicyConfig {
	var stored []storedRoutingCandidate
	if json.Unmarshal([]byte(value.CandidatesJSON), &stored) != nil {
		stored = []storedRoutingCandidate{}
	}
	candidates := make([]RoutingCandidateConfig, 0, len(stored))
	for index, candidate := range stored {
		candidates = append(candidates, RoutingCandidateConfig{ID: fmt.Sprintf("%s-candidate-%d", value.ID, index+1), Name: fmt.Sprintf("Candidate %d", index+1), AgentID: candidate.Agent, ProviderID: candidate.Provider, ModelID: candidate.Model, Enabled: candidate.Enabled, Parameters: candidate.Parameters})
	}
	return RoutingPolicyConfig{ID: value.ID, Name: value.Name, Enabled: true, AutoFailover: value.AutoFailover, MaxAttempts: value.MaxAttempts, Candidates: candidates}
}

func (s *store) routingPolicies() ([]RoutingPolicyConfig, error) {
	var rows []RoutingPolicyRow
	if err := s.db.Order("created_at, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	values := make([]RoutingPolicyConfig, 0, len(rows))
	for _, row := range rows {
		values = append(values, policyConfig(row))
	}
	return values, nil
}

func (s *store) replaceRouting(value RoutingConfig) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var existing []RoutingPolicyRow
		if err := tx.Find(&existing).Error; err != nil {
			return err
		}
		created := map[string]int64{}
		for _, row := range existing {
			created[row.ID] = row.CreatedAt
		}
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&RoutingPolicyRow{}).Error; err != nil {
			return err
		}
		for _, policy := range value.RoutingPolicies {
			row := policyRow(policy, created[policy.ID])
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return tx.Save(&PluginState{Key: "default_routing_policy_id", Value: value.DefaultRoutingPolicyID}).Error
	})
}

func (s *store) defaultRoutingPolicyID() (string, error) {
	var value PluginState
	result := s.db.Limit(1).Find(&value, "key = ?", "default_routing_policy_id")
	if result.Error != nil || result.RowsAffected == 0 {
		return "", result.Error
	}
	return value.Value, nil
}

func (s *store) domainTablesEmpty() (bool, error) {
	var roleCount, policyCount int64
	if err := s.db.Model(&SuperRole{}).Count(&roleCount).Error; err != nil {
		return false, err
	}
	if err := s.db.Model(&RoutingPolicyRow{}).Count(&policyCount).Error; err != nil {
		return false, err
	}
	return roleCount == 0 && policyCount == 0, nil
}

func (s *store) importDomain(roles []SuperRole, routing RoutingConfig) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		for index := range roles {
			if err := tx.Create(&roles[index]).Error; err != nil {
				return err
			}
		}
		for _, policy := range routing.RoutingPolicies {
			row := policyRow(policy, 0)
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return tx.Save(&PluginState{Key: "default_routing_policy_id", Value: routing.DefaultRoutingPolicyID}).Error
	})
}

func (s *store) deleteChat(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		for _, model := range []any{&MessageBlock{}, &TurnEvent{}, &Message{}, &Turn{}, &TurnSummary{}, &RoleSession{}} {
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
func (s *store) updateMessageText(id, text string) error {
	return s.db.Model(&Message{}).Where("id = ?", id).Update("text", text).Error
}
func (s *store) addTurnEvent(event *TurnEvent) error       { return s.db.Create(event).Error }
func (s *store) addMessageBlock(block *MessageBlock) error { return s.db.Create(block).Error }

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

func (s *store) turn(id string) (*Turn, error) {
	var value Turn
	result := s.db.Limit(1).Find(&value, "id = ?", id)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &value, nil
}

func (s *store) turnMessages(turnID string) ([]Message, error) {
	var values []Message
	err := s.db.Where("turn_id = ?", turnID).Order("created_at, id").Find(&values).Error
	return values, err
}

func (s *store) turnMessageBlocks(turnID string) ([]MessageBlock, error) {
	var values []MessageBlock
	err := s.db.Table("message_blocks").
		Select("message_blocks.*").
		Joins("JOIN turn_events ON turn_events.id = message_blocks.event_id").
		Where("message_blocks.turn_id = ?", turnID).
		Order("turn_events.seq, message_blocks.id").
		Find(&values).Error
	return values, err
}

// chatMessageBlocks lists a chat's activity blocks in display order (strictly
// by observation time), optionally restricted to a time window [after, before)
// in ms — the timeline fetches blocks per loaded message span so long chats
// lazy-load older pages instead of pulling everything at once. Zero bounds
// mean unbounded.
func (s *store) chatMessageBlocks(chatID string, after, before int64) ([]MessageBlock, error) {
	query := s.db.Where("chat_id = ?", chatID)
	if after > 0 {
		query = query.Where("occurred_at >= ?", after)
	}
	if before > 0 {
		query = query.Where("occurred_at < ?", before)
	}
	var values []MessageBlock
	err := query.Order("occurred_at, id").Find(&values).Error
	return values, err
}

// historyPage returns one page of a chat's messages for newest-first
// pagination: at most limit messages strictly older than the composite cursor
// (created_at, id), in ascending display order (newest of the page last).
// hasMore reports whether older messages exist beyond the page. A zero
// beforeTs means "start from the newest end" (no cursor).
func (s *store) historyPage(chatID string, beforeTs int64, beforeID string, limit int) ([]Message, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	query := s.db.Where("chat_id = ?", chatID)
	if beforeTs > 0 {
		if beforeID != "" {
			query = query.Where("(created_at < ? OR (created_at = ? AND id < ?))", beforeTs, beforeTs, beforeID)
		} else {
			query = query.Where("created_at < ?", beforeTs)
		}
	}
	var values []Message
	if err := query.Order("created_at desc, id desc").Limit(limit + 1).Find(&values).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(values) > limit
	if hasMore {
		values = values[:limit]
	}
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
	return values, hasMore, nil
}

func (s *store) chatTurns(chatID string) ([]Turn, error) {
	var values []Turn
	err := s.db.Where("chat_id = ?", chatID).Find(&values).Error
	return values, err
}

func (s *store) latestUserMessage(chatID string, before int64) (*Message, error) {
	var value Message
	result := s.db.Where("chat_id = ? AND role = ? AND created_at <= ?", chatID, "user", before).Order("created_at desc").Limit(1).Find(&value)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &value, nil
}

func (m MessageBlock) payload() map[string]any {
	return map[string]any{
		"id": m.ID, "chat_id": m.ChatID, "turn_id": m.TurnID, "kind": m.Kind,
		"text": m.Text, "payload": m.Payload, "occurred_at": m.OccurredAt,
	}
}

func (s *store) saveTurnSummary(value *TurnSummary) error { return s.db.Save(value).Error }

func (s *store) recentTurnSummaries(chatID string, before, after int64, excludeRoleID string) ([]TurnSummary, error) {
	var values []TurnSummary
	query := s.db.Where("chat_id = ? AND status = ? AND occurred_at < ?", chatID, "completed", before)
	if after > 0 {
		query = query.Where("occurred_at > ?", after)
	}
	if excludeRoleID != "" {
		query = query.Where("role_id <> ?", excludeRoleID)
	}
	err := query.Order("occurred_at asc, turn_id asc").Limit(50).Find(&values).Error
	return values, err
}

func (s *store) latestSummaryTime(chatID string, before int64) (int64, error) {
	var value TurnSummary
	result := s.db.Where("chat_id = ? AND status = ? AND occurred_at < ?", chatID, "completed", before).Order("occurred_at desc").Limit(1).Find(&value)
	if result.Error != nil || result.RowsAffected == 0 {
		return 0, result.Error
	}
	return value.OccurredAt, nil
}

func (s *store) roleLastActivity(chatID, roleID string, before int64) (int64, error) {
	var value Message
	result := s.db.Where("chat_id = ? AND role_id = ? AND created_at < ?", chatID, roleID, before).Order("created_at desc").Limit(1).Find(&value)
	if result.Error != nil || result.RowsAffected == 0 {
		return 0, result.Error
	}
	return value.CreatedAt, nil
}

func (s *store) hasActivityBetween(chatID string, after, before int64) (bool, error) {
	var count int64
	err := s.db.Model(&Message{}).Where("chat_id = ? AND created_at > ? AND created_at < ?", chatID, after, before).Count(&count).Error
	return count > 0, err
}

func (s *store) historyAfter(chatID string, after, before int64, wordBudget int) ([]Message, error) {
	var values []Message
	query := s.db.Where("chat_id = ? AND created_at < ?", chatID, before)
	if after > 0 {
		query = query.Where("created_at > ?", after)
	}
	if err := query.Order("created_at desc").Limit(1000).Find(&values).Error; err != nil {
		return nil, err
	}
	used, picked := 0, []Message{}
	for _, value := range values {
		words := len(splitWords(value.Text))
		if wordBudget > 0 && used+words > wordBudget && len(picked) > 0 {
			break
		}
		used += words
		picked = append(picked, value)
	}
	for left, right := 0, len(picked)-1; left < right; left, right = left+1, right-1 {
		picked[left], picked[right] = picked[right], picked[left]
	}
	return picked, nil
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
