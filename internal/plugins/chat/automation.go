package chat

// Automation is a generic chat contract: durable deduplication, fenced
// scheduler ownership, and a branch gate serialized with human dispatches.
import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"viewer/sdk/go/busclient"
)

type AutomationLease struct {
	ID        string `gorm:"primaryKey" json:"-"`
	Owner     string `json:"owner"`
	Fence     int64  `json:"fence"`
	ExpiresAt int64  `json:"expires_at"`
}
type AutomationGate struct {
	Key          string `gorm:"primaryKey" json:"-"`
	ChatID       string `json:"chat_id"`
	BranchID     string `json:"branch_id"`
	AutomationID string `json:"automation_id"`
	Paused       bool   `json:"paused"`
	Revision     int64  `json:"revision"`
	Feedback     string `json:"feedback"`
}
type DispatchReceipt struct {
	Key          string `gorm:"primaryKey" json:"idempotency_key"`
	DispatchID   string `gorm:"uniqueIndex" json:"dispatch_id"`
	ChatID       string `json:"chat_id"`
	BranchID     string `json:"branch_id"`
	AutomationID string `gorm:"index" json:"automation_id"`
	Generation   string `json:"-"`
	Fingerprint  string `json:"-"`
	Failure      string `json:"error,omitempty"`
	Cancelled    bool   `json:"-"`
	CreatedAt    int64  `json:"created_at"`
}

func gateKey(chat, branch string) string { return chat + "\x00" + branch }
func fingerprint(r dispatchRequest) string {
	b, _ := json.Marshal([]any{r.ChatID, r.BranchID, r.AutomationID, r.Message, r.RoleIDs, r.ForceNewSession})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func (p *Plugin) existingDispatch(r dispatchRequest) (map[string]any, bool, error) {
	if r.IdempotencyKey == "" || len(r.RoleIDs) != 1 || r.ParallelDispatch || r.ContinueTurnID != "" {
		return nil, false, errors.New("automation requires an idempotency key and exactly one explicit role; parallel/lane dispatch is unsupported")
	}
	var receipt DispatchReceipt
	lookup := p.store.db.Where("key = ?", r.IdempotencyKey).Limit(1).Find(&receipt)
	err := lookup.Error
	if err == nil && lookup.RowsAffected == 0 {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if receipt.Fingerprint != fingerprint(r) {
		return nil, true, errors.New("idempotency key reused with a different request")
	}
	return map[string]any{"dispatch_id": receipt.DispatchID, "deduplicated": true}, true, nil
}
func checkLease(tx *gorm.DB, owner string, fence int64) error {
	var lease AutomationLease
	if err := tx.First(&lease, "id = ?", "scheduler").Error; err != nil {
		return err
	}
	if owner == "" || lease.Owner != owner || lease.Fence != fence || lease.ExpiresAt <= nowMillis() {
		return errors.New("scheduler lease expired or fenced")
	}
	return nil
}
func (p *Plugin) prepareDispatch(r dispatchRequest, id string) error {
	err := p.store.db.Transaction(func(tx *gorm.DB) error {
		if r.AutomationID != "" {
			// Acquire SQLite's write lock before reading the lease/gate, including
			// across backend generations. Gate mutation and acceptance are atomic.
			if err := tx.Exec("UPDATE automation_leases SET fence = fence WHERE id = ?", "scheduler").Error; err != nil {
				return err
			}
			if err := checkLease(tx, r.Owner, r.Fence); err != nil {
				return err
			}
			var gate AutomationGate
			if err := tx.First(&gate, "key = ?", gateKey(r.ChatID, r.BranchID)).Error; err != nil {
				return err
			}
			if gate.AutomationID != r.AutomationID || gate.Paused {
				return errors.New("automation gate paused or owned by another run")
			}
			receipt := DispatchReceipt{Key: r.IdempotencyKey, DispatchID: id, ChatID: r.ChatID, BranchID: r.BranchID, AutomationID: r.AutomationID, Generation: p.generation, Fingerprint: fingerprint(r), CreatedAt: nowMillis()}
			return tx.Create(&receipt).Error
		}
		// Ordinary sends (including merge-confirm) close the gate before the
		// message enters the queue. A resume must compare this revision.
		return tx.Model(&AutomationGate{}).Where("key = ? AND automation_id <> ''", gateKey(r.ChatID, r.BranchID)).Updates(map[string]any{"paused": true, "revision": gorm.Expr("revision + 1"), "feedback": boundedTail(r.Message, 8192)}).Error
	})
	if err != nil || r.AutomationID != "" {
		return err
	}
	// A human send also retracts an automatic message still waiting in the
	// role queue. A turn already dequeued is the current turn and may finish.
	var receipts []DispatchReceipt
	if err = p.store.db.Where("chat_id = ? AND branch_id = ? AND cancelled = ?", r.ChatID, r.BranchID, false).Order("created_at DESC").Limit(2).Find(&receipts).Error; err != nil {
		return err
	}
	for _, receipt := range receipts {
		if err := p.cancelReceiptQueue(receipt); err != nil {
			return err
		}
	}
	return nil
}
func (p *Plugin) cancelReceiptQueue(receipt DispatchReceipt) error {
	count, messageID := p.cancelQueued(receipt.ChatID, receipt.DispatchID)
	if count == 0 {
		return nil
	}
	if err := p.store.db.Model(&receipt).Update("cancelled", true).Error; err != nil {
		return err
	}
	if messageID != "" {
		if err := p.store.deleteMessage(messageID); err != nil {
			return err
		}
		p.publish("chat:"+receipt.ChatID+":message", map[string]any{"id": messageID, "deleted": true})
	}
	p.publishQueue(receipt.ChatID)
	return nil
}
func (p *Plugin) handleAutomation(frame busclient.Frame) {
	value, err := frameObject(frame)
	var r struct {
		Op, Owner    string
		Fence        int64
		ChatID       string `json:"chat_id"`
		BranchID     string `json:"branch_id"`
		AutomationID string `json:"automation_id"`
		RoleID       string `json:"role_id"`
		Revision     int64
	}
	if err == nil {
		err = decodeInto(value, &r)
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	p.automationMu.Lock()
	defer p.automationMu.Unlock()
	var result any
	err = p.store.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&AutomationLease{ID: "scheduler"}).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE automation_leases SET fence = fence WHERE id = ?", "scheduler").Error; err != nil {
			return err
		}
		if r.Op == "lease" {
			var lease AutomationLease
			if err := tx.First(&lease, "id = ?", "scheduler").Error; err != nil {
				return err
			}
			if r.Owner == "" {
				return errors.New("owner required")
			}
			if lease.Owner != r.Owner && lease.ExpiresAt > nowMillis() {
				return errors.New("scheduler already owned")
			}
			if lease.Owner != r.Owner || lease.ExpiresAt <= nowMillis() {
				lease.Fence++
			}
			lease.Owner = r.Owner
			lease.ExpiresAt = nowMillis() + 30000
			result = lease
			return tx.Save(&lease).Error
		}
		if err := checkLease(tx, r.Owner, r.Fence); err != nil {
			return err
		}
		if r.Op == "release-lease" {
			return tx.Model(&AutomationLease{}).Where("id = ?", "scheduler").Update("expires_at", 0).Error
		}
		if r.ChatID == "" {
			return errors.New("chat_id required")
		}
		if r.Op == "describe" {
			var c Chat
			var role SuperRole
			if err := tx.First(&c, "id = ?", r.ChatID).Error; err != nil {
				return err
			}
			if err := tx.First(&role, "id = ?", r.RoleID).Error; err != nil {
				return err
			}
			member := false
			for _, id := range decodeStrings(c.MemberRoleIDsJSON) {
				if id == role.ID {
					member = true
				}
			}
			if !member {
				return errors.New("role is not a member of chat")
			}
			if r.BranchID != "" {
				var b Branch
				if err := tx.First(&b, "id = ? AND chat_id = ? AND archived_at IS NULL", r.BranchID, r.ChatID).Error; err != nil {
					return err
				}
			}
			root := c.Root
			if role.CWD != "" {
				if filepath.IsAbs(role.CWD) {
					root = role.CWD
				} else {
					root = filepath.Join(root, role.CWD)
				}
			}
			root, err := filepath.Abs(root)
			if err != nil {
				return err
			}
			result = map[string]any{"root": root, "role_name": role.Name}
			return nil
		}
		gate := AutomationGate{Key: gateKey(r.ChatID, r.BranchID), ChatID: r.ChatID, BranchID: r.BranchID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&gate).Error; err != nil {
			return err
		}
		if err := tx.First(&gate, "key = ?", gate.Key).Error; err != nil {
			return err
		}
		switch r.Op {
		case "get":
		case "claim":
			if r.AutomationID == "" {
				return errors.New("automation_id required")
			}
			if gate.AutomationID != "" && gate.AutomationID != r.AutomationID {
				return errors.New("branch already has an automation")
			}
			if gate.AutomationID == "" {
				gate.AutomationID = r.AutomationID
				gate.Paused = true
				gate.Revision++
			}
		case "resume", "pause", "release":
			if gate.AutomationID != r.AutomationID {
				return errors.New("automation does not own branch")
			}
			if r.Op == "resume" && gate.Revision != r.Revision {
				return errors.New("branch changed; refresh before resuming")
			}
			gate.Paused = r.Op != "resume"
			if r.Op == "release" {
				gate.AutomationID = ""
			}
			gate.Revision++
		default:
			return errors.New("unknown automation operation")
		}
		result = gate
		return tx.Save(&gate).Error
	})
	p.reply(frame, result, err)
}
func boundedTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[1:]
	}
	return "[earlier content omitted]\n" + s
}
func (p *Plugin) dispatchStatus(key string) (map[string]any, error) {
	var receipt DispatchReceipt
	lookup := p.store.db.Where("key = ?", key).Limit(1).Find(&receipt)
	if lookup.Error != nil {
		return nil, lookup.Error
	}
	if lookup.RowsAffected == 0 {
		return map[string]any{"status": "missing"}, nil
	}
	result := map[string]any{"dispatch_id": receipt.DispatchID, "status": "pending", "created_at": receipt.CreatedAt}
	if receipt.Cancelled {
		result["status"] = "cancelled"
		return result, nil
	}
	if receipt.Failure != "" {
		result["status"] = "failed"
		result["error"] = receipt.Failure
		return result, nil
	}
	var turns []Turn
	if err := p.store.db.Where("dispatch_id = ?", receipt.DispatchID).Order("started_at").Limit(2).Find(&turns).Error; err != nil {
		return nil, err
	}
	if len(turns) > 0 {
		t := turns[0]
		result["turn_id"] = t.ID
		result["started_at"] = t.StartedAt
		result["status"] = "running"
		if t.EndedAt != nil {
			result["status"] = "completed"
			result["ended_at"] = t.EndedAt
			result["stop_reason"] = t.StopReason
			// SQL substr and row limits bound transport/memory before reading text.
			var messages []struct{ Text string }
			if err := p.store.db.Model(&Message{}).Select("substr(text, -24000) AS text").Where("turn_id = ? AND role = ?", t.ID, "assistant").Order("created_at DESC").Limit(4).Scan(&messages).Error; err != nil {
				return nil, err
			}
			var parts []string
			for i := len(messages) - 1; i >= 0; i-- {
				parts = append(parts, messages[i].Text)
			}
			result["output"] = boundedTail(strings.Join(parts, "\n"), 24000)
			var blocks []struct{ Kind, Text, Payload string }
			if err := p.store.db.Model(&MessageBlock{}).Select("kind, substr(text, -2000) AS text, substr(payload, -3000) AS payload").Where("turn_id = ? AND kind <> ?", t.ID, "agent_text").Order("occurred_at DESC").Limit(8).Scan(&blocks).Error; err != nil {
				return nil, err
			}
			result["evidence"] = blocks
		} else if receipt.Generation != p.generation {
			result["status"] = "unknown"
		}
	} else if receipt.Generation != p.generation {
		result["status"] = "unknown"
	}
	return result, nil
}
func (p *Plugin) handleDispatchGet(frame busclient.Frame) {
	value, err := frameObject(frame)
	key := requestString(value, "idempotency_key")
	if err != nil || key == "" {
		p.reply(frame, nil, errors.New("idempotency_key required"))
		return
	}
	p.automationMu.Lock()
	defer p.automationMu.Unlock()
	op := requestString(value, "op")
	if op == "cancel" || op == "resolve" {
		if err := checkLease(p.store.db, requestString(value, "owner"), requestInt64(value, "fence")); err != nil {
			p.reply(frame, nil, err)
			return
		}
	}
	if op == "resolve" {
		var receipt DispatchReceipt
		if err = p.store.db.First(&receipt, "key = ?", key).Error; err == nil {
			if value["confirmed_stopped"] != true || receipt.Generation == p.generation {
				err = errors.New("only an explicitly confirmed old-generation task can be marked interrupted")
			} else {
				err = p.store.db.Transaction(func(tx *gorm.DB) error {
					if err := tx.Model(&Turn{}).Where("dispatch_id = ? AND ended_at IS NULL", receipt.DispatchID).Updates(map[string]any{"ended_at": nowMillis(), "stop_reason": "interrupted"}).Error; err != nil {
						return err
					}
					return tx.Model(&receipt).Update("failure", "operator confirmed previous execution has stopped").Error
				})
			}
		}
	}
	if op == "cancel" {
		var receipt DispatchReceipt
		if err = p.store.db.First(&receipt, "key = ?", key).Error; err == nil {
			err = p.cancelReceiptQueue(receipt)
			var turns []Turn
			if err == nil {
				err = p.store.db.Where("dispatch_id = ? AND ended_at IS NULL", receipt.DispatchID).Limit(2).Find(&turns).Error
			}
			for _, turn := range turns {
				_, stopErr := p.stopTurn(receipt.ChatID, "", turn.ID)
				err = errors.Join(err, stopErr)
			}
		}
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	result, err := p.dispatchStatus(key)
	p.reply(frame, result, err)
}
