package chat

// Read-side history RPCs (history-DAG model): the pane's line visibility
// membership (no second branch recursion on the frontend) and the paged
// debug graph view.

import (
	"gorm.io/gorm"

	"viewer/sdk/go/busclient"
)

// handleLineKeys returns the membership set of one line's current head
// snapshot: reachable turn ids, their dispatch ids (user messages key by
// dispatch), and orphan input message ids. The pane unions the sets of its
// selected lines to filter the timeline; streaming/unknown content stays
// visible on top. Works for open, archived, and merged lines alike (heads
// are retained forever).
func (p *Plugin) handleLineKeys(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	branchID := requestString(value, "branch_id")
	if err == nil && chatID == "" {
		err = errBadRequest
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	var head *LineHead
	err = p.store.db.Transaction(func(tx *gorm.DB) error {
		var headErr error
		head, headErr = ensureLineHead(tx, chatID, branchID)
		return headErr
	})
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	snapshot, err := p.store.snapshotTurns(chatID, head.HeadNodeID)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	p.reply(frame, map[string]any{
		"chat_id": chatID, "branch_id": branchID,
		"head_node_id": head.HeadNodeID, "revision": head.Revision,
		"keys": snapshot.keys(),
	}, nil)
}

// handleBranchesGraph pages the chat's history graph for debugging and a
// future DAG view: nodes ordered by the chat-monotonic seq (cursor =
// after_seq), plus the edges touching the returned nodes. Never requires a
// first-screen full download.
func (p *Plugin) handleBranchesGraph(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	afterSeq := requestInt64(value, "after_seq")
	limit := int(requestInt64(value, "limit"))
	if err == nil && chatID == "" {
		err = errBadRequest
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	var nodes []HistoryNode
	if err := p.store.db.Where("chat_id = ? AND seq > ?", chatID, afterSeq).Order("seq").Limit(limit + 1).Find(&nodes).Error; err != nil {
		p.reply(frame, nil, err)
		return
	}
	hasMore := len(nodes) > limit
	if hasMore {
		nodes = nodes[:limit]
	}
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	edges := []HistoryEdge{}
	if len(ids) > 0 {
		if err := p.store.db.Where("child_id IN ? OR parent_id IN ?", ids, ids).Find(&edges).Error; err != nil {
			p.reply(frame, nil, err)
			return
		}
	}
	if edges == nil {
		edges = []HistoryEdge{}
	}
	next := afterSeq
	if len(nodes) > 0 {
		next = nodes[len(nodes)-1].Seq
	}
	p.reply(frame, map[string]any{"chat_id": chatID, "nodes": nodes, "edges": edges, "has_more": hasMore, "next_after_seq": next}, nil)
}
