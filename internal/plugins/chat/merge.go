package chat

// Named parallel branches (framework v0.63+, history-DAG model
// docs/chat-branch-dag-plan.md): creation/rename/delete, archive-without-
// merge, and the single-step atomic merge. Merge is a pure graph graft: one
// transaction writes a merge node (parents = target old head + every source
// head), advances the target head with sessions marked stale, and closes
// the source lines (state=merged). No LLM summary, no dispatched message,
// no agent wake-up; the source branch disappears from every list while its
// rows, turns, and edges stay for queries and debugging. The retired
// two-step draft/confirm protocol is rejected with an upgrade error.

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"

	"viewer/sdk/go/busclient"
)

var (
	errBranchArchived   = errors.New("the branch is archived (merged or shelved)")
	errBranchRunning    = errors.New("the branch still has a running turn — wait for it to finish")
	errBranchEmpty      = errors.New("the branch has no turns yet")
	errMergeProtocol    = errors.New("the merge draft/confirm protocol was retired — upgrade the client (merge is now a single-step branches:merge call)")
	errRevisionConflict = errors.New("the work line changed since you looked — refresh and retry")
)

// publishBranch pushes one branch record on the branch feed; panes reseed or
// upsert from it (phases: created / updated / archived / merged / deleted).
func (p *Plugin) publishBranch(branch *Branch, phase string) {
	payload := branch.payload()
	payload["phase"] = phase
	p.publish("chat:_:branch", payload)
}

func (p *Plugin) handleBranchesCreate(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	name := strings.TrimSpace(requestString(value, "name"))
	fromTurnID := strings.TrimSpace(requestString(value, "from_turn_id"))
	fromBranchID, hasFromBranch := value["from_branch_id"].(string)
	if err == nil && chatID == "" {
		err = errBadRequest
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	chat, err := p.store.chat(chatID)
	if err != nil || chat == nil {
		if err == nil {
			err = errors.New("chat not found")
		}
		p.reply(frame, nil, err)
		return
	}
	// Fork point: from_turn_id forks the exact ancestor closure of that
	// (finished, published) turn; from_branch_id forks a line's current
	// head ("" = mainline); neither starts a fresh, history-less branch.
	// The turn's line must be active — archived/merged lines are read-only.
	parentBranchID := ""
	if fromTurnID != "" {
		fork, forkErr := p.store.turn(fromTurnID)
		if forkErr != nil || fork == nil || fork.ChatID != chatID {
			if forkErr == nil {
				forkErr = errors.New("fork turn was not found in the chat")
			}
			p.reply(frame, nil, forkErr)
			return
		}
		parentBranchID = fork.BranchID
		if parentBranchID != "" {
			parent, parentErr := p.store.branch(parentBranchID)
			if parentErr != nil {
				p.reply(frame, nil, parentErr)
				return
			}
			if parent == nil || parent.archived() {
				p.reply(frame, nil, errBranchArchived)
				return
			}
		}
		var count int64
		if nodeErr := p.store.db.Model(&HistoryNode{}).Where("id = ? AND kind = ?", turnNodeID(fromTurnID), nodeKindTurn).Count(&count).Error; nodeErr != nil {
			p.reply(frame, nil, nodeErr)
			return
		}
		if count == 0 {
			p.reply(frame, nil, errors.New("the fork turn is not published yet — wait for its turn to finish"))
			return
		}
	} else if hasFromBranch && fromBranchID != "" {
		parent, parentErr := p.store.branch(fromBranchID)
		if parentErr != nil {
			p.reply(frame, nil, parentErr)
			return
		}
		if parent == nil || parent.ChatID != chatID {
			p.reply(frame, nil, errors.New("the from_branch_id branch was not found in the chat"))
			return
		}
		if parent.archived() {
			p.reply(frame, nil, errBranchArchived)
			return
		}
		parentBranchID = fromBranchID
	}
	if name == "" {
		// Default 分支NN: count over all branches (including retired) so a
		// number, once shown, never moves to another branch.
		existing, countErr := p.store.chatBranches(chatID)
		if countErr != nil {
			p.reply(frame, nil, countErr)
			return
		}
		name = fmt.Sprintf("分支%02d", len(existing)+1)
	}
	id := newID()
	if key := requestString(value, "idempotency_key"); key != "" {
		id = fmt.Sprintf("%x", sha256.Sum256([]byte("branch:"+chatID+":"+key)))[:32]
		existing, lookupErr := p.store.branch(id)
		if lookupErr != nil {
			p.reply(frame, nil, lookupErr)
			return
		}
		if existing != nil {
			if existing.ForkTurnID != fromTurnID {
				p.reply(frame, nil, errors.New("branch key reused with a different fork"))
				return
			}
			p.reply(frame, existing.payload(), nil)
			return
		}
	}
	now := nowMillis()
	branch := &Branch{ID: id, ChatID: chatID, Name: name, ForkTurnID: fromTurnID, ParentBranchID: parentBranchID, State: branchStateOpen, CreatedAt: now, UpdatedAt: now}
	err = p.store.db.Transaction(func(tx *gorm.DB) error {
		parents := []string{}
		switch {
		case fromTurnID != "":
			parents = []string{turnNodeID(fromTurnID)}
		case hasFromBranch:
			head, headErr := ensureLineHead(tx, chatID, fromBranchID)
			if headErr != nil {
				return headErr
			}
			parents = []string{head.HeadNodeID}
		}
		node := &HistoryNode{ID: forkNodeID(id), ChatID: chatID, Kind: nodeKindFork, OriginBranchID: id, CreatedAt: now}
		edgeKind := edgeKindFork
		if len(parents) == 0 {
			edgeKind = ""
		}
		if err := addNode(tx, node, parents, edgeKind); err != nil {
			return err
		}
		if err := tx.Create(&LineHead{ChatID: chatID, BranchID: id, HeadNodeID: node.ID, Revision: 1}).Error; err != nil {
			return err
		}
		return tx.Create(branch).Error
	})
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	p.publishBranch(branch, "created")
	p.reply(frame, branch.payload(), nil)
}

func (p *Plugin) handleBranchesPatch(frame busclient.Frame) {
	value, err := frameObject(frame)
	id, _ := value["id"].(string)
	if err == nil && id == "" {
		err = errors.New("id is required")
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	branch, err := p.store.branch(id)
	if err != nil || branch == nil {
		if err == nil {
			err = errors.New("branch not found")
		}
		p.reply(frame, nil, err)
		return
	}
	if name, ok := value["name"].(string); ok {
		branch.Name = strings.TrimSpace(name)
		if branch.Name == "" {
			branch.Name = "分支"
		}
	}
	branch.UpdatedAt = nowMillis()
	if err = p.store.saveBranch(branch); err != nil {
		p.reply(frame, nil, err)
		return
	}
	p.publishBranch(branch, "updated")
	p.reply(frame, branch.payload(), nil)
}

// handleBranchesDelete removes a branch that never ran (no turns) and is not
// retired — the cleanup path for a mis-clicked ＋. Its trivial graph line
// (fork node, head row) goes with it. Branches with history stay forever:
// archiving/merging are the terminal states, deletion would orphan the
// timeline's turn attribution.
func (p *Plugin) handleBranchesDelete(frame busclient.Frame) {
	value, err := frameObject(frame)
	id, _ := value["id"].(string)
	if err == nil && id == "" {
		err = errors.New("id is required")
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	branch, err := p.store.branch(id)
	if err != nil || branch == nil {
		if err == nil {
			err = errors.New("branch not found")
		}
		p.reply(frame, nil, err)
		return
	}
	turns, err := p.store.branchTurns(id)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	if len(turns) > 0 || branch.archived() {
		p.reply(frame, nil, errors.New("only an empty, unmerged branch can be deleted"))
		return
	}
	err = p.store.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&LineHead{}, "chat_id = ? AND branch_id = ?", branch.ChatID, id).Error; err != nil {
			return err
		}
		if err := tx.Delete(&HistoryEdge{}, "child_id = ? OR parent_id = ?", forkNodeID(id), forkNodeID(id)).Error; err != nil {
			return err
		}
		if err := tx.Delete(&HistoryNode{}, "id = ?", forkNodeID(id)).Error; err != nil {
			return err
		}
		return tx.Delete(&Branch{}, "id = ?", id).Error
	})
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	p.publishBranch(branch, "deleted")
	p.reply(frame, map[string]any{"deleted": true, "id": id}, nil)
}

// handleBranchesMerge is the single-step atomic merge
// (docs/chat-branch-dag-plan.md §6): one transaction writes the merge node
// (parents = target old head + every source head), advances the target head
// with its sessions marked stale (the next batch rebuilds every session
// from the new full snapshot), and closes the sources (state=merged).
// Empty branches merge fine (their fork node is real history); shared
// ancestry is deduplicated by the graph walk. expected_revisions (line →
// revision, "" key = mainline) makes a stale client fail with a revision
// conflict instead of merging blind; idempotency_key replays return the
// stored result, and a key reused with different parameters is rejected.
func (p *Plugin) handleBranchesMerge(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	targetBranchID := strings.TrimSpace(requestString(value, "target_branch_id"))
	idempotencyKey := strings.TrimSpace(requestString(value, "idempotency_key"))
	var branchIDs []string
	if err == nil {
		err = decodeInto(value["branch_ids"], &branchIDs)
	}
	expected := map[string]int64{}
	if raw, ok := value["expected_revisions"].(map[string]any); ok {
		for line, revision := range raw {
			if number, ok := revision.(float64); ok {
				expected[line] = int64(number)
			}
		}
	}
	if err == nil && (chatID == "" || len(branchIDs) == 0) {
		err = errBadRequest
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	// Serialize with dispatch intake and automation: the busy validation
	// below cannot go stale before the merge commits.
	p.automationMu.Lock()
	defer p.automationMu.Unlock()
	// Idempotency replay: same key + same parameters returns the stored
	// result; same key + different parameters is rejected outright.
	fingerprintSource := append([]string{chatID, targetBranchID}, branchIDs...)
	sort.Strings(fingerprintSource[1:])
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(fingerprintSource, "\x00"))))
	if idempotencyKey != "" {
		var receipt MergeReceipt
		lookup := p.store.db.Where("key = ?", idempotencyKey).Limit(1).Find(&receipt)
		if lookup.Error != nil {
			p.reply(frame, nil, lookup.Error)
			return
		}
		if lookup.RowsAffected > 0 {
			if receipt.Fingerprint != fingerprint {
				p.reply(frame, nil, errors.New("idempotency key reused with a different merge"))
				return
			}
			var stored map[string]any
			if json.Unmarshal([]byte(receipt.Payload), &stored) == nil {
				stored["deduplicated"] = true
				p.reply(frame, stored, nil)
			} else {
				p.reply(frame, map[string]any{"merged": true, "deduplicated": true}, nil)
			}
			return
		}
	}
	// Validate sources: same chat, all open (empty branches allowed — their
	// fork node is real history), target open and not among sources.
	sources := []*Branch{}
	seen := map[string]bool{}
	for _, id := range branchIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		branch, loadErr := p.store.branch(id)
		if loadErr != nil {
			p.reply(frame, nil, loadErr)
			return
		}
		if branch == nil || branch.ChatID != chatID {
			p.reply(frame, nil, fmt.Errorf("branch was not found in the chat: %s", id))
			return
		}
		if branch.archived() {
			p.reply(frame, nil, fmt.Errorf("分支「%s」%w", branch.Name, errBranchArchived))
			return
		}
		sources = append(sources, branch)
	}
	if seen[targetBranchID] {
		p.reply(frame, nil, errors.New("the merge target cannot be merged into itself"))
		return
	}
	var target *Branch
	if targetBranchID != "" {
		target, err = p.store.branch(targetBranchID)
		if err != nil {
			p.reply(frame, nil, err)
			return
		}
		if target == nil || target.ChatID != chatID {
			p.reply(frame, nil, errors.New("the merge target branch was not found in the chat"))
			return
		}
		if target.archived() {
			p.reply(frame, nil, fmt.Errorf("merge target %w", errBranchArchived))
			return
		}
	}
	// Busy validation: no in-flight batch, no queued batches, no live
	// automation on any involved line.
	for _, line := range append([]string{targetBranchID}, seenKeys(sources)...) {
		if busyErr := p.lineMergeBlockers(chatID, line); busyErr != nil {
			name := "主线"
			if line != "" {
				for _, branch := range sources {
					if branch.ID == line {
						name = "「" + branch.Name + "」"
					}
				}
				if target != nil && target.ID == line {
					name = "「" + target.Name + "」"
				}
			}
			p.reply(frame, nil, fmt.Errorf("%s %w", name, busyErr))
			return
		}
	}
	now := nowMillis()
	mergeNodeID := "merge:" + newID()
	var targetHead *LineHead
	err = p.store.db.Transaction(func(tx *gorm.DB) error {
		parents := []string{}
		for _, line := range append([]string{targetBranchID}, seenKeys(sources)...) {
			head, headErr := ensureLineHead(tx, chatID, line)
			if headErr != nil {
				return headErr
			}
			if revision, ok := expected[line]; ok && head.Revision != revision {
				return fmt.Errorf("line %s: %w", line, errRevisionConflict)
			}
			if line == targetBranchID {
				targetHead = head
			}
			parents = append(parents, head.HeadNodeID)
		}
		if err := addNode(tx, &HistoryNode{ID: mergeNodeID, ChatID: chatID, Kind: nodeKindMerge, OriginBranchID: targetBranchID, CreatedAt: now}, parents, edgeKindMerge); err != nil {
			return err
		}
		// Advance the target head and mark its sessions stale: the next
		// batch on the line rebuilds every session from the new full
		// snapshot (merged content is never bridged by time guesses).
		result := tx.Model(&LineHead{}).Where("chat_id = ? AND branch_id = ?", chatID, targetBranchID).
			Updates(map[string]any{"head_node_id": mergeNodeID, "revision": gorm.Expr("revision + 1"), "sessions_stale": true})
		if result.Error != nil {
			return result.Error
		}
		targetHead.HeadNodeID = mergeNodeID
		targetHead.Revision++
		targetHead.SessionsStale = true
		for _, source := range sources {
			updates := map[string]any{
				"state": branchStateMerged, "archived_at": now, "merged_at": now,
				"merged_into_branch_id": targetBranchID, "merge_node_id": mergeNodeID, "updated_at": now,
			}
			if err := tx.Model(&Branch{}).Where("id = ?", source.ID).Updates(updates).Error; err != nil {
				return err
			}
			source.State = branchStateMerged
			source.ArchivedAt = &now
			source.MergedAt = &now
			source.MergedIntoBranchID = targetBranchID
			source.MergeNodeID = mergeNodeID
			source.UpdatedAt = now
		}
		if idempotencyKey != "" {
			payload := encodeJSON(map[string]any{
				"merged": true, "merge_node_id": mergeNodeID, "target_branch_id": targetBranchID,
				"target_head_node_id": mergeNodeID, "branch_ids": seenKeys(sources),
			})
			if err := tx.Create(&MergeReceipt{Key: idempotencyKey, ChatID: chatID, Fingerprint: fingerprint, Payload: payload, CreatedAt: now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	for _, source := range sources {
		p.publishBranch(source, "merged")
	}
	p.reply(frame, map[string]any{
		"merged": true, "merge_node_id": mergeNodeID, "target_branch_id": targetBranchID,
		"target_head_node_id": targetHead.HeadNodeID, "target_revision": targetHead.Revision,
		"branch_ids": seenKeys(sources),
	}, nil)
}

func seenKeys(branches []*Branch) []string {
	ids := make([]string, 0, len(branches))
	for _, branch := range branches {
		ids = append(ids, branch.ID)
	}
	return ids
}

// handleBranchesMergeConfirm rejects the retired two-step draft/confirm
// protocol: merge no longer drafts an LLM summary nor dispatches anything.
func (p *Plugin) handleBranchesMergeConfirm(frame busclient.Frame) {
	p.reply(frame, nil, errMergeProtocol)
}

// handleBranchesArchive archives branches WITHOUT merging (the "by the
// way" pattern): a side conversation whose content should stay out of
// every line's history. No merge edge is written, so the branch's turns
// stay reachable only through its own (retained) head — never through
// another line. Archived branches leave the bar and reject dispatch;
// unarchiving is a reserved function — deliberately not implemented yet.
// Busy lines (running batch, queued batches) refuse archiving; empty
// branches may be archived (or deleted outright).
func (p *Plugin) handleBranchesArchive(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	var branchIDs []string
	if err == nil {
		err = decodeInto(value["branch_ids"], &branchIDs)
	}
	if err == nil && (chatID == "" || len(branchIDs) == 0) {
		err = errBadRequest
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	p.automationMu.Lock()
	defer p.automationMu.Unlock()
	now := nowMillis()
	archived := make([]map[string]any, 0, len(branchIDs))
	seen := map[string]bool{}
	for _, id := range branchIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		branch, loadErr := p.store.branch(id)
		if loadErr != nil {
			p.reply(frame, nil, loadErr)
			return
		}
		if branch == nil || branch.ChatID != chatID {
			p.reply(frame, nil, fmt.Errorf("branch was not found in the chat: %s", id))
			return
		}
		if branch.archived() {
			p.reply(frame, nil, fmt.Errorf("分支「%s」%w", branch.Name, errBranchArchived))
			return
		}
		running, runErr := p.store.branchHasRunningTurn(id)
		if runErr != nil {
			p.reply(frame, nil, runErr)
			return
		}
		if running {
			p.reply(frame, nil, fmt.Errorf("分支「%s」%w", branch.Name, errBranchRunning))
			return
		}
		p.mu.Lock()
		busy, reason := p.lineBusyLocked(chatID, id)
		p.mu.Unlock()
		if busy {
			p.reply(frame, nil, fmt.Errorf("分支「%s」%w: %s", branch.Name, errLineBusy, reason))
			return
		}
		branch.ArchivedAt = &now
		branch.State = branchStateArchived
		branch.UpdatedAt = now
		if saveErr := p.store.saveBranch(branch); saveErr != nil {
			p.reply(frame, nil, saveErr)
			return
		}
		p.publishBranch(branch, "archived")
		archived = append(archived, branch.payload())
	}
	p.reply(frame, map[string]any{"archived": true, "branches": archived}, nil)
}
