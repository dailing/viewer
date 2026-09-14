package chat

// History DAG (docs/chat-branch-dag-plan.md): the persistent graph is the
// single authority for "which work belongs to a line". History nodes wrap
// real execution turns (kind=turn) plus pure graph markers (fork / merge /
// join / input); directed edges child→parent express "depends on". Every
// work line (mainline = "" branch id, or a named branch) owns a line_heads
// row pointing at its current head node; a line's history snapshot is the
// exact ancestor closure of that head, deduplicated by node id. Branch
// lifecycle rows remain the authority for operation permissions (open /
// archived / merged); the graph is the authority for history visibility.
// Both are mutated in the same transaction.

import (
	"errors"
	"fmt"
	"sort"

	"gorm.io/gorm"
)

// Node kinds.
const (
	nodeKindTurn  = "turn"  // wraps one real agent execution turn (TurnID = turns.id)
	nodeKindFork  = "fork"  // line genesis / fork point marker (no content)
	nodeKindMerge = "merge" // merge marker: parents = target old head + source heads
	nodeKindJoin  = "join"  // multi-role batch收束 marker: parents = the batch's turn nodes
	nodeKindInput = "input" // legacy orphan user input with no agent turn (TurnID = messages.id)
)

// Edge kinds (informational; reachability uses the edge alone).
const (
	edgeKindSequence = "sequence"
	edgeKindFork     = "fork"
	edgeKindMerge    = "merge"
	edgeKindJoin     = "join"
)

// Branch lifecycle states (Branch.State).
const (
	branchStateOpen     = "open"
	branchStateArchived = "archived" // shelved without merging: joins no line's history
	branchStateMerged   = "merged"   // grafted into a target line; the line disappears everywhere
)

type HistoryNode struct {
	ID             string `gorm:"primaryKey" json:"id"`
	ChatID         string `gorm:"index;not null" json:"chat_id"`
	Kind           string `gorm:"not null" json:"kind"`
	OriginBranchID string `gorm:"index" json:"origin_branch_id"`
	// TurnID references the wrapped object: turns.id for kind=turn,
	// messages.id for kind=input, empty for pure markers.
	TurnID    string `gorm:"index" json:"turn_id,omitempty"`
	CreatedAt int64  `json:"created_at"`
	Seq       int64  `gorm:"index" json:"seq"` // chat-monotonic insertion order
}

func (HistoryNode) TableName() string { return "history_nodes" }

type HistoryEdge struct {
	ChildID  string `gorm:"primaryKey" json:"child_id"`
	ParentID string `gorm:"primaryKey" json:"parent_id"`
	Kind     string `gorm:"not null" json:"kind"`
}

func (HistoryEdge) TableName() string { return "history_edges" }

// LineHead is one work line's mutable pointer into the DAG. The mainline
// always has its own row (BranchID ""). Revision is bumped on every head
// move (CAS against lost updates); SessionsStale marks the line's existing
// agent sessions for rebuild before the next run (set by merge, cleared
// when the next batch starts fresh sessions).
type LineHead struct {
	ChatID        string `gorm:"primaryKey" json:"chat_id"`
	BranchID      string `gorm:"primaryKey" json:"branch_id"`
	HeadNodeID    string `json:"head_node_id"`
	Revision      int64  `json:"revision"`
	SessionsStale bool   `json:"sessions_stale"`
}

func (LineHead) TableName() string { return "line_heads" }

// DispatchSnapshot records one execution batch's fixed input: every prompt
// of the batch builds from the ancestor closure of InputNodeID, never from
// a later head. BatchID distinguishes the immediate batch and later
// queued-start batches of the same dispatch.
type DispatchSnapshot struct {
	BatchID       string `gorm:"primaryKey"`
	DispatchID    string `gorm:"index;not null"`
	ChatID        string `gorm:"index;not null"`
	BranchID      string // line the batch advances ("" = mainline)
	InputNodeID   string
	InputRevision int64
	CreatedAt     int64
}

func (DispatchSnapshot) TableName() string { return "dispatch_snapshots" }

// MergeReceipt is the idempotency record of a branches:merge call: same key
// + same fingerprint returns the stored payload; same key + different
// fingerprint is rejected.
type MergeReceipt struct {
	Key         string `gorm:"primaryKey"`
	ChatID      string `gorm:"index;not null"`
	Fingerprint string
	Payload     string
	CreatedAt   int64
}

func (MergeReceipt) TableName() string { return "merge_receipts" }

// ContextBuild records one context construction for diagnostics: which head
// snapshot it drew from and how each reachable turn was represented
// (summarized / raw / omitted by budget).
type ContextBuild struct {
	ID         string `gorm:"primaryKey"`
	ChatID     string `gorm:"index;not null"`
	BranchID   string
	DispatchID string `gorm:"index"`
	Kind       string // fresh | bridge
	HeadNodeID string
	Coverage   string // JSON {summarized:[], raw:[], omitted:[]}
	CreatedAt  int64
}

func (ContextBuild) TableName() string { return "context_builds" }

// Deterministic node ids keep publishing idempotent across retries and
// crash recovery.
func turnNodeID(turnID string) string            { return turnID }
func forkNodeID(branchID string) string          { return "fork:" + branchID }
func inputNodeID(messageID string) string        { return "input:" + messageID }
func joinNodeID(batchID, branchID string) string { return "join:" + batchID + ":" + branchID }
func mainlineRootNodeID(chatID string) string    { return "root:" + chatID }

var errGraphCorrupt = errors.New("history graph integrity violation")

// nextNodeSeq assigns the chat-monotonic sequence inside a transaction.
func nextNodeSeq(tx *gorm.DB, chatID string) (int64, error) {
	var seq int64
	err := tx.Model(&HistoryNode{}).Where("chat_id = ?", chatID).Select("COALESCE(MAX(seq), 0) + 1").Scan(&seq).Error
	return seq, err
}

// addNode inserts one node plus its parent edges (child→parent, same chat,
// parents must already exist — the graph only ever references committed
// nodes, so it is acyclic by construction).
func addNode(tx *gorm.DB, node *HistoryNode, parents []string, edgeKind string) error {
	seq, err := nextNodeSeq(tx, node.ChatID)
	if err != nil {
		return err
	}
	node.Seq = seq
	if err := tx.Create(node).Error; err != nil {
		return err
	}
	for _, parent := range parents {
		if parent == "" || parent == node.ID {
			return fmt.Errorf("%w: invalid parent %q for node %q", errGraphCorrupt, parent, node.ID)
		}
		if err := tx.Create(&HistoryEdge{ChildID: node.ID, ParentID: parent, Kind: edgeKind}).Error; err != nil {
			return err
		}
	}
	return nil
}

// ensureLineHead loads the line's head row, creating the line's genesis
// node (a rootless fork marker) on first touch. Mainline genesis is shared
// root:<chat>; branch genesis is fork:<branch>.
func ensureLineHead(tx *gorm.DB, chatID, branchID string) (*LineHead, error) {
	var head LineHead
	result := tx.Limit(1).Find(&head, "chat_id = ? AND branch_id = ?", chatID, branchID)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected > 0 {
		return &head, nil
	}
	nodeID := mainlineRootNodeID(chatID)
	if branchID != "" {
		nodeID = forkNodeID(branchID)
	}
	node := &HistoryNode{ID: nodeID, ChatID: chatID, Kind: nodeKindFork, OriginBranchID: branchID, CreatedAt: nowMillis()}
	if err := addNode(tx, node, nil, ""); err != nil {
		return nil, err
	}
	head = LineHead{ChatID: chatID, BranchID: branchID, HeadNodeID: nodeID, Revision: 1}
	if err := tx.Create(&head).Error; err != nil {
		return nil, err
	}
	return &head, nil
}

func (s *store) lineHead(chatID, branchID string) (*LineHead, error) {
	var head LineHead
	result := s.db.Limit(1).Find(&head, "chat_id = ? AND branch_id = ?", chatID, branchID)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &head, nil
}

// advanceLineHead moves the line head to newHead with a revision CAS. The
// node/edges must already be committed (or committed in the same tx).
func advanceLineHead(tx *gorm.DB, chatID, branchID, newHead string) error {
	result := tx.Model(&LineHead{}).
		Where("chat_id = ? AND branch_id = ?", chatID, branchID).
		Updates(map[string]any{"head_node_id": newHead, "revision": gorm.Expr("revision + 1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: no line head for %s/%s", errGraphCorrupt, chatID, branchID)
	}
	return nil
}

// historySnapshot is the resolved ancestor closure of one head: the real
// turns (time-ordered) plus the orphan input message ids. All pagination
// and context building binds to one snapshot — never re-derived from branch
// metadata or time cutoffs.
type historySnapshot struct {
	HeadNodeID      string
	Turns           []Turn
	InputMessageIDs []string
}

// snapshotTurns resolves the exact ancestor closure of headNodeID: every
// reachable turn node (projected to its Turn row, ordered by started_at+id)
// and every reachable input node's message id. Dangling edges, cross-chat
// edges, or edges referencing missing nodes are explicit errors — a
// degraded context is never silently produced.
func (s *store) snapshotTurns(chatID, headNodeID string) (*historySnapshot, error) {
	if headNodeID == "" {
		return &historySnapshot{}, nil
	}
	type row struct {
		ID     string
		Kind   string
		TurnID string
	}
	var nodes []row
	// SQLite recursive CTE: UNION dedups node ids, so diamonds are visited
	// once and the walk terminates even on (impossible-by-construction) cycles.
	ancestorWalk := `WITH RECURSIVE anc(id) AS (
		SELECT ?
		UNION
		SELECT e.parent_id FROM history_edges e JOIN anc ON e.child_id = anc.id
	)`
	if err := s.db.Raw(ancestorWalk+`
		SELECT n.id, n.kind, n.turn_id FROM history_nodes n
		JOIN anc ON n.id = anc.id WHERE n.chat_id = ?`, headNodeID, chatID).Scan(&nodes).Error; err != nil {
		return nil, err
	}
	var edges []HistoryEdge
	if err := s.db.Raw(ancestorWalk+`
		SELECT e.child_id, e.parent_id, e.kind FROM history_edges e
		JOIN anc ON e.child_id = anc.id`, headNodeID).Scan(&edges).Error; err != nil {
		return nil, err
	}
	reachable := map[string]bool{}
	nodeByID := map[string]row{}
	for _, node := range nodes {
		reachable[node.ID] = true
		nodeByID[node.ID] = node
	}
	for _, edge := range edges {
		if !reachable[edge.ParentID] {
			return nil, fmt.Errorf("%w: edge %s → %s reaches outside the snapshot", errGraphCorrupt, edge.ChildID, edge.ParentID)
		}
		if _, ok := nodeByID[edge.ParentID]; !ok {
			return nil, fmt.Errorf("%w: edge %s → %s references a missing node", errGraphCorrupt, edge.ChildID, edge.ParentID)
		}
	}
	turnIDs := make([]string, 0, len(nodes))
	inputIDs := []string{}
	for _, node := range nodes {
		switch node.Kind {
		case nodeKindTurn:
			if node.TurnID != "" {
				turnIDs = append(turnIDs, node.TurnID)
			}
		case nodeKindInput:
			if node.TurnID != "" {
				inputIDs = append(inputIDs, node.TurnID)
			}
		}
	}
	snapshot := &historySnapshot{HeadNodeID: headNodeID, InputMessageIDs: inputIDs}
	if len(turnIDs) > 0 {
		var turns []Turn
		if err := s.db.Where("id IN ?", turnIDs).Find(&turns).Error; err != nil {
			return nil, err
		}
		sort.SliceStable(turns, func(i, j int) bool {
			if turns[i].StartedAt != turns[j].StartedAt {
				return turns[i].StartedAt < turns[j].StartedAt
			}
			return turns[i].ID < turns[j].ID
		})
		snapshot.Turns = turns
	}
	return snapshot, nil
}

// snapshotKeys projects a snapshot to the membership set the UI filters by:
// turn ids (assistant messages, blocks), dispatch ids (user messages carry
// the dispatch id as their turn_id), and orphan input message ids.
func (snapshot *historySnapshot) keys() []string {
	keys := make([]string, 0, len(snapshot.Turns)*2+len(snapshot.InputMessageIDs))
	seen := map[string]bool{}
	for _, turn := range snapshot.Turns {
		if !seen[turn.ID] {
			seen[turn.ID] = true
			keys = append(keys, turn.ID)
		}
		if turn.DispatchID != "" && !seen[turn.DispatchID] {
			seen[turn.DispatchID] = true
			keys = append(keys, turn.DispatchID)
		}
	}
	for _, id := range snapshot.InputMessageIDs {
		if !seen[id] {
			seen[id] = true
			keys = append(keys, id)
		}
	}
	return keys
}
