package chat

// One-shot, versioned migration from the branch-metadata model (turns +
// prev_turn_id + merge messages) to the authoritative history DAG
// (docs/chat-branch-dag-plan.md §9). It runs once at plugin construction:
// the database is backed up first (VACUUM INTO), every line's event
// sequence is rebuilt — fork nodes at fork points, merge nodes spliced at
// their historical position in the target line, input nodes for orphan
// user inputs — heads are created for every line, and the result is
// validated before the graph read path is marked active. Old columns and
// messages are never deleted; legacy-inferred spots are reported, never
// silently guessed.

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"time"

	"gorm.io/gorm"
)

const historyGraphVersion = "1"

// migrateHistoryGraph runs the DAG migration when the store predates it.
// Idempotent: the version key is written only after a fully validated run,
// and a chat that already has history nodes is skipped on retry.
func migrateHistoryGraph(s *store, dataDir string) error {
	var state PluginState
	result := s.db.Limit(1).Find(&state, "key = ?", "history_graph_version")
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 && state.Value == historyGraphVersion {
		return nil
	}
	backup := filepath.Join(dataDir, fmt.Sprintf("chat-pre-dag-%s.sqlite3", time.Now().Format("20060102-150405")))
	if err := s.db.Exec("VACUUM INTO ?", backup).Error; err != nil {
		return fmt.Errorf("history graph migration backup failed: %w", err)
	}
	slog.Info("chat history graph migration: database backed up", "backup", backup)

	chats, err := s.chats()
	if err != nil {
		return err
	}
	report := &migrationReport{}
	for _, chat := range chats {
		if err := s.migrateChatGraph(chat.ID, report); err != nil {
			return fmt.Errorf("history graph migration failed for chat %s: %w", chat.ID, err)
		}
	}
	if err := s.validateGraph(report); err != nil {
		return err
	}
	if err := s.db.Save(&PluginState{Key: "history_graph_version", Value: historyGraphVersion}).Error; err != nil {
		return err
	}
	report.log()
	return nil
}

type migrationReport struct {
	chats, lines, nodes, edges, merges, inputs int
	anomalies                                  []string
}

func (r *migrationReport) anomaly(format string, args ...any) {
	if len(r.anomalies) >= 100 {
		return
	}
	r.anomalies = append(r.anomalies, fmt.Sprintf(format, args...))
}

func (r *migrationReport) log() {
	slog.Info("chat history graph migration completed",
		"chats", r.chats, "lines", r.lines, "nodes", r.nodes, "edges", r.edges,
		"merge_nodes", r.merges, "input_nodes", r.inputs, "anomalies", len(r.anomalies))
	for _, line := range r.anomalies {
		slog.Warn("chat history graph migration anomaly", "detail", line)
	}
}

// chainBuilder accumulates one line's ordered node list plus the extra
// (non-chain) parent edges of merge nodes, so the whole chat graph is
// composed in memory and written in one transaction.
type chainBuilder struct {
	nodes     []HistoryNode       // final chain order per line, keyed below
	chain     map[string][]string // branch line → ordered node ids
	nodeByID  map[string]*HistoryNode
	extraEdge []HistoryEdge // merge-node → source-head edges etc.
	seq       map[string]int64
}

func newChainBuilder() *chainBuilder {
	return &chainBuilder{chain: map[string][]string{}, nodeByID: map[string]*HistoryNode{}, seq: map[string]int64{}}
}

func (b *chainBuilder) add(node HistoryNode) {
	stored := node
	b.nodeByID[node.ID] = &stored
	b.nodes = append(b.nodes, stored)
}

// splice inserts nodeID into the line's chain before the first node newer
// than at (node times: turn nodes carry the turn's start, markers their
// event time), appending at the end when nothing is newer.
func (b *chainBuilder) splice(line, nodeID string, at int64) {
	chain := b.chain[line]
	index := len(chain)
	for i, id := range chain {
		if b.nodeByID[id].CreatedAt > at {
			index = i
			break
		}
	}
	chain = append(chain, "")
	copy(chain[index+1:], chain[index:])
	chain[index] = nodeID
	b.chain[line] = chain
}

func (s *store) migrateChatGraph(chatID string, report *migrationReport) error {
	var existing int64
	if err := s.db.Model(&HistoryNode{}).Where("chat_id = ?", chatID).Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		slog.Info("chat history graph migration: chat already migrated, skipping", "chat_id", chatID, "nodes", existing)
		return nil
	}
	turns, err := s.chatTurns(chatID)
	if err != nil {
		return err
	}
	branches, err := s.chatBranches(chatID)
	if err != nil {
		return err
	}
	branchByID := map[string]*Branch{}
	for i := range branches {
		branchByID[branches[i].ID] = &branches[i]
	}
	// Only terminated turns enter published history; in-flight rows are
	// crash leftovers the startup recovery finalizes.
	ended := map[string]Turn{}
	byLine := map[string][]Turn{}
	for _, turn := range turns {
		if turn.EndedAt == nil {
			report.anomaly("chat %s: turn %s has no ended_at at migration; left for startup recovery", chatID, turn.ID)
			continue
		}
		ended[turn.ID] = turn
		byLine[turn.BranchID] = append(byLine[turn.BranchID], turn)
	}
	for line := range byLine {
		sort.SliceStable(byLine[line], func(i, j int) bool {
			if byLine[line][i].StartedAt != byLine[line][j].StartedAt {
				return byLine[line][i].StartedAt < byLine[line][j].StartedAt
			}
			return byLine[line][i].ID < byLine[line][j].ID
		})
	}

	builder := newChainBuilder()
	// Mainline: root marker + the mainline turn chain.
	root := HistoryNode{ID: mainlineRootNodeID(chatID), ChatID: chatID, Kind: nodeKindFork, OriginBranchID: "", CreatedAt: 0}
	builder.add(root)
	builder.chain[""] = []string{root.ID}
	for _, turn := range byLine[""] {
		node := HistoryNode{ID: turnNodeID(turn.ID), ChatID: chatID, Kind: nodeKindTurn, OriginBranchID: "", TurnID: turn.ID, CreatedAt: turn.StartedAt}
		builder.add(node)
		builder.chain[""] = append(builder.chain[""], node.ID)
	}
	// Branches: fork marker (parent = the fork turn's node when recoverable)
	// + the branch turn chain. Merged branches keep their own chain/head —
	// the merge node references it; nothing is rewritten.
	for i := range branches {
		branch := &branches[i]
		fork := HistoryNode{ID: forkNodeID(branch.ID), ChatID: chatID, Kind: nodeKindFork, OriginBranchID: branch.ID, CreatedAt: branch.CreatedAt}
		builder.add(fork)
		builder.chain[branch.ID] = []string{fork.ID}
		if branch.ForkTurnID != "" {
			if _, ok := ended[branch.ForkTurnID]; ok {
				builder.extraEdge = append(builder.extraEdge, HistoryEdge{ChildID: fork.ID, ParentID: turnNodeID(branch.ForkTurnID), Kind: edgeKindFork})
			} else {
				report.anomaly("chat %s: branch %s fork turn %s missing or unfinished; fork node left rootless (legacy inference)", chatID, branch.ID, branch.ForkTurnID)
			}
		}
		for _, turn := range byLine[branch.ID] {
			node := HistoryNode{ID: turnNodeID(turn.ID), ChatID: chatID, Kind: nodeKindTurn, OriginBranchID: branch.ID, TurnID: turn.ID, CreatedAt: turn.StartedAt}
			builder.add(node)
			builder.chain[branch.ID] = append(builder.chain[branch.ID], node.ID)
		}
	}
	// Merge nodes: one per legacy merge message (a confirm archived each
	// source with the same merge_message_id), spliced into the target
	// chain at the message time. The old summary message stays ordinary
	// history; only the UI card goes away.
	mergeGroups := map[string][]*Branch{}
	for i := range branches {
		branch := &branches[i]
		if branch.ArchivedAt != nil && branch.MergeMessageID != "" {
			mergeGroups[branch.MergeMessageID] = append(mergeGroups[branch.MergeMessageID], branch)
		}
	}
	// Merge nodes splice into the target chain at the message time. They
	// must be grafted CHRONOLOGICALLY: a nested merge (C into B) lands on
	// B's chain before the outer merge (B into mainline) references B's
	// head, or the outer edge would point at a stale head and lose the
	// inner content. (The legacy model archived a branch at its merge, so
	// an inner merge always predates its outer one.)
	messageIDs := make([]string, 0, len(mergeGroups))
	mergeTimes := map[string]int64{}
	for messageID, group := range mergeGroups {
		mergeTime := *group[0].ArchivedAt
		if message, msgErr := s.message(messageID); msgErr == nil && message != nil {
			mergeTime = message.CreatedAt
		} else {
			report.anomaly("chat %s: merge message %s missing; merge node timed by archive time (legacy inference)", chatID, messageID)
		}
		mergeTimes[messageID] = mergeTime
		messageIDs = append(messageIDs, messageID)
	}
	sort.Slice(messageIDs, func(i, j int) bool {
		if mergeTimes[messageIDs[i]] != mergeTimes[messageIDs[j]] {
			return mergeTimes[messageIDs[i]] < mergeTimes[messageIDs[j]]
		}
		return messageIDs[i] < messageIDs[j]
	})
	for _, messageID := range messageIDs {
		group := mergeGroups[messageID]
		target := group[0].MergedIntoBranchID
		mergeTime := mergeTimes[messageID]
		node := HistoryNode{ID: "merge:" + messageID, ChatID: chatID, Kind: nodeKindMerge, OriginBranchID: target, CreatedAt: mergeTime}
		builder.add(node)
		for _, source := range group {
			sourceChain := builder.chain[source.ID]
			if len(sourceChain) == 0 {
				report.anomaly("chat %s: merged branch %s has no chain; skipped as merge source", chatID, source.ID)
				continue
			}
			builder.extraEdge = append(builder.extraEdge, HistoryEdge{ChildID: node.ID, ParentID: sourceChain[len(sourceChain)-1], Kind: edgeKindMerge})
			source.State = branchStateMerged
			source.MergedAt = source.ArchivedAt
			source.MergeNodeID = node.ID
		}
		builder.splice(target, node.ID, mergeTime)
		report.merges++
	}
	// Input nodes: visible user inputs that produced no turn (failed or
	// interrupted dispatches) must stay addressable history. Line: the
	// dispatch receipt's branch when recorded, else mainline (legacy).
	var userMessages []Message
	if err := s.db.Where("chat_id = ? AND role = ?", chatID, "user").Order("created_at, id").Find(&userMessages).Error; err != nil {
		return err
	}
	dispatchWithTurns := map[string]bool{}
	for _, turn := range turns {
		if turn.DispatchID != "" {
			dispatchWithTurns[turn.DispatchID] = true
		}
	}
	for _, message := range userMessages {
		if dispatchWithTurns[message.TurnID] {
			continue
		}
		line := ""
		var receipt DispatchReceipt
		if lookup := s.db.Where("dispatch_id = ?", message.TurnID).Limit(1).Find(&receipt); lookup.Error == nil && lookup.RowsAffected > 0 {
			line = receipt.BranchID
			if branch, ok := branchByID[line]; ok && branch.State == branchStateMerged {
				report.anomaly("chat %s: orphan input %s targeted merged branch %s; attached to mainline instead", chatID, message.ID, line)
				line = ""
			}
		}
		node := HistoryNode{ID: inputNodeID(message.ID), ChatID: chatID, Kind: nodeKindInput, OriginBranchID: line, TurnID: message.ID, CreatedAt: message.CreatedAt}
		builder.add(node)
		if _, ok := builder.chain[line]; !ok {
			report.anomaly("chat %s: orphan input %s references unknown line %q; attached to mainline instead", chatID, message.ID, line)
			line = ""
		}
		builder.splice(line, node.ID, message.CreatedAt)
		report.inputs++
	}
	// Remaining branch states: archived-without-merge vs open.
	for i := range branches {
		branch := &branches[i]
		if branch.State == "" {
			if branch.ArchivedAt != nil {
				branch.State = branchStateArchived
			} else {
				branch.State = branchStateOpen
			}
		}
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		// Nodes in stable (time, id) order — seq is a diagnostic cursor,
		// never a reachability signal.
		sort.SliceStable(builder.nodes, func(i, j int) bool {
			if builder.nodes[i].CreatedAt != builder.nodes[j].CreatedAt {
				return builder.nodes[i].CreatedAt < builder.nodes[j].CreatedAt
			}
			return builder.nodes[i].ID < builder.nodes[j].ID
		})
		for index := range builder.nodes {
			node := builder.nodes[index]
			node.Seq = int64(index + 1)
			if err := tx.Create(&node).Error; err != nil {
				return err
			}
			report.nodes++
		}
		// Chain edges: consecutive nodes per line (first element is the
		// line's root marker, parentless or fork-edged via extraEdge).
		for line, chain := range builder.chain {
			for i := 1; i < len(chain); i++ {
				kind := edgeKindSequence
				if i == 1 && builder.nodeByID[chain[0]].Kind == nodeKindFork && line != "" {
					kind = edgeKindFork
				}
				if err := tx.Create(&HistoryEdge{ChildID: chain[i], ParentID: chain[i-1], Kind: kind}).Error; err != nil {
					return err
				}
				report.edges++
			}
		}
		for _, edge := range builder.extraEdge {
			if err := tx.Create(&edge).Error; err != nil {
				return err
			}
			report.edges++
		}
		// Heads: every line (mainline included) gets an explicit row.
		for line, chain := range builder.chain {
			head := LineHead{ChatID: chatID, BranchID: line, HeadNodeID: chain[len(chain)-1], Revision: 1}
			if err := tx.Create(&head).Error; err != nil {
				return err
			}
			report.lines++
		}
		for i := range branches {
			if err := tx.Model(&Branch{}).Where("id = ?", branches[i].ID).
				Updates(map[string]any{"state": branches[i].State, "merged_at": branches[i].MergedAt, "merge_node_id": branches[i].MergeNodeID}).Error; err != nil {
				return err
			}
		}
		report.chats++
		return nil
	})
}

// validateGraph re-reads every head's snapshot: the ancestor walk must
// resolve without integrity errors and must cover exactly the chat's
// terminated turns (no loss, no duplication) plus its orphan inputs.
func (s *store) validateGraph(report *migrationReport) error {
	var heads []LineHead
	if err := s.db.Find(&heads).Error; err != nil {
		return err
	}
	for _, head := range heads {
		if _, err := s.snapshotTurns(head.ChatID, head.HeadNodeID); err != nil {
			return fmt.Errorf("history graph validation failed for %s/%s: %w", head.ChatID, head.BranchID, err)
		}
	}
	return nil
}
