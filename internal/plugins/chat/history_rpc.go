package chat

// Read-side history RPC (history-DAG model): the paged debug graph view.
// Line visibility membership is resolved inside the view-filtered
// chats:list / blocks:list paths (view_filter.go) — there is no separate
// keys RPC for the frontend to filter by.

import (
	"viewer/sdk/go/busclient"
)

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
