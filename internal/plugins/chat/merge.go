package chat

// Named parallel branches (framework v0.63): creation/rename/delete, and the
// merge flow — branches:merge drafts an editable summary (LLM over the
// branches' turn summaries, falling back to raw transcripts), the user edits
// it, and branches:merge-confirm dispatches the final text into the mainline
// and archives the branches with their cutoff turns recorded.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"viewer/sdk/go/busclient"
)

var (
	errBranchArchived = errors.New("the branch is already merged and archived")
	errBranchRunning  = errors.New("the branch still has a running turn — wait for it to finish before merging")
	errBranchEmpty    = errors.New("the branch has no turns yet")
)

// publishBranch pushes one branch record on the branch feed; panes reseed or
// upsert from it (phases: created / updated / archived / deleted).
func (p *Plugin) publishBranch(branch *Branch, phase string) {
	payload := branch.payload()
	payload["phase"] = phase
	p.publish("chat:_:branch", payload)
}

func (p *Plugin) handleBranchesCreate(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	name := strings.TrimSpace(requestString(value, "name"))
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
	if name == "" {
		name = "分支"
	}
	now := nowMillis()
	branch := &Branch{ID: newID(), ChatID: chatID, Name: name, CreatedAt: now, UpdatedAt: now}
	if err = p.store.createBranch(branch); err != nil {
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
// archived — the cleanup path for a mis-clicked ＋. Branches with history
// stay forever: archiving is the merge outcome, deletion would orphan the
// timeline's turn attribution and the 已合并分支 cards.
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
	if err = p.store.deleteBranch(id); err != nil {
		p.reply(frame, nil, err)
		return
	}
	p.publishBranch(branch, "deleted")
	p.reply(frame, map[string]any{"deleted": true, "id": id}, nil)
}

const mergeSummarySystemPrompt = "You integrate the outcomes of several parallel work branches that ran in one shared working directory, writing a merge briefing for the mainline session that will continue the work. Be factual: never invent constraints, decisions, file names or numbers that are not present in the input. Keep names of files, scripts, commands and important numbers verbatim. Write in the same language as the input (Chinese if the input is Chinese). Keep the whole summary under 1500 characters."

const mergeSummaryUserTemplate = `Below are the work records of parallel branches (each branch worked independently in the SAME shared working directory; code changes already landed in the files).

Write a merge summary with exactly these four sections:
## 各分支成果
(per branch: 分支名 — what was done, conclusions)
## 代码改动与验证
(per branch: changed files, commands, verification results)
## 分支间的冲突
(overlapping edits to the same files, contradictory conclusions; write "无" if none)
## 待办
(open questions / next steps; write "无" if none)

BRANCH RECORDS:
%s
`

// mergeTranscriptBudget caps one branch's contribution to the merge draft
// input, so a long branch cannot crowd out the others.
const mergeTranscriptBudget = 6000

// buildMergeInput renders the branches' turn records for the merge draft:
// completed turn summaries where available, truncated raw transcripts
// otherwise.
func (p *Plugin) buildMergeInput(branches []*Branch) (string, error) {
	config := p.summaryConfig(p.ctx)
	sections := make([]string, 0, len(branches))
	for _, branch := range branches {
		turns, err := p.store.branchTurns(branch.ID)
		if err != nil {
			return "", err
		}
		turnIDs := make([]string, 0, len(turns))
		for _, turn := range turns {
			turnIDs = append(turnIDs, turn.ID)
		}
		summaries, err := p.store.turnSummariesForTurns(turnIDs)
		if err != nil {
			return "", err
		}
		lines := []string{fmt.Sprintf("### 分支「%s」(role: %s)", branch.Name, fallback(branch.RoleName, "agent"))}
		for _, turn := range turns {
			if summary, ok := summaries[turn.ID]; ok && strings.TrimSpace(summary.Summary) != "" {
				lines = append(lines, summary.Summary)
				continue
			}
			transcript, _, _, transcriptErr := p.buildTurnTranscript(turn.ID, config.ToolCharBudget)
			if transcriptErr != nil || strings.TrimSpace(transcript) == "" {
				continue
			}
			lines = append(lines, truncateText(transcript, 2000))
		}
		sections = append(sections, truncateText(strings.Join(lines, "\n\n"), mergeTranscriptBudget))
	}
	return strings.Join(sections, "\n\n"), nil
}

// validateMergeBranches loads and checks the merge candidates: same chat,
// all active, none empty, none running (a running branch is merged only
// after its turn finishes). Returns the branch rows in request order.
func (p *Plugin) validateMergeBranches(chatID string, branchIDs []string) ([]*Branch, error) {
	if len(branchIDs) == 0 {
		return nil, errors.New("branch_ids is required")
	}
	branches := make([]*Branch, 0, len(branchIDs))
	seen := map[string]bool{}
	for _, id := range branchIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		branch, err := p.store.branch(id)
		if err != nil {
			return nil, err
		}
		if branch == nil || branch.ChatID != chatID {
			return nil, fmt.Errorf("branch was not found in the chat: %s", id)
		}
		if branch.archived() {
			return nil, fmt.Errorf("分支「%s」%w", branch.Name, errBranchArchived)
		}
		turns, err := p.store.branchTurns(id)
		if err != nil {
			return nil, err
		}
		if len(turns) == 0 {
			return nil, fmt.Errorf("分支「%s」%w", branch.Name, errBranchEmpty)
		}
		running, err := p.store.branchHasRunningTurn(id)
		if err != nil {
			return nil, err
		}
		if running {
			return nil, fmt.Errorf("分支「%s」%w", branch.Name, errBranchRunning)
		}
		branches = append(branches, branch)
	}
	return branches, nil
}

func (p *Plugin) handleBranchesMerge(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	var branchIDs []string
	if err == nil {
		err = decodeInto(value["branch_ids"], &branchIDs)
	}
	if err == nil && chatID == "" {
		err = errBadRequest
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	branches, err := p.validateMergeBranches(chatID, branchIDs)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	input, err := p.buildMergeInput(branches)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	config := p.summaryConfig(p.ctx)
	timeout := config.TimeoutSeconds
	if timeout <= 0 {
		timeout = 60
	}
	ctx, cancel := context.WithTimeout(p.ctx, time.Duration(timeout)*time.Second)
	result, err := p.llmFn(ctx, []map[string]string{
		{"role": "system", "content": mergeSummarySystemPrompt},
		{"role": "user", "content": fmt.Sprintf(mergeSummaryUserTemplate, input)},
	}, false, timeout)
	cancel()
	if err != nil {
		slog.Warn("chat merge summary failed", "chat_id", chatID, "error", err)
		p.reply(frame, nil, err)
		return
	}
	// Cutoff per branch: the newest turn at draft time. merge-confirm
	// carries these back so a turn that starts after the draft is not
	// silently covered by a summary that never saw it.
	cutoffs := make([]map[string]any, 0, len(branches))
	for _, branch := range branches {
		turns, turnErr := p.store.branchTurns(branch.ID)
		cutoff := ""
		if turnErr == nil && len(turns) > 0 {
			cutoff = turns[len(turns)-1].ID
		}
		cutoffs = append(cutoffs, map[string]any{"id": branch.ID, "name": branch.Name, "cutoff_turn_id": cutoff})
	}
	header := "已合并分支" + branchNamesLabel(branches) + "：\n\n"
	p.reply(frame, map[string]any{"summary": header + strings.TrimSpace(result.Content), "branches": cutoffs}, nil)
}

func branchNamesLabel(branches []*Branch) string {
	names := make([]string, 0, len(branches))
	for _, branch := range branches {
		names = append(names, "「"+branch.Name+"」")
	}
	return strings.Join(names, "")
}

// handleBranchesMergeConfirm dispatches the (user-edited) summary into the
// mainline as a normal dispatch, then archives the branches with the cutoff
// turns recorded at draft time and the merge message id linking the summary
// box to its source branches.
func (p *Plugin) handleBranchesMergeConfirm(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	summary := strings.TrimSpace(requestString(value, "summary"))
	var entries []struct {
		ID           string `json:"id"`
		CutoffTurnID string `json:"cutoff_turn_id"`
	}
	if err == nil {
		err = decodeInto(value["branches"], &entries)
	}
	if err == nil && (chatID == "" || summary == "" || len(entries) == 0) {
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
	branchIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		branchIDs = append(branchIDs, entry.ID)
	}
	// Revalidate before dispatching: a branch merged (or still running)
	// since the draft fails the whole confirm — nothing is dispatched or
	// archived partially.
	branches, err := p.validateMergeBranches(chatID, branchIDs)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	workspace, err := p.workspace(p.ctx)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	_, user, err := p.dispatchMessage(chat, workspace, dispatchRequest{ChatID: chatID, Message: summary}, map[string]any{"chat_id": chatID, "message": summary})
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	now := nowMillis()
	archived := make([]map[string]any, 0, len(branches))
	for index, branch := range branches {
		branch.ArchivedAt = &now
		branch.MergedThroughTurnID = entries[index].CutoffTurnID
		if branch.MergedThroughTurnID == "" {
			if turns, turnErr := p.store.branchTurns(branch.ID); turnErr == nil && len(turns) > 0 {
				branch.MergedThroughTurnID = turns[len(turns)-1].ID
			}
		}
		branch.MergeMessageID = user.ID
		branch.UpdatedAt = now
		if err = p.store.saveBranch(branch); err != nil {
			p.reply(frame, nil, err)
			return
		}
		p.publishBranch(branch, "archived")
		archived = append(archived, branch.payload())
	}
	p.reply(frame, map[string]any{"merged": true, "message_id": user.ID, "branches": archived}, nil)
}
