package chat

// View-scoped history filtering: branch visibility is resolved server-side.
// The pane asks for one line set's visible slice (chats:list / blocks:list
// with branch_ids) and the backend filters by the lines' snapshot
// membership — the frontend renders what it receives, holds no closure
// state, and has_more is view-scoped (no invisible-page walks, no
// chat-level pagination anchors mismatched with line-level visibility).

import (
	"sort"

	"gorm.io/gorm"
)

// viewKeys is the resolved membership of a set of lines (branch ids; "" =
// the main line): messageKeys matches Message.TurnID (turn ids and dispatch
// ids) and Message.ID (orphan inputs); turnKeys matches MessageBlock.TurnID.
type viewKeys struct {
	messageKeys map[string]bool
	turnKeys    map[string]bool
}

func (keys *viewKeys) addTurn(turn Turn) {
	keys.turnKeys[turn.ID] = true
	keys.messageKeys[turn.ID] = true
	if turn.DispatchID != "" {
		keys.messageKeys[turn.DispatchID] = true
	}
}

func (keys *viewKeys) seesMessage(message *Message) bool {
	return keys.messageKeys[message.TurnID] || keys.messageKeys[message.ID]
}

// viewLineKeys resolves the union membership of the given lines: each line
// head's snapshot closure PLUS the lines' in-flight turns (unended — their
// history nodes publish only at turn end) and queued dispatches (whose user
// message already sits on the timeline).
func (p *Plugin) viewLineKeys(chatID string, branchIDs []string) (*viewKeys, error) {
	keys := &viewKeys{messageKeys: map[string]bool{}, turnKeys: map[string]bool{}}
	for _, branchID := range branchIDs {
		var head *LineHead
		err := p.store.db.Transaction(func(tx *gorm.DB) error {
			var headErr error
			head, headErr = ensureLineHead(tx, chatID, branchID)
			return headErr
		})
		if err != nil {
			return nil, err
		}
		snapshot, err := p.store.snapshotTurns(chatID, head.HeadNodeID)
		if err != nil {
			return nil, err
		}
		for _, turn := range snapshot.Turns {
			keys.addTurn(turn)
		}
		for _, id := range snapshot.InputMessageIDs {
			keys.messageKeys[id] = true
		}
	}
	// In-flight turns of the selected lines: their nodes publish at turn
	// end, but their streaming messages/blocks are live on the line now.
	var running []Turn
	if err := p.store.db.Where("chat_id = ? AND branch_id IN ? AND ended_at IS NULL", chatID, branchIDs).Find(&running).Error; err != nil {
		return nil, err
	}
	for _, turn := range running {
		keys.addTurn(turn)
	}
	// Queued dispatches on the selected lines: the user message is already
	// visible; the turn starts when the line frees up.
	p.mu.Lock()
	for _, queue := range p.queues {
		for _, entry := range queue {
			if entry.chatID != chatID || !branchIDListed(branchIDs, entry.branchID) {
				continue
			}
			keys.messageKeys[entry.dispatchID] = true
			keys.messageKeys[entry.messageID] = true
		}
	}
	p.mu.Unlock()
	return keys, nil
}

func branchIDListed(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// requestStrings reads a string-array field from an RPC payload.
func requestStrings(request map[string]any, field string) []string {
	raw, ok := request[field].([]any)
	if !ok {
		return nil
	}
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		if value, ok := item.(string); ok {
			values = append(values, value)
		}
	}
	return values
}

// viewScanChunk is the chat-wide scan granularity of view-filtered paging:
// rows are scanned newest/oldest-first in chunks until the page fills with
// visible messages or the chat's history is exhausted.
const viewScanChunk = 500

// historyPageView is historyPage restricted to a view's membership: same
// composite-cursor semantics, but the store scans chat-wide chunks newest
// first and keeps only visible rows, so hasMore reports whether OLDER
// VISIBLE messages exist — never an invisible-page walk.
func (s *store) historyPageView(chatID string, keys *viewKeys, beforeTs int64, beforeID string, limit int) ([]Message, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	cursorTs, cursorID := beforeTs, beforeID
	page := make([]Message, 0, limit+1)
	for len(page) <= limit {
		query := s.db.Where("chat_id = ?", chatID)
		if cursorTs > 0 {
			if cursorID != "" {
				query = query.Where("(created_at < ? OR (created_at = ? AND id < ?))", cursorTs, cursorTs, cursorID)
			} else {
				query = query.Where("created_at < ?", cursorTs)
			}
		}
		var rows []Message
		if err := query.Order("created_at desc, id desc").Limit(viewScanChunk).Find(&rows).Error; err != nil {
			return nil, false, err
		}
		for index := range rows {
			if keys.seesMessage(&rows[index]) {
				page = append(page, rows[index])
			}
		}
		if len(rows) < viewScanChunk {
			break // history exhausted
		}
		last := rows[len(rows)-1]
		cursorTs, cursorID = last.CreatedAt, last.ID
	}
	hasMore := len(page) > limit
	if hasMore {
		page = page[:limit]
	}
	for left, right := 0, len(page)-1; left < right; left, right = left+1, right-1 {
		page[left], page[right] = page[right], page[left]
	}
	return page, hasMore, nil
}

// historyPageAfterView is historyPageAfter restricted to a view's
// membership: the incremental (delta-refresh / catch-up) half of
// view-scoped paging. hasMore reports whether even NEWER visible messages
// exist beyond the page.
func (s *store) historyPageAfterView(chatID string, keys *viewKeys, afterTs int64, afterID string, limit int) ([]Message, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	cursorTs, cursorID := afterTs, afterID
	page := make([]Message, 0, limit+1)
	for len(page) <= limit {
		query := s.db.Where("chat_id = ?", chatID)
		if cursorTs > 0 {
			if cursorID != "" {
				query = query.Where("(created_at > ? OR (created_at = ? AND id >= ?))", cursorTs, cursorTs, cursorID)
			} else {
				query = query.Where("created_at >= ?", cursorTs)
			}
		}
		var rows []Message
		if err := query.Order("created_at asc, id asc").Limit(viewScanChunk).Find(&rows).Error; err != nil {
			return nil, false, err
		}
		for index := range rows {
			if keys.seesMessage(&rows[index]) {
				page = append(page, rows[index])
			}
		}
		if len(rows) < viewScanChunk {
			break
		}
		last := rows[len(rows)-1]
		cursorTs, cursorID = last.CreatedAt, last.ID
	}
	hasMore := len(page) > limit
	if hasMore {
		page = page[:limit]
	}
	return page, hasMore, nil
}

// lineMessageCounts returns every line's settled visible-message count
// (main under ""): each line head's snapshot membership summed over
// per-turn_id message counts, plus orphan inputs. In-flight turns and
// queued dispatches are excluded — counts track persisted history.
func (p *Plugin) lineMessageCounts(chatID string) (map[string]int, error) {
	type turnCount struct {
		TurnID string
		N      int
	}
	var rows []turnCount
	if err := p.store.db.Model(&Message{}).Select("turn_id, COUNT(*) AS n").Where("chat_id = ?", chatID).Group("turn_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	perKey := make(map[string]int, len(rows))
	for _, row := range rows {
		perKey[row.TurnID] = row.N
	}
	var heads []LineHead
	if err := p.store.db.Where("chat_id = ?", chatID).Find(&heads).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(heads))
	for _, head := range heads {
		snapshot, err := p.store.snapshotTurns(chatID, head.HeadNodeID)
		if err != nil {
			return nil, err
		}
		count := len(snapshot.InputMessageIDs)
		for _, turn := range snapshot.Turns {
			count += perKey[turn.ID] + perKey[turn.DispatchID]
		}
		counts[head.BranchID] = count
	}
	return counts, nil
}

// sortedBranchIDs normalizes a request's branch id list (dedupe + sort) so
// equivalent views resolve identically.
func sortedBranchIDs(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
