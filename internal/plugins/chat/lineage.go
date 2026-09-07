package chat

// Branch DAG lineage (framework v0.64): the turn set visible to a work line
// at a given moment, and the lineage-scoped context builders over it. A
// line's context draws from its own turns, its fork ancestry (each parent
// line up to and including the fork turn), and — recursively — the turns of
// branches merged into any of those, all time-ordered, so a line that
// absorbed merges reads as if the work never left it.

import (
	"fmt"
	"sort"
	"strings"
)

// lineageTurns collects the turns visible to a work line (branchID "" =
// mainline) before cutoff, time-ordered. Fork ancestry is a chain into
// strictly older lines and merge targets are always active at merge time
// (sources archived forever), so both recursions are acyclic; the visited
// set guards the diamond case (a line reachable via ancestry and via a
// merged branch's ancestry).
func (p *Plugin) lineageTurns(chatID, branchID string, before int64) ([]Turn, error) {
	visited := map[string]bool{}
	collected := []Turn{}
	var walk func(line string, cutoff int64) error
	walk = func(line string, cutoff int64) error {
		if visited[line] {
			return nil
		}
		visited[line] = true
		turns, err := p.store.lineTurns(chatID, line, cutoff)
		if err != nil {
			return err
		}
		collected = append(collected, turns...)
		merged, err := p.store.branchesMergedInto(chatID, line)
		if err != nil {
			return err
		}
		for _, item := range merged {
			// The merged branch's turns join this line; its own merged-in
			// branches and fork ancestry come along transitively.
			if err := walk(item.ID, cutoff); err != nil {
				return err
			}
		}
		if line == "" {
			return nil
		}
		branch, err := p.store.branch(line)
		if err != nil || branch == nil || branch.ForkTurnID == "" {
			return err
		}
		fork, err := p.store.turn(branch.ForkTurnID)
		if err != nil || fork == nil {
			return err
		}
		// The parent line contributes up to and including the fork turn
		// (+1ms: the cutoff comparison is exclusive).
		parentCutoff := fork.StartedAt + 1
		if cutoff < parentCutoff {
			parentCutoff = cutoff
		}
		return walk(branch.ParentBranchID, parentCutoff)
	}
	if err := walk(branchID, before); err != nil {
		return nil, err
	}
	sort.SliceStable(collected, func(i, j int) bool {
		if collected[i].StartedAt != collected[j].StartedAt {
			return collected[i].StartedAt < collected[j].StartedAt
		}
		return collected[i].ID < collected[j].ID
	})
	return collected, nil
}

// prevTurnFor computes the parent turn stamped at beginTurn (framework
// v0.64): a branch turn follows the branch's latest turn (first turn: the
// fork turn, so the chain crosses into the parent line); a lane
// continuation follows the session's latest turn; everything else follows
// the latest mainline turn. Stamp failures degrade to "" — the chain is
// informational; the lineage collector works off branch metadata.
func (p *Plugin) prevTurnFor(chatID string, target relayTarget) string {
	if target.branch != "" {
		if latest, err := p.store.latestLineTurn(chatID, target.branch); err == nil && latest != nil {
			return latest.ID
		}
		if branch, err := p.store.branch(target.branch); err == nil && branch != nil {
			return branch.ForkTurnID
		}
		return ""
	}
	if target.resume != "" {
		if latest, err := p.store.latestSessionTurn(chatID, target.resume); err == nil && latest != nil {
			return latest.ID
		}
		return ""
	}
	if latest, err := p.store.latestLineTurn(chatID, ""); err == nil && latest != nil {
		return latest.ID
	}
	return ""
}

// lineageTurnIDs / lineageTurnKeys project the collected turns: summaries key
// by turn id; visible messages key by turn id (role rows) or dispatch id
// (user rows), so the message queries need both.
func lineageTurnIDs(turns []Turn) []string {
	ids := make([]string, 0, len(turns))
	for _, turn := range turns {
		ids = append(ids, turn.ID)
	}
	return ids
}

func lineageTurnKeys(turns []Turn) []string {
	keys := make([]string, 0, len(turns)*2)
	for _, turn := range turns {
		keys = append(keys, turn.ID)
		if turn.DispatchID != "" {
			keys = append(keys, turn.DispatchID)
		}
	}
	return keys
}

// buildLineContext is the lineage-scoped replacement for the chat-wide
// new-session context: summaries + raw tail come from the line's own turn
// set, so a branch inherits exactly its fork ancestry — shared history
// before the fork point, nothing of sibling lines after it.
func (p *Plugin) buildLineContext(chat Chat, branchID, query string, before int64) string {
	config := p.summaryConfig(p.ctx)
	if !config.ContextEnabled {
		return ""
	}
	turns, err := p.lineageTurns(chat.ID, branchID, before)
	if err != nil {
		return ""
	}
	sections := nonEmpty(p.buildLineSummariesSection(turns, before, 0, config.SummaryCharBudget, ""), p.buildLineTailSection(chat.ID, turns, before, 0, config.TailWordBudget, config.TailByteBudget))
	if recall := p.buildHindsightRecallSection(chat.ID, query, lastString(sections), before); recall != "" {
		sections = append(sections, recall)
	}
	return capRecentContext(strings.Join(sections, "\n\n"), config.ContextByteBudget)
}

// buildLineBridge is the lineage-scoped role-switch bridge: "while you were
// away" measures activity within the line's own turn set, so continuing a
// branch never digests sibling lines' (or the role's own mainline) work.
func (p *Plugin) buildLineBridge(chat Chat, branchID, roleID, query string, before int64) string {
	config := p.summaryConfig(p.ctx)
	if !config.ContextEnabled {
		return ""
	}
	turns, err := p.lineageTurns(chat.ID, branchID, before)
	if err != nil {
		return ""
	}
	keys := lineageTurnKeys(turns)
	last, _ := p.store.lineRoleLastActivity(chat.ID, keys, roleID, before)
	if last > 0 {
		active, _ := p.store.lineHasActivityBetween(chat.ID, keys, last, before)
		if !active {
			return ""
		}
	}
	sections := nonEmpty(p.buildLineSummariesSection(turns, before, last, config.SummaryCharBudget, roleID), p.buildLineTailSection(chat.ID, turns, before, last, config.TailWordBudget, config.TailByteBudget))
	if recall := p.buildHindsightRecallSection(chat.ID, query, "", before); recall != "" {
		sections = append(sections, recall)
	}
	if len(sections) == 0 {
		return ""
	}
	bridge := "While you were away, other work happened in this chat that your session did not see. Catch up from this context:\n\n" + strings.Join(sections, "\n\n")
	return capRecentContext(bridge, config.ContextByteBudget)
}

// buildLineSummariesSection mirrors the chat-wide summary section over a
// lineage turn set: newest-first budget pick, oldest-first render.
func (p *Plugin) buildLineSummariesSection(turns []Turn, before, after int64, charBudget int, excludeRoleID string) string {
	if charBudget <= 0 {
		return ""
	}
	summaries, err := p.store.lineTurnSummaries(lineageTurnIDs(turns), before, after, excludeRoleID)
	if err != nil || len(summaries) == 0 {
		return ""
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
		label := fallback(item.RoleName, fallback(item.RoleID, "agent"))
		lines = append(lines, fmt.Sprintf("- [%s, role %q]\n%s", formatSummaryTime(item.OccurredAt), label, item.Summary))
	}
	return strings.Join(lines, "\n\n")
}

// buildLineTailSection mirrors the chat-wide unsummarized tail over a
// lineage turn set: raw messages newer than the set's latest summary.
func (p *Plugin) buildLineTailSection(chatID string, turns []Turn, before, after int64, wordBudget, byteBudget int) string {
	if wordBudget <= 0 || byteBudget <= 0 {
		return ""
	}
	latest, _ := p.store.latestSummaryTimeForTurns(lineageTurnIDs(turns), before)
	floor := after
	if latest > floor {
		floor = latest
	}
	messages, err := p.store.lineHistoryAfter(chatID, lineageTurnKeys(turns), floor, before, wordBudget)
	if err != nil {
		return ""
	}
	heading := "Recent activity not yet covered by a summary (raw messages):"
	if latest == 0 && after == 0 {
		heading = "Recent visible chat history before the current message:"
	}
	return renderRecentHistory(messages, heading, byteBudget)
}
