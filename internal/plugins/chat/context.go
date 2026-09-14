package chat

// Snapshot-scoped context building (history-DAG model,
// docs/chat-branch-dag-plan.md §7). The persistent DAG is the authority for
// history membership: every prompt builds from the exact ancestor closure
// of its batch's recorded input head — never from branch metadata recursion
// or time cutoffs. On top of the snapshot the builder applies budgets:
// summaries may only replace the turns they verifiably cover (a summary
// covers exactly its own turn), raw tail candidates are the snapshot's
// uncovered messages, and every build reports which turns were injected
// summarized / raw / omitted by budget (ContextBuild rows).

import (
	"fmt"
	"log/slog"
	"math"
	"strings"
)

// contextCoverage records how a snapshot's turns were represented in one
// built prompt (JSON-persisted on the ContextBuild row).
type contextCoverage struct {
	Summarized []string `json:"summarized"`
	Raw        []string `json:"raw"`
	Omitted    []string `json:"omitted"`
}

// buildLineContext is the new-session context over the batch's fixed input
// snapshot: summaries + raw tail come from the snapshot's exact turn set,
// so a branch inherits precisely its fork ancestry and a post-merge line
// reads the grafted history as if the work never left it.
func (p *Plugin) buildLineContext(chat Chat, inputNodeID, branchID, query string, occurredAt int64, dispatchID string) string {
	config := p.summaryConfig(p.ctx)
	if !config.ContextEnabled || inputNodeID == "" {
		return ""
	}
	snapshot, err := p.store.snapshotTurns(chat.ID, inputNodeID)
	if err != nil {
		slog.Warn("chat context snapshot failed", "chat_id", chat.ID, "head", inputNodeID, "error", err)
		return ""
	}
	coverage := &contextCoverage{Summarized: []string{}, Raw: []string{}, Omitted: []string{}}
	summaries, covered := p.snapshotSummariesSection(snapshot, 0, config.SummaryCharBudget, "")
	tail, raw, omitted := p.snapshotTailSection(chat.ID, snapshot, covered, 0, config.TailWordBudget, config.TailByteBudget)
	coverage.Summarized, coverage.Raw, coverage.Omitted = setKeys(covered), raw, omitted
	sections := nonEmpty(summaries, tail)
	if recall := p.buildHindsightRecallSection(chat.ID, query, lastString(sections), occurredAt); recall != "" {
		sections = append(sections, recall)
	}
	p.recordContextBuild(chat.ID, branchID, dispatchID, "fresh", inputNodeID, coverage)
	return capRecentContext(strings.Join(sections, "\n\n"), config.ContextByteBudget)
}

// buildLineBridge is the snapshot-scoped role-switch bridge: "while you
// were away" measures activity within the snapshot's turn set. Per-line
// batch serialization makes the time floor exact here — content that
// entered the line since the role last ran is genuinely newer; content
// grafted by a merge never takes this path (merge marks the line's sessions
// stale, forcing a rebuild with the full fresh context instead).
func (p *Plugin) buildLineBridge(chat Chat, inputNodeID, branchID, roleID, query string, occurredAt int64, dispatchID string) string {
	config := p.summaryConfig(p.ctx)
	if !config.ContextEnabled || inputNodeID == "" {
		return ""
	}
	snapshot, err := p.store.snapshotTurns(chat.ID, inputNodeID)
	if err != nil {
		slog.Warn("chat bridge snapshot failed", "chat_id", chat.ID, "head", inputNodeID, "error", err)
		return ""
	}
	keys := snapshot.keys()
	last, _ := p.store.lineRoleLastActivity(chat.ID, keys, roleID, math.MaxInt64)
	if last > 0 {
		active, _ := p.store.lineHasActivityBetween(chat.ID, keys, last, math.MaxInt64)
		if !active {
			return ""
		}
	}
	coverage := &contextCoverage{Summarized: []string{}, Raw: []string{}, Omitted: []string{}}
	summaries, covered := p.snapshotSummariesSection(snapshot, last, config.SummaryCharBudget, roleID)
	tail, raw, omitted := p.snapshotTailSection(chat.ID, snapshot, covered, last, config.TailWordBudget, config.TailByteBudget)
	coverage.Summarized, coverage.Raw, coverage.Omitted = setKeys(covered), raw, omitted
	sections := nonEmpty(summaries, tail)
	if recall := p.buildHindsightRecallSection(chat.ID, query, "", occurredAt); recall != "" {
		sections = append(sections, recall)
	}
	if len(sections) == 0 {
		return ""
	}
	p.recordContextBuild(chat.ID, branchID, dispatchID, "bridge", inputNodeID, coverage)
	bridge := "While you were away, other work happened in this chat that your session did not see. Catch up from this context:\n\n" + strings.Join(sections, "\n\n")
	return capRecentContext(bridge, config.ContextByteBudget)
}

// snapshotSummariesSection renders the summary section over a snapshot's
// turn set (newest-first budget pick, oldest-first render) and reports the
// covered turn ids. A summary covers exactly its own turn — only picked
// summaries may replace raw content.
func (p *Plugin) snapshotSummariesSection(snapshot *historySnapshot, after int64, charBudget int, excludeRoleID string) (string, map[string]bool) {
	covered := map[string]bool{}
	if charBudget <= 0 || len(snapshot.Turns) == 0 {
		return "", covered
	}
	turnIDs := make([]string, 0, len(snapshot.Turns))
	for _, turn := range snapshot.Turns {
		turnIDs = append(turnIDs, turn.ID)
	}
	summaries, err := p.store.lineTurnSummaries(turnIDs, math.MaxInt64, after, excludeRoleID)
	if err != nil || len(summaries) == 0 {
		return "", covered
	}
	picked, used := []TurnSummary{}, 0
	for i := len(summaries) - 1; i >= 0; i-- {
		length := len([]rune(summaries[i].Summary))
		if used+length > charBudget {
			if len(picked) == 0 {
				item := summaries[i]
				item.Summary = truncateText(item.Summary, charBudget)
				picked = append(picked, item)
			}
			break
		}
		picked = append(picked, summaries[i])
		used += length
	}
	lines := []string{"Summaries of earlier work turns in this chat (most recent last):"}
	for i := len(picked) - 1; i >= 0; i-- {
		item := picked[i]
		covered[item.TurnID] = true
		label := fallback(item.RoleName, fallback(item.RoleID, "agent"))
		lines = append(lines, fmt.Sprintf("- [%s, role %q]\n%s", formatSummaryTime(item.OccurredAt), label, item.Summary))
	}
	return strings.Join(lines, "\n\n"), covered
}

// snapshotTailSection renders the raw (unsummarized) tail over a snapshot:
// eligible messages are the visible messages of turns NOT covered by a
// picked summary plus the snapshot's orphan input messages, newer than
// `after` when given. Word-budgeted newest-first; reports the turn ids
// injected raw and the eligible turn ids omitted by the budget.
func (p *Plugin) snapshotTailSection(chatID string, snapshot *historySnapshot, covered map[string]bool, after int64, wordBudget, byteBudget int) (string, []string, []string) {
	raw, omitted := []string{}, []string{}
	if wordBudget <= 0 || byteBudget <= 0 {
		return "", raw, omitted
	}
	keys := []string{}
	eligible := map[string]bool{}
	for _, turn := range snapshot.Turns {
		if covered[turn.ID] {
			continue
		}
		eligible[turn.ID] = true
		keys = append(keys, turn.ID)
		if turn.DispatchID != "" {
			keys = append(keys, turn.DispatchID)
		}
	}
	messages := []Message{}
	if len(keys) > 0 {
		found, err := p.store.lineHistoryAfter(chatID, keys, after, math.MaxInt64, wordBudget)
		if err == nil {
			messages = found
		}
	}
	if len(snapshot.InputMessageIDs) > 0 {
		var inputs []Message
		query := p.store.db.Where("id IN ?", snapshot.InputMessageIDs)
		if after > 0 {
			query = query.Where("created_at > ?", after)
		}
		if err := query.Order("created_at, id").Find(&inputs).Error; err == nil {
			messages = append(messages, inputs...)
		}
	}
	if len(messages) == 0 {
		return "", raw, omitted
	}
	// Merge turn-keyed and input messages into one time order, then apply
	// the word budget newest-first (lineHistoryAfter budgets its own half
	// already; the union re-budgets so inputs compete fairly).
	sortMessages(messages)
	used, picked := 0, []Message{}
	for i := len(messages) - 1; i >= 0; i-- {
		words := len(splitWords(messages[i].Text))
		if used+words > wordBudget && len(picked) > 0 {
			break
		}
		used += words
		picked = append(picked, messages[i])
	}
	for left, right := 0, len(picked)-1; left < right; left, right = left+1, right-1 {
		picked[left], picked[right] = picked[right], picked[left]
	}
	included := map[string]bool{}
	for _, message := range picked {
		included[message.TurnID] = true
		included[message.ID] = true
	}
	for _, turn := range snapshot.Turns {
		if covered[turn.ID] {
			continue
		}
		if included[turn.ID] || (turn.DispatchID != "" && included[turn.DispatchID]) {
			raw = append(raw, turn.ID)
		} else {
			omitted = append(omitted, turn.ID)
		}
	}
	heading := "Recent activity not yet covered by a summary (raw messages):"
	if len(covered) == 0 && after == 0 {
		heading = "Recent visible chat history before the current message:"
	}
	return renderRecentHistory(picked, heading, byteBudget), raw, omitted
}

func sortMessages(messages []Message) {
	for i := 1; i < len(messages); i++ {
		for j := i; j > 0 && (messages[j-1].CreatedAt > messages[j].CreatedAt ||
			(messages[j-1].CreatedAt == messages[j].CreatedAt && messages[j-1].ID > messages[j].ID)); j-- {
			messages[j-1], messages[j] = messages[j], messages[j-1]
		}
	}
}

func setKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	return keys
}

// recordContextBuild persists one build's coverage report (best-effort —
// diagnostics never fail a prompt; synchronous so a build's report is
// durable before the turn runs, and to keep write concurrency off the
// single-writer database).
func (p *Plugin) recordContextBuild(chatID, branchID, dispatchID, kind, headNodeID string, coverage *contextCoverage) {
	row := &ContextBuild{ID: newID(), ChatID: chatID, BranchID: branchID, DispatchID: dispatchID, Kind: kind, HeadNodeID: headNodeID, Coverage: encodeJSON(coverage), CreatedAt: nowMillis()}
	if err := p.store.db.Create(row).Error; err != nil {
		slog.Warn("chat context build record failed", "chat_id", chatID, "error", err)
	}
}
