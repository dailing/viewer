package chat

// Runtime ↔ history-DAG glue (docs/chat-branch-dag-plan.md §5): batch
// intake records the fixed input snapshot; turn completion publishes
// result nodes; batch settlement advances the line head (join node for
// multi-role batches); startup recovery finalizes crash leftovers into an
// explainable, idempotent state. Cross-branch parallel, in-branch ordered:
// one batch per line at a time (the line busy key serializes intake), so
// head advancement has a single writer per line.

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"gorm.io/gorm"
)

// intakeBatch atomically records the batch's fixed input snapshot (the
// line's current head) and reports whether the line's sessions are stale —
// set by merge: the next batch on a merged-into line starts every role on a
// fresh session fed with the new full snapshot, then clears the flag.
// Serialized with merge and other dispatches by p.automationMu.
func (p *Plugin) intakeBatch(chatID, branchID, dispatchID string) (batchID, inputNodeID string, stale bool, err error) {
	batchID = newID()
	err = p.store.db.Transaction(func(tx *gorm.DB) error {
		head, err := ensureLineHead(tx, chatID, branchID)
		if err != nil {
			return err
		}
		inputNodeID = head.HeadNodeID
		stale = head.SessionsStale
		if stale {
			if err := tx.Model(&LineHead{}).Where("chat_id = ? AND branch_id = ?", chatID, branchID).Update("sessions_stale", false).Error; err != nil {
				return err
			}
		}
		return tx.Create(&DispatchSnapshot{BatchID: batchID, DispatchID: dispatchID, ChatID: chatID, BranchID: branchID, InputNodeID: inputNodeID, InputRevision: head.Revision, CreatedAt: nowMillis()}).Error
	})
	return batchID, inputNodeID, stale, err
}

// createAutoBranch opens one named branch forked from the origin line's
// CURRENT head (parallel send-now): branch row, fork node with its parent
// edge, and the line head row land in one transaction.
func (p *Plugin) createAutoBranch(chatID, name, originBranch string, role SuperRole) (*Branch, error) {
	now := nowMillis()
	branch := &Branch{ID: newID(), ChatID: chatID, Name: name, RoleID: role.ID, RoleName: role.Name, State: branchStateOpen, CreatedAt: now, UpdatedAt: now}
	err := p.store.db.Transaction(func(tx *gorm.DB) error {
		head, err := ensureLineHead(tx, chatID, originBranch)
		if err != nil {
			return err
		}
		node := &HistoryNode{ID: forkNodeID(branch.ID), ChatID: chatID, Kind: nodeKindFork, OriginBranchID: branch.ID, CreatedAt: now}
		if err := addNode(tx, node, []string{head.HeadNodeID}, edgeKindFork); err != nil {
			return err
		}
		if err := tx.Create(&LineHead{ChatID: chatID, BranchID: branch.ID, HeadNodeID: node.ID, Revision: 1}).Error; err != nil {
			return err
		}
		return tx.Create(branch).Error
	})
	if err != nil {
		return nil, err
	}
	return branch, nil
}

// publishTurnNode appends a finished turn's history node, parented at the
// batch's recorded input head. Deterministic node id (the turn id) makes
// republishing idempotent across retries and crash recovery. A turn only
// enters published history once terminated with its final content
// persisted — normal completion, failure, and cancellation all publish.
func (p *Plugin) publishTurnNode(chatID, branchID string, turn *Turn, inputNodeID string) (string, error) {
	id := turnNodeID(turn.ID)
	if inputNodeID == "" {
		return "", fmt.Errorf("%w: turn %s has no input snapshot", errGraphCorrupt, turn.ID)
	}
	err := p.store.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&HistoryNode{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		kind := edgeKindSequence
		if branchID != "" && inputNodeID == forkNodeID(branchID) {
			kind = edgeKindFork
		}
		return addNode(tx, &HistoryNode{ID: id, ChatID: chatID, Kind: nodeKindTurn, OriginBranchID: branchID, TurnID: turn.ID, CreatedAt: turn.StartedAt}, []string{inputNodeID}, kind)
	})
	return id, err
}

// advanceBatch settles a finished batch: a single result node becomes the
// line head directly; multiple role results收束 into one join node first
// (no last-finisher overwrites another's work). The line's busy key makes
// this single-writer per line.
func (p *Plugin) advanceBatch(chatID, branchID, batchID string, nodeIDs []string) error {
	if len(nodeIDs) == 0 {
		return nil
	}
	return p.store.db.Transaction(func(tx *gorm.DB) error {
		if _, err := ensureLineHead(tx, chatID, branchID); err != nil {
			return err
		}
		head := nodeIDs[0]
		if len(nodeIDs) > 1 {
			head = joinNodeID(batchID, branchID)
			var count int64
			if err := tx.Model(&HistoryNode{}).Where("id = ?", head).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				if err := addNode(tx, &HistoryNode{ID: head, ChatID: chatID, Kind: nodeKindJoin, OriginBranchID: branchID, CreatedAt: nowMillis()}, nodeIDs, edgeKindJoin); err != nil {
					return err
				}
			}
		}
		return advanceLineHead(tx, chatID, branchID, head)
	})
}

// lineHasRunningTurn reports whether the line ("" = mainline) has an
// in-flight turn — merges and archives refuse busy lines.
func (s *store) lineHasRunningTurn(chatID, branchID string) (bool, error) {
	var count int64
	err := s.db.Model(&Turn{}).Where("chat_id = ? AND branch_id = ? AND ended_at IS NULL", chatID, branchID).Count(&count).Error
	return count > 0, err
}

// recoverHistory finalizes history left inconsistent by a crash or kill,
// before any new dispatch is accepted:
//  1. turns still open (ended_at NULL) are terminated as "interrupted" and
//     their result nodes published idempotently off their batch snapshot
//     (or the line head when the snapshot is lost), then the head advances;
//  2. visible user inputs that never produced a turn (e.g. a dispatch
//     queued across the restart) become input nodes at their line's head —
//     executed/failed historical input is never silently dropped.
func (p *Plugin) recoverHistory() error {
	var open []Turn
	if err := p.store.db.Where("ended_at IS NULL").Find(&open).Error; err != nil {
		return err
	}
	for index := range open {
		if err := p.store.db.Model(&Turn{}).Where("id = ? AND ended_at IS NULL", open[index].ID).Updates(map[string]any{"ended_at": nowMillis(), "stop_reason": "interrupted"}).Error; err != nil {
			return err
		}
	}
	if err := p.publishRecoveredTurns(open); err != nil {
		return err
	}
	// Orphan user inputs: no turn carries their dispatch id, and no input
	// node exists yet (idempotent across restarts).
	var inputs []Message
	if err := p.store.db.Raw(`SELECT m.* FROM messages m
		WHERE m.role = 'user'
		AND NOT EXISTS (SELECT 1 FROM turns t WHERE t.dispatch_id = m.turn_id)
		AND NOT EXISTS (SELECT 1 FROM history_nodes n WHERE n.id = 'input:' || m.id)`).Scan(&inputs).Error; err != nil {
		return err
	}
	for _, message := range inputs {
		line := ""
		var receipt DispatchReceipt
		if lookup := p.store.db.Where("dispatch_id = ?", message.TurnID).Limit(1).Find(&receipt); lookup.Error == nil && lookup.RowsAffected > 0 {
			line = receipt.BranchID
		}
		if line != "" {
			branch, err := p.store.branch(line)
			if err != nil || branch == nil || branch.State != branchStateOpen {
				line = ""
			}
		}
		if err := p.publishInputNode(message.ChatID, line, message.ID, message.CreatedAt); err != nil {
			return err
		}
		slog.Info("chat orphan input recovered into history", "chat_id", message.ChatID, "message_id", message.ID, "branch", line)
	}
	return nil
}

// publishRecoveredTurns publishes nodes for crash-terminated turns and
// advances their lines' heads (grouped per line, one join per recovery
// batch). Idempotent: node ids are deterministic.
func (p *Plugin) publishRecoveredTurns(turns []Turn) error {
	byLine := map[string][]Turn{}
	for _, turn := range turns {
		byLine[turn.ChatID+"\x00"+turn.BranchID] = append(byLine[turn.ChatID+"\x00"+turn.BranchID], turn)
	}
	for key, group := range byLine {
		chatID := group[0].ChatID
		branchID := group[0].BranchID
		_ = key
		sort.SliceStable(group, func(i, j int) bool { return group[i].StartedAt < group[j].StartedAt })
		nodeIDs := []string{}
		for _, turn := range group {
			inputNode := ""
			if turn.BatchID != "" {
				var snapshot DispatchSnapshot
				if lookup := p.store.db.Where("batch_id = ?", turn.BatchID).Limit(1).Find(&snapshot); lookup.Error == nil && lookup.RowsAffected > 0 {
					inputNode = snapshot.InputNodeID
				}
			}
			if inputNode == "" {
				if err := p.store.db.Transaction(func(tx *gorm.DB) error {
					head, err := ensureLineHead(tx, chatID, branchID)
					if err != nil {
						return err
					}
					inputNode = head.HeadNodeID
					return nil
				}); err != nil {
					return err
				}
			}
			nodeID, err := p.publishTurnNode(chatID, branchID, &turn, inputNode)
			if err != nil {
				return err
			}
			nodeIDs = append(nodeIDs, nodeID)
		}
		if len(nodeIDs) > 0 {
			batchID := group[0].BatchID
			if batchID == "" {
				batchID = "recovery"
			}
			if err := p.advanceBatch(chatID, branchID, batchID, nodeIDs); err != nil {
				return err
			}
		}
	}
	return nil
}

// publishInputNode appends an orphan user input as a content node at the
// line's current head and advances the head.
func (p *Plugin) publishInputNode(chatID, branchID, messageID string, createdAt int64) error {
	return p.store.db.Transaction(func(tx *gorm.DB) error {
		head, err := ensureLineHead(tx, chatID, branchID)
		if err != nil {
			return err
		}
		id := inputNodeID(messageID)
		var count int64
		if err := tx.Model(&HistoryNode{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			if err := addNode(tx, &HistoryNode{ID: id, ChatID: chatID, Kind: nodeKindInput, OriginBranchID: branchID, TurnID: messageID, CreatedAt: createdAt}, []string{head.HeadNodeID}, edgeKindSequence); err != nil {
				return err
			}
		}
		return advanceLineHead(tx, chatID, branchID, id)
	})
}

// lineBusyLocked reports whether the line has an in-flight batch or queued
// entries — merges and archives refuse such lines with an actionable busy
// error. Caller holds p.mu.
func (p *Plugin) lineBusyLocked(chatID, branchID string) (bool, string) {
	key := lineKey(chatID, branchID)
	if p.busy[key] {
		return true, "a batch is running"
	}
	if len(p.queues[key]) > 0 {
		return true, fmt.Sprintf("%d queued batch(es) are waiting", len(p.queues[key]))
	}
	return false, ""
}

var errLineBusy = errors.New("the work line is busy — wait for it to settle or pause its automation first")

// lineMergeBlockers collects every reason a line cannot merge right now:
// in-flight batch, queued batches, running turns, or an active
// (unpaused) automation gate. Serialized with dispatch intake by
// p.automationMu, so the check cannot go stale before the merge commits.
func (p *Plugin) lineMergeBlockers(chatID, branchID string) error {
	p.mu.Lock()
	busy, reason := p.lineBusyLocked(chatID, branchID)
	p.mu.Unlock()
	if busy {
		return fmt.Errorf("%w: %s", errLineBusy, reason)
	}
	running, err := p.store.lineHasRunningTurn(chatID, branchID)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("%w: a turn is finishing up", errLineBusy)
	}
	var gate AutomationGate
	if lookup := p.store.db.Where("key = ?", gateKey(chatID, branchID)).Limit(1).Find(&gate); lookup.Error != nil {
		return lookup.Error
	} else if lookup.RowsAffected > 0 && gate.AutomationID != "" && !gate.Paused {
		return fmt.Errorf("%w: automation %s is live on this line — pause it first", errLineBusy, gate.AutomationID)
	}
	return nil
}
