package chat

// Named parallel branches (framework v0.63): creation/rename/delete, and the
// merge flow — branches:merge drafts an editable summary (LLM over the
// branches' turn summaries, falling back to raw transcripts), the user edits
// it, and branches:merge-confirm dispatches the final text into the mainline
// and archives the branches with their cutoff turns recorded.

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"viewer/sdk/go/busclient"
)

var (
	errBranchArchived = errors.New("the branch is archived (merged or shelved)")
	errBranchRunning  = errors.New("the branch still has a running turn — wait for it to finish")
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
	fromTurnID := strings.TrimSpace(requestString(value, "from_turn_id"))
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
	// Fork point (framework v0.64): the branch starts after the referenced
	// turn. The turn's line must be active — archived (merged) lines are
	// read-only and can no longer be forked from.
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
	}
	if name == "" {
		// Default 分支NN: count over all branches (including archived) so a
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
	branch := &Branch{ID: id, ChatID: chatID, Name: name, ForkTurnID: fromTurnID, ParentBranchID: parentBranchID, CreatedAt: now, UpdatedAt: now}
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

const mergeSummarySystemPrompt = "You integrate the outcomes of several parallel work branches that ran in one shared working directory, writing a merge briefing for the work line (mainline or another branch) that will continue the work. Be factual: never invent constraints, decisions, file names or numbers that are not present in the input. Keep names of files, scripts, commands and important numbers verbatim. Write in the same language as the input (Chinese if the input is Chinese). Keep the whole summary under 1500 characters."

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

// validateMergeTarget loads the merge target line ("" = mainline, no row):
// a branch target must belong to the chat, be active, and not be among the
// merge sources (framework v0.64 — branch-to-branch merges).
func (p *Plugin) validateMergeTarget(chatID, targetBranchID string, sources map[string]bool) (*Branch, error) {
	if targetBranchID == "" {
		return nil, nil
	}
	if sources[targetBranchID] {
		return nil, errors.New("the merge target cannot be merged into itself")
	}
	target, err := p.store.branch(targetBranchID)
	if err != nil {
		return nil, err
	}
	if target == nil || target.ChatID != chatID {
		return nil, errors.New("the merge target branch was not found in the chat")
	}
	if target.archived() {
		return nil, fmt.Errorf("merge target %w", errBranchArchived)
	}
	return target, nil
}

func (p *Plugin) handleBranchesMerge(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	targetBranchID := strings.TrimSpace(requestString(value, "target_branch_id"))
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
	sourceSet := map[string]bool{}
	for _, branch := range branches {
		sourceSet[branch.ID] = true
	}
	target, err := p.validateMergeTarget(chatID, targetBranchID, sourceSet)
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
	targetPayload := map[string]any{"id": "", "name": "主线"}
	if target != nil {
		targetPayload = map[string]any{"id": target.ID, "name": target.Name}
	}
	p.reply(frame, map[string]any{"summary": header + strings.TrimSpace(result.Content), "branches": cutoffs, "target": targetPayload}, nil)
}

func branchNamesLabel(branches []*Branch) string {
	names := make([]string, 0, len(branches))
	for _, branch := range branches {
		names = append(names, "「"+branch.Name+"」")
	}
	return strings.Join(names, "")
}

// handleBranchesArchive archives branches WITHOUT merging (framework
// v0.66 — the "by the way" pattern): a side conversation whose content
// should stay out of every line's context. Archive-only rows carry no
// MergeMessageID, so lineage never unions their turns (see
// branchesMergedInto). Archived branches leave the bar and reject
// dispatch; unarchiving is a reserved function — deliberately not
// implemented yet. Running branches refuse archiving (same rule as
// merging); empty branches may be archived (or deleted outright).
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
		branch.ArchivedAt = &now
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

// handleBranchesMergeConfirm dispatches the (user-edited) summary into the
// target line (mainline by default; a branch target receives it as a
// continuation turn of that branch, framework v0.64), then archives the
// source branches with the cutoff turns recorded at draft time, the merge
// target, and the merge message id linking the summary box to its sources.
func (p *Plugin) handleBranchesMergeConfirm(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	summary := strings.TrimSpace(requestString(value, "summary"))
	targetBranchID := strings.TrimSpace(requestString(value, "target_branch_id"))
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
	// archived partially. The target must also still be active.
	branches, err := p.validateMergeBranches(chatID, branchIDs)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	sourceSet := map[string]bool{}
	for _, branch := range branches {
		sourceSet[branch.ID] = true
	}
	if _, err = p.validateMergeTarget(chatID, targetBranchID, sourceSet); err != nil {
		p.reply(frame, nil, err)
		return
	}
	workspace, err := p.workspace(p.ctx)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	// Target-line dispatch: mainline rides the normal flow. A branch
	// target pins the summary to the branch's latest role (framework
	// v0.66: branches are multi-role now, so without the pin the summary
	// would go through LLM routing to an arbitrary member); the role
	// resumes its own session lane on that branch. No turns yet, or the
	// role left the chat → fall through to normal routing.
	dispatch := dispatchRequest{ChatID: chatID, Message: summary, BranchID: targetBranchID}
	raw := map[string]any{"chat_id": chatID, "message": summary}
	if targetBranchID != "" {
		raw["branch_id"] = targetBranchID
		if latest, latestErr := p.store.latestLineTurn(chatID, targetBranchID); latestErr == nil && latest != nil {
			for _, id := range decodeStrings(chat.MemberRoleIDsJSON) {
				if id == latest.RoleID {
					dispatch.RoleIDs = []string{id}
					raw["role_ids"] = []string{id}
					break
				}
			}
		}
	}
	_, user, err := p.dispatchMessage(chat, workspace, dispatch, raw)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	now := nowMillis()
	archived := make([]map[string]any, 0, len(branches))
	for index, branch := range branches {
		branch.ArchivedAt = &now
		branch.MergedIntoBranchID = targetBranchID
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
