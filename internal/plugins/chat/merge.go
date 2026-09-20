package chat

// Named parallel branches (framework v0.63+, history-DAG model
// docs/chat-branch-dag-plan.md): creation/rename/delete and the single-step
// atomic merge. Merge is a pure graph graft: one transaction writes a merge
// node (parents = target old head + every source head), advances the target
// head with sessions marked stale, and closes the source lines
// (state=merged). No LLM summary, no dispatched message, no agent wake-up;
// the source branch disappears from every list while its rows, turns, and
// edges stay for queries and debugging. Archiving IS a merge (v0.78): the
// sources graft onto the chat's terminal 归档 line (born on first use), so
// shelved content stays on a real, viewable, forkable line. The retired
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
	errArchiveSink      = errors.New("the 归档 archive line is the terminal sink — it cannot be merged away, archived, or deleted")
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
	// Shelved (archive-only) lines are read-only; a turn on a MERGED line
	// stays forkable — its content joined the merge target's history, so
	// the fork attributes to the surviving line (v0.77).
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
			// Follow the merge chain to the surviving line ("" = mainline
			// when the chain ends there); the hop cap guards a corrupted
			// cyclic chain.
			for hops := 0; parent != nil && parent.archived() && parent.merged(); hops++ {
				if hops >= 16 {
					p.reply(frame, nil, errors.New("the fork turn's merge chain does not terminate"))
					return
				}
				parentBranchID = parent.MergedIntoBranchID
				if parentBranchID == "" {
					parent = nil
					break
				}
				if parent, parentErr = p.store.branch(parentBranchID); parentErr != nil {
					p.reply(frame, nil, parentErr)
					return
				}
			}
			if parent == nil && parentBranchID != "" {
				// Dangling branch record (deleted mid-chain).
				p.reply(frame, nil, errBranchArchived)
				return
			}
			if parent != nil && parent.archived() {
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
	if branch.ID == archiveBranchID(branch.ChatID) {
		p.reply(frame, nil, errArchiveSink)
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

// archiveBranchName is the display name of the chat's archive line.
const archiveBranchName = "归档"

// archiveBranchID is the deterministic id of the chat's archive line — a
// normal open branch acting as the terminal merge sink for shelving
// (v0.78). The deterministic id makes birth-on-first-use idempotent and
// lets the sink guard work without a schema flag.
func archiveBranchID(chatID string) string { return "archive:" + chatID }

// handleBranchesMerge parses the single-step atomic merge RPC
// (docs/chat-branch-dag-plan.md §6) and delegates to mergeLines.
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
	result, mergeErr := p.mergeLines(chatID, branchIDs, targetBranchID, expected, idempotencyKey, false)
	p.reply(frame, result, mergeErr)
}

// mergeLines is the merge core shared by branches:merge and
// branches:archive: one transaction writes the merge node (parents =
// target old head + every source head), advances the target head with its
// sessions marked stale (the next batch rebuilds every session from the
// new full snapshot), and closes the sources (state=merged). Empty
// branches merge fine (their fork node is real history); shared ancestry
// is deduplicated by the graph walk. expected_revisions (line → revision,
// "" key = mainline) makes a stale client fail with a revision conflict
// instead of merging blind; idempotency_key replays return the stored
// result, and a key reused with different parameters is rejected.
// createTarget allows a not-yet-existing target line and births it inside
// the merge transaction (archiving creates the 归档 line on first use), so
// a failed merge leaves no empty target behind.
func (p *Plugin) mergeLines(chatID string, branchIDs []string, targetBranchID string, expected map[string]int64, idempotencyKey string, createTarget bool) (map[string]any, error) {
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
			return nil, lookup.Error
		}
		if lookup.RowsAffected > 0 {
			if receipt.Fingerprint != fingerprint {
				return nil, errors.New("idempotency key reused with a different merge")
			}
			var stored map[string]any
			if json.Unmarshal([]byte(receipt.Payload), &stored) == nil {
				stored["deduplicated"] = true
				return stored, nil
			}
			return map[string]any{"merged": true, "deduplicated": true}, nil
		}
	}
	// Validate sources: same chat, all open (empty branches allowed — their
	// fork node is real history), target open and not among sources. The
	// 归档 line is terminal: merging it away would strand everything shelved
	// into it.
	sources := []*Branch{}
	seen := map[string]bool{}
	for _, id := range branchIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if id == archiveBranchID(chatID) {
			return nil, errArchiveSink
		}
		branch, loadErr := p.store.branch(id)
		if loadErr != nil {
			return nil, loadErr
		}
		if branch == nil || branch.ChatID != chatID {
			return nil, fmt.Errorf("branch was not found in the chat: %s", id)
		}
		if branch.archived() {
			return nil, fmt.Errorf("分支「%s」%w", branch.Name, errBranchArchived)
		}
		sources = append(sources, branch)
	}
	if seen[targetBranchID] {
		return nil, errors.New("the merge target cannot be merged into itself")
	}
	var target *Branch
	if targetBranchID != "" {
		var targetErr error
		target, targetErr = p.store.branch(targetBranchID)
		if targetErr != nil {
			return nil, targetErr
		}
		if target == nil && !createTarget {
			return nil, errors.New("the merge target branch was not found in the chat")
		}
		if target != nil && target.ChatID != chatID {
			return nil, errors.New("the merge target branch was not found in the chat")
		}
		if target != nil && target.archived() {
			return nil, fmt.Errorf("merge target %w", errBranchArchived)
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
				if line == archiveBranchID(chatID) {
					name = "「" + archiveBranchName + "」"
				}
			}
			return nil, fmt.Errorf("%s %w", name, busyErr)
		}
	}
	now := nowMillis()
	mergeNodeID := "merge:" + newID()
	var targetHead *LineHead
	createdTarget := false
	err := p.store.db.Transaction(func(tx *gorm.DB) error {
		if target == nil && targetBranchID != "" {
			// Birth the target line (the 归档 archive sink) inside the
			// merge transaction: a merge that fails later leaves no empty
			// branch behind. ensureLineHead below creates its fork node
			// and head row.
			target = &Branch{ID: targetBranchID, ChatID: chatID, Name: archiveBranchName, State: branchStateOpen, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(target).Error; err != nil {
				return err
			}
			createdTarget = true
		}
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
		return nil, err
	}
	if createdTarget {
		p.publishBranch(target, "created")
	}
	for _, source := range sources {
		p.publishBranch(source, "merged")
	}
	return map[string]any{
		"merged": true, "merge_node_id": mergeNodeID, "target_branch_id": targetBranchID,
		"target_head_node_id": targetHead.HeadNodeID, "target_revision": targetHead.Revision,
		"branch_ids": seenKeys(sources),
	}, nil
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

// handleBranchesArchive retires branches into the chat's archive line:
// archiving IS a merge (v0.78) onto the terminal 归档 line, so shelved
// content keeps living in a real, viewable, forkable history instead of
// dropping out of every line. The 归档 line is born on first use.
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
	target := archiveBranchID(chatID)
	result, mergeErr := p.mergeLines(chatID, branchIDs, target, nil, "", true)
	if mergeErr != nil {
		p.reply(frame, nil, mergeErr)
		return
	}
	result["archived"] = true
	result["archive_branch_id"] = target
	p.reply(frame, result, nil)
}
