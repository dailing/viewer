package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"viewer/internal/agentdriver"
	"viewer/sdk/go/busclient"
)

type dispatchRequest struct {
	IdempotencyKey    string   `json:"idempotency_key"`
	AutomationID      string   `json:"automation_id"`
	Owner             string   `json:"owner"`
	Fence             int64    `json:"fence"`
	ChatID            string   `json:"chat_id"`
	Message           string   `json:"message"`
	Text              string   `json:"text"`
	RoleIDs           []string `json:"role_ids"`
	BeforeMessageID   string   `json:"before_message_id"`
	HistoryWordBudget *int     `json:"history_word_budget"`
	// ForceNewSession makes every selected role start a fresh agent session
	// for this message instead of reusing the stored one (one-shot, set by
	// the composer's new-session toggle).
	ForceNewSession bool `json:"force_new_session"`
	// ParallelDispatch sends immediately without waiting for in-flight
	// turns: every selected role runs concurrently on a fresh, throwaway
	// agent session (the composer's send-now toggle). Implies a fresh
	// session; the canonical per-chat+role session is left untouched.
	ParallelDispatch bool `json:"parallel_dispatch"`
	// ContinueTurnID makes the dispatch a lane continuation: it skips LLM
	// routing, goes to the role owning the referenced turn, and resumes
	// that turn's session. Contradicts force_new_session / parallel_dispatch
	// (both are cleared when set).
	ContinueTurnID string `json:"continue_turn_id"`
	// BranchID targets a named branch (framework v0.63+). Since v0.66 a
	// branch is a pure CONTEXT partition — a separated corner of the
	// chat's working group — not a role/session binding: dispatching on
	// it selects roles exactly like a mainline dispatch (explicit
	// role_ids or LLM routing, any number of roles; force_new_session /
	// parallel_dispatch apply). The only differences: each role's
	// session is scoped to the branch (role × branch lane), and context
	// builds draw from the branch's lineage.
	BranchID string `json:"branch_id"`
}

func runtimeKey(chatID, roleID string) string { return chatID + "\x00" + roleID }

// maxQueuedPerRole bounds the per-chat+role pending-message queue.
const maxQueuedPerRole = 32

// queuedMessage is a dispatch waiting for a busy chat+role: it starts as its
// own turn once the in-flight relay releases the role's busy key.
type queuedMessage struct {
	chatID   string
	role     SuperRole
	message  string
	before   int64
	forceNew bool
	enqueued int64
	// dispatchID of the dispatch that queued this message, so the turn that
	// eventually runs it links back to the user message's turn_id.
	dispatchID string
	// messageID is the user message row of the dispatch, so a queued cancel
	// removes it from the timeline and a queued edit retexts it.
	messageID string
	// resumeSession is the lane session the queued turn resumes ("" on the
	// canonical path).
	resumeSession string
	// branchID is the branch the queued turn belongs to ("" on the
	// canonical and anonymous-parallel paths).
	branchID string
}

// relayTarget is one role's turn within a relay, plus the lane session it
// resumes ("" on the canonical and parallel paths) and the branch the turn
// is stamped with ("" off-branch).
type relayTarget struct {
	role   SuperRole
	resume string
	branch string
}

func (p *Plugin) handleDispatch(frame busclient.Frame) {
	value, err := frameObject(frame)
	var request dispatchRequest
	if err == nil {
		err = decodeInto(value, &request)
	}
	if request.Message == "" {
		request.Message = request.Text
	}
	request.Message = strings.TrimSpace(request.Message)
	if err != nil || request.ChatID == "" || request.Message == "" {
		if err == nil {
			err = errBadRequest
		}
		p.reply(frame, nil, err)
		return
	}
	chat, err := p.store.chat(request.ChatID)
	if err != nil || chat == nil {
		if err == nil {
			err = errors.New("chat not found")
		}
		p.reply(frame, nil, err)
		return
	}
	workspace, err := p.workspace(p.ctx)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	reply, _, err := p.dispatchMessage(chat, workspace, request, value)
	p.reply(frame, reply, err)
}

// dispatchMessage is the frame-free core of handleDispatch (also used by
// branch merge-confirm to send the summary into the mainline): it validates
// targeting (lane continuation / branch / routing), persists the user
// message, queues behind busy keys, starts the relay, and returns the reply
// payload plus the user message row.
func (p *Plugin) dispatchMessage(chat *Chat, workspace Workspace, request dispatchRequest, raw map[string]any) (map[string]any, *Message, error) {
	p.automationMu.Lock()
	defer p.automationMu.Unlock()
	if request.AutomationID != "" {
		if reply, found, err := p.existingDispatch(request); found || err != nil {
			return reply, nil, err
		}
	}
	// Branch dispatch (branch_id): validate the branch, then route exactly
	// like a mainline dispatch (framework v0.66 — the branch only scopes
	// context and the role × branch session lanes; it no longer fixes a
	// single owner role). Lane continuation (continue_turn_id): skip LLM
	// routing and dispatch to the role owning the referenced turn,
	// resuming that turn's session.
	resumeSession := ""
	var selected []SuperRole
	var rationale string
	var branch *Branch
	var err error
	if request.BranchID != "" {
		branch, err = p.resolveBranch(*chat, request.BranchID)
		if err != nil {
			return nil, nil, err
		}
		selected, rationale, err = p.selectRoles(request, raw, *chat, workspace)
		if err != nil {
			return nil, nil, err
		}
	} else if request.ContinueTurnID != "" {
		var laneRole SuperRole
		resumeSession, laneRole, err = p.resolveContinuation(*chat, workspace, request.ContinueTurnID)
		if err != nil {
			return nil, nil, err
		}
		selected = []SuperRole{laneRole}
		rationale = "Continue the session of turn " + request.ContinueTurnID + "."
		request.ForceNewSession = false
		request.ParallelDispatch = false
	} else {
		selected, rationale, err = p.selectRoles(request, raw, *chat, workspace)
		if err != nil {
			return nil, nil, err
		}
	}
	parallel := request.ParallelDispatch
	// keyOf computes a role's busy/runtime key and the lane session its
	// turn resumes. Branch turns get their own per-role × per-branch key
	// and resume the role's own latest session on the branch ("" — no
	// prior branch session, or force_new_session — starts fresh with the
	// branch's lineage context); a continuation whose lane IS the role's
	// stored canonical session rides the canonical key and flow, keeping
	// the stored session pointer authoritative; any other lane gets its
	// own lane-scoped key.
	keyOf := func(role SuperRole) (string, string) {
		if branch != nil {
			resume := ""
			if !request.ForceNewSession {
				resume = p.branchRoleSession(chat.ID, branch.ID, role.ID)
			}
			return runtimeKey(chat.ID, role.ID) + "\x00branch\x00" + branch.ID, resume
		}
		if resumeSession == "" {
			return runtimeKey(chat.ID, role.ID), ""
		}
		state, stateErr := p.store.roleSession(chat.ID, role.ID)
		if stateErr == nil && state != nil && state.ProviderSessionID == resumeSession {
			return runtimeKey(chat.ID, role.ID), ""
		}
		return runtimeKey(chat.ID, role.ID) + "\x00lane\x00" + resumeSession, resumeSession
	}
	if !parallel {
		// Queue-capacity pre-check: an over-full queue fails the dispatch
		// before the user message lands in the timeline. A lane/branch
		// continuation also counts as busy when another key holds a turn on
		// its session.
		p.mu.Lock()
		for _, role := range selected {
			key, resume := keyOf(role)
			if (p.busy[key] || (resume != "" && p.sessionInFlightLocked(resume))) && len(p.queues[key]) >= maxQueuedPerRole {
				err = errQueueFull
				break
			}
		}
		p.mu.Unlock()
		if err != nil {
			return nil, nil, err
		}
	}
	dispatchID := newID()
	if err := p.prepareDispatch(request, dispatchID); err != nil {
		return nil, nil, err
	}
	user := &Message{ID: newID(), ChatID: chat.ID, TurnID: dispatchID, Role: "user", Text: request.Message, SenderFrom: "user", CreatedAt: nowMillis()}
	if err = p.store.addMessage(user); err != nil {
		p.store.db.Model(&DispatchReceipt{}).Where("dispatch_id = ?", dispatchID).Update("failure", err.Error())
		return nil, nil, err
	}
	p.retainVisibleMessage(user, "", "")
	// A human send supersedes the synced composer draft on every device;
	// automatic (loop) dispatches leave it alone.
	if request.AutomationID == "" {
		p.clearDraft(chat.ID)
	}
	// Busy roles queue the message (it starts when the in-flight turn ends);
	// free roles start immediately. Parallel dispatch skips the busy lock and
	// runs every role right away on a throwaway session.
	startedKeys := []string{}
	started := []relayTarget{}
	queuedRoleIDs := []string{}
	p.mu.Lock()
	for _, role := range selected {
		key, resume := keyOf(role)
		if !parallel && (p.busy[key] || (resume != "" && p.sessionInFlightLocked(resume))) {
			p.queues[key] = append(p.queues[key], queuedMessage{chatID: chat.ID, role: role, message: request.Message, before: user.CreatedAt, forceNew: request.ForceNewSession, enqueued: nowMillis(), dispatchID: dispatchID, messageID: user.ID, resumeSession: resume, branchID: branchIDOf(branch)})
			queuedRoleIDs = append(queuedRoleIDs, role.ID)
			continue
		}
		if !parallel {
			p.busy[key] = true
			startedKeys = append(startedKeys, key)
		}
		started = append(started, relayTarget{role: role, resume: resume, branch: branchIDOf(branch)})
	}
	p.mu.Unlock()
	// Anonymous parallel (send-now) dispatches open one named branch per
	// started role (framework v0.63): the throwaway session becomes an
	// addressable, mergeable work line instead of an untracked lane. An
	// explicit branch dispatch stamps its role onto an empty branch.
	touchedBranches := []*Branch{}
	if parallel && branch == nil {
		// Fork point: the mainline's latest turn at dispatch time, so the
		// auto-branches' lineage context shares the mainline history up to
		// now and nothing after.
		forkTurnID := ""
		if latest, latestErr := p.store.latestLineTurn(chat.ID, ""); latestErr == nil && latest != nil {
			forkTurnID = latest.ID
		}
		for index := range started {
			now := nowMillis()
			created := &Branch{ID: newID(), ChatID: chat.ID, Name: autoBranchName(request.Message, started[index].role, len(started)), RoleID: started[index].role.ID, RoleName: started[index].role.Name, ForkTurnID: forkTurnID, CreatedAt: now, UpdatedAt: now}
			if createErr := p.store.createBranch(created); createErr != nil {
				slog.Warn("chat branch auto-create failed", "chat_id", chat.ID, "role_id", started[index].role.ID, "error", createErr)
				continue
			}
			started[index].branch = created.ID
			touchedBranches = append(touchedBranches, created)
			p.publishBranch(created, "created")
		}
	} else if branch != nil && branch.RoleID == "" && len(started) > 0 {
		branch.RoleID, branch.RoleName = started[0].role.ID, started[0].role.Name
		branch.UpdatedAt = nowMillis()
		if saveErr := p.store.saveBranch(branch); saveErr != nil {
			slog.Warn("chat branch role stamp failed", "branch_id", branch.ID, "error", saveErr)
		} else {
			touchedBranches = append(touchedBranches, branch)
			p.publishBranch(branch, "updated")
		}
	}
	p.publishMessage(user)
	if len(queuedRoleIDs) > 0 {
		p.publishQueue(chat.ID)
	}
	startedRoleIDs := make([]string, 0, len(started))
	for _, target := range started {
		startedRoleIDs = append(startedRoleIDs, target.role.ID)
	}
	reply := map[string]any{"role_ids": roleIDs(selected), "started_role_ids": startedRoleIDs, "queued_role_ids": queuedRoleIDs, "rationale": rationale, "dispatch_id": dispatchID, "message_id": user.ID}
	if len(touchedBranches) > 0 {
		payloads := make([]map[string]any, 0, len(touchedBranches))
		for _, item := range touchedBranches {
			payloads = append(payloads, item.payload())
		}
		reply["branches"] = payloads
	}
	if len(started) == 0 {
		return reply, user, nil
	}
	p.wg.Add(1)
	startGate := make(chan struct{})
	go func() {
		<-startGate
		defer p.wg.Done()
		if !parallel {
			defer p.releaseBusy(startedKeys)
		}
		p.runRelay(*chat, workspace, started, request.Message, user.CreatedAt, request.ForceNewSession, parallel, dispatchID)
	}()
	close(startGate)
	return reply, user, nil
}

func branchIDOf(branch *Branch) string {
	if branch == nil {
		return ""
	}
	return branch.ID
}

// autoBranchName derives a branch name from the dispatching message (first
// runes, so the tab is recognizable without a rename); multi-role parallel
// dispatches suffix the role to tell the branches apart.
func autoBranchName(message string, role SuperRole, roleCount int) string {
	runes := []rune(strings.TrimSpace(message))
	name := "分支"
	if len(runes) > 0 {
		if len(runes) > 12 {
			name = string(runes[:12]) + "…"
		} else {
			name = string(runes)
		}
	}
	if roleCount > 1 {
		name += " · " + role.Name
	}
	return name
}

// resolveBranch validates a branch_id dispatch: the branch must belong to
// the chat and be active (archived branches are read-only history). Role
// selection is deliberately NOT the branch's business — since framework
// v0.66 a branch is a pure context partition, and the caller routes
// exactly like a mainline dispatch.
func (p *Plugin) resolveBranch(chat Chat, branchID string) (*Branch, error) {
	branch, err := p.store.branch(branchID)
	if err != nil {
		return nil, err
	}
	if branch == nil || branch.ChatID != chat.ID {
		return nil, errors.New("branch was not found in the chat")
	}
	if branch.archived() {
		return nil, errBranchArchived
	}
	return branch, nil
}

// branchRoleSession returns the role's latest resumable session on the
// branch ("" when the role never ran there — the relay then starts a
// fresh session fed with the branch's lineage context): the newest turn's
// stamped session, or the in-flight runtime's session while that turn
// runs.
func (p *Plugin) branchRoleSession(chatID, branchID, roleID string) string {
	turn, err := p.store.latestBranchRoleTurn(chatID, branchID, roleID)
	if err != nil || turn == nil {
		return ""
	}
	if turn.SessionID != "" {
		return turn.SessionID
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inflightTurnSessionLocked(turn.ID)
}

func (p *Plugin) selectRoles(request dispatchRequest, raw map[string]any, chat Chat, workspace Workspace) ([]SuperRole, string, error) {
	members := map[string]bool{}
	for _, id := range decodeStrings(chat.MemberRoleIDsJSON) {
		members[id] = true
	}
	candidates := []SuperRole{}
	for _, role := range workspace.Roles {
		if members[role.ID] {
			candidates = append(candidates, role)
		}
	}
	_, explicit := raw["role_ids"]
	if explicit && len(request.RoleIDs) > 0 {
		selected := []SuperRole{}
		seen := map[string]bool{}
		for _, id := range request.RoleIDs {
			role, ok := roleByID(candidates, id)
			if !ok {
				return nil, "", fmt.Errorf("role is not a member of chat: %s", id)
			}
			if !seen[id] {
				selected = append(selected, role)
				seen[id] = true
			}
		}
		return selected, "Explicit role_ids; LLM routing skipped.", nil
	}
	eligible := []SuperRole{}
	for _, role := range candidates {
		if strings.TrimSpace(role.Description) != "" {
			eligible = append(eligible, role)
		}
	}
	if len(eligible) == 0 {
		return nil, "", errors.New("no dispatchable chat roles have descriptions")
	}
	budget := defaultHistoryWordBudget
	if request.HistoryWordBudget != nil {
		budget = *request.HistoryWordBudget
	}
	if budget < 0 {
		budget = 0
	}
	if budget > 8000 {
		budget = 8000
	}
	before := int64(0)
	if request.BeforeMessageID != "" {
		marker, markerErr := p.store.message(request.BeforeMessageID)
		if markerErr != nil {
			return nil, "", markerErr
		}
		if marker == nil || marker.ChatID != chat.ID {
			return nil, "", errors.New("before_message_id was not found in the chat")
		}
		before = marker.CreatedAt
	}
	history, err := p.historyPrompt(chat.ID, before, budget)
	if err != nil {
		return nil, "", err
	}
	ids, rationale, err := routeWithLLM(p.ctx, p.llmFn, request.Message, eligible, history)
	if err != nil {
		slog.Warn("chat dispatch routing failed", "chat_id", chat.ID, "error", err)
		return nil, "", err
	}
	selected := []SuperRole{}
	for _, id := range ids {
		if role, ok := roleByID(eligible, id); ok {
			selected = append(selected, role)
		}
	}
	return selected, rationale, nil
}

func roleIDs(roles []SuperRole) []string {
	result := make([]string, 0, len(roles))
	for _, role := range roles {
		result = append(result, role.ID)
	}
	return result
}

// resolveContinuation validates a continue_turn_id dispatch: the turn must
// belong to the chat and have a resumable session (stamped on the row once
// its runtime starts, or visible on an in-flight runtime), and its role must
// still exist in the workspace. Returns the session to resume and the role.
func (p *Plugin) resolveContinuation(chat Chat, workspace Workspace, turnID string) (string, SuperRole, error) {
	turn, err := p.store.turn(turnID)
	if err != nil {
		return "", SuperRole{}, err
	}
	if turn == nil || turn.ChatID != chat.ID {
		return "", SuperRole{}, errors.New("continue_turn_id was not found in the chat")
	}
	sessionID := turn.SessionID
	if sessionID == "" {
		p.mu.Lock()
		sessionID = p.inflightTurnSessionLocked(turnID)
		p.mu.Unlock()
	}
	if sessionID == "" {
		return "", SuperRole{}, errors.New("that turn has no resumable session")
	}
	for _, role := range workspace.Roles {
		if role.ID == turn.RoleID {
			return sessionID, role, nil
		}
	}
	return "", SuperRole{}, errors.New("the turn's role no longer exists")
}

// inflightTurnSessionLocked returns the session of a currently running turn
// ("" when the turn is not in flight). Caller holds p.mu.
func (p *Plugin) inflightTurnSessionLocked(turnID string) string {
	for _, current := range p.runtimes {
		if current.activeTurn == turnID {
			return current.sessionID
		}
	}
	return ""
}

// sessionInFlightLocked reports whether any runtime is running a turn on the
// session — under any key (canonical, throwaway, or lane). Lane continuations
// queue behind it: one session never takes two prompts at once. Caller holds
// p.mu.
func (p *Plugin) sessionInFlightLocked(sessionID string) bool {
	for _, current := range p.runtimes {
		if current.sessionID == sessionID && current.activeTurn != "" {
			return true
		}
	}
	return false
}
func (p *Plugin) releaseBusy(keys []string) {
	p.mu.Lock()
	for _, key := range keys {
		delete(p.busy, key)
	}
	p.mu.Unlock()
	// A freed role may have queued messages waiting; start the next one.
	for _, key := range keys {
		p.startQueued(key)
	}
}

// startQueued pops the next queued message for a freed chat+role key, marks
// the key busy again, and relays it as its own single-role turn. Entries
// whose chat disappeared (or whose workspace can no longer load) are dropped
// and the cascade continues with the following entry.
func (p *Plugin) startQueued(key string) {
	p.mu.Lock()
	if p.busy[key] {
		p.mu.Unlock()
		return
	}
	queue := p.queues[key]
	if len(queue) == 0 {
		p.mu.Unlock()
		return
	}
	entry := queue[0]
	if len(queue) == 1 {
		delete(p.queues, key)
	} else {
		p.queues[key] = queue[1:]
	}
	p.busy[key] = true
	p.mu.Unlock()
	p.publishQueue(entry.chatID)
	chat, err := p.store.chat(entry.chatID)
	if err == nil && chat == nil {
		err = errors.New("chat not found")
	}
	if err == nil {
		var workspace Workspace
		workspace, err = p.workspace(p.ctx)
		if err == nil {
			slog.Info("chat queued message starting", "chat_id", entry.chatID, "role_id", entry.role.ID, "role_name", entry.role.Name, "queued_ms", nowMillis()-entry.enqueued)
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				defer p.releaseBusy([]string{key})
				p.runRelay(*chat, workspace, []relayTarget{{role: entry.role, resume: entry.resumeSession, branch: entry.branchID}}, entry.message, entry.before, entry.forceNew, false, entry.dispatchID)
			}()
			return
		}
	}
	slog.Warn("chat queued message dropped", "chat_id", entry.chatID, "role_id", entry.role.ID, "error", err)
	p.releaseBusy([]string{key})
}

// queuedSnapshotLocked lists one chat's pending queue entries (across all
// role/lane keys) oldest-first — the pane's queued-chip source. position is
// the 1-based slot inside that role key's queue. Caller holds p.mu.
func (p *Plugin) queuedSnapshotLocked(chatID string) []map[string]any {
	keys := make([]string, 0, len(p.queues))
	for key := range p.queues {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := []map[string]any{}
	for _, key := range keys {
		for index, entry := range p.queues[key] {
			if entry.chatID != chatID {
				continue
			}
			result = append(result, map[string]any{
				"message_id": entry.messageID, "dispatch_id": entry.dispatchID,
				"chat_id": entry.chatID, "role_id": entry.role.ID, "role_name": entry.role.Name,
				"text": entry.message, "enqueued_at": entry.enqueued, "position": index + 1,
			})
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i]["enqueued_at"].(int64) < result[j]["enqueued_at"].(int64)
	})
	return result
}

func (p *Plugin) queuedSnapshot(chatID string) []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.queuedSnapshotLocked(chatID)
}

// publishQueue pushes the chat's full queue snapshot on the queue feed after
// any mutation (enqueue, dequeue, cancel, edit) so every pane reseeds
// wholesale instead of tracking per-entry deltas.
func (p *Plugin) publishQueue(chatID string) {
	p.publish("chat:_:queue", map[string]any{"chat_id": chatID, "queued": p.queuedSnapshot(chatID)})
}

// cancelQueued drops every queue entry of one dispatch in the chat (a
// multi-role dispatch queues one entry per busy role) and returns the count
// plus the dispatch's user message id. Caller holds no lock.
func (p *Plugin) cancelQueued(chatID, dispatchID string) (int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	removed := 0
	messageID := ""
	for key, queue := range p.queues {
		if !strings.HasPrefix(key, chatID+"\x00") {
			continue
		}
		kept := make([]queuedMessage, 0, len(queue))
		for _, entry := range queue {
			if entry.dispatchID == dispatchID {
				removed++
				if messageID == "" {
					messageID = entry.messageID
				}
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == 0 {
			delete(p.queues, key)
		} else {
			p.queues[key] = kept
		}
	}
	return removed, messageID
}

// updateQueued rewrites the message text of every queue entry of one
// dispatch, keeping each entry's position. Returns the count plus the
// dispatch's user message id.
func (p *Plugin) updateQueued(chatID, dispatchID, message string) (int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	updated := 0
	messageID := ""
	for key, queue := range p.queues {
		if !strings.HasPrefix(key, chatID+"\x00") {
			continue
		}
		for index := range queue {
			if queue[index].dispatchID != dispatchID {
				continue
			}
			queue[index].message = message
			updated++
			if messageID == "" {
				messageID = queue[index].messageID
			}
		}
	}
	return updated, messageID
}

func (p *Plugin) handleQueuedCancel(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	dispatchID, _ := value["dispatch_id"].(string)
	if err == nil && (chatID == "" || dispatchID == "") {
		err = errBadRequest
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	removed, messageID := p.cancelQueued(chatID, dispatchID)
	if removed > 0 {
		if err := p.store.db.Model(&DispatchReceipt{}).Where("dispatch_id = ?", dispatchID).Update("cancelled", true).Error; err != nil {
			p.reply(frame, nil, err)
			return
		}
	}
	if removed == 0 {
		p.reply(frame, nil, errNotQueued)
		return
	}
	// The dispatch's user message row leaves the timeline with its queue
	// entries; the deleted frame lets panes drop the box immediately.
	if messageID != "" {
		if err = p.store.deleteMessage(messageID); err != nil {
			p.reply(frame, nil, err)
			return
		}
		p.publish("chat:"+chatID+":message", map[string]any{"id": messageID, "chat_id": chatID, "deleted": true})
	}
	p.publishQueue(chatID)
	p.reply(frame, map[string]any{"cancelled": true, "removed": removed}, nil)
}

func (p *Plugin) handleQueuedUpdate(frame busclient.Frame) {
	p.automationMu.Lock()
	defer p.automationMu.Unlock()
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	dispatchID, _ := value["dispatch_id"].(string)
	message := strings.TrimSpace(requestString(value, "message"))
	if err == nil && (chatID == "" || dispatchID == "" || message == "") {
		err = errBadRequest
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	updated, messageID := p.updateQueued(chatID, dispatchID, message)
	if updated > 0 {
		var receipt DispatchReceipt
		found := p.store.db.Where("dispatch_id = ?", dispatchID).Limit(1).Find(&receipt)
		if found.Error != nil {
			p.reply(frame, nil, found.Error)
			return
		}
		if found.RowsAffected > 0 {
			err := p.store.db.Model(&AutomationGate{}).Where("key = ?", gateKey(chatID, receipt.BranchID)).Updates(map[string]any{"paused": true, "revision": gorm.Expr("revision + 1"), "feedback": boundedTail(message, 8192)}).Error
			if err != nil {
				p.reply(frame, nil, err)
				return
			}
		}
	}
	if updated == 0 {
		p.reply(frame, nil, errNotQueued)
		return
	}
	if messageID != "" {
		if err = p.store.updateMessageText(messageID, message); err != nil {
			p.reply(frame, nil, err)
			return
		}
		// Republish the row so open panes retext the query box in place.
		if row, rowErr := p.store.message(messageID); rowErr == nil && row != nil {
			p.publishMessage(row)
		}
	}
	p.publishQueue(chatID)
	p.reply(frame, map[string]any{"updated": true}, nil)
}

func (p *Plugin) runRelay(chat Chat, workspace Workspace, targets []relayTarget, message string, before int64, forceNew bool, parallel bool, dispatchID string) {
	for _, target := range targets {
		role := target.role
		turnID := newID()
		key := runtimeKey(chat.ID, role.ID)
		switch {
		case parallel:
			// Concurrent send-now turn: unique throwaway runtime key so the
			// canonical chat+role runtime (and its stored session) stays
			// untouched; the entry is removed when the turn ends.
			key += "\x00" + turnID
		case target.branch != "":
			// Branch turn: the branch's session runs under its own
			// branch-scoped key, so it never fights the canonical
			// session's busy lock; the runtime stays resident for the
			// branch's next continuation.
			key += "\x00branch\x00" + target.branch
		case target.resume != "":
			// Lane continuation: the lane's session runs under its own key so
			// it never fights the canonical session's busy lock, and the
			// runtime stays resident for the lane's next continuation.
			key += "\x00lane\x00" + target.resume
		}
		turn := &Turn{ID: turnID, ChatID: chat.ID, RoleID: role.ID, RoleName: role.Name, DispatchID: dispatchID, BranchID: target.branch, PrevTurnID: p.prevTurnFor(chat.ID, target), StartedAt: nowMillis()}
		if err := p.store.beginTurn(turn); err != nil {
			slog.Error("chat turn persistence failed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "error", err)
			p.store.db.Model(&DispatchReceipt{}).Where("dispatch_id = ?", dispatchID).Update("failure", err.Error())
			continue
		}
		// Global turn lifecycle feed for the Dock status dots: started here,
		// completed below alongside the per-chat turn-completed frame.
		p.publish("chat:_:turn", map[string]any{"chat_id": chat.ID, "turn_id": turnID, "role_id": role.ID, "role_name": role.Name, "phase": "started", "dispatch_id": dispatchID, "branch_id": target.branch})
		candidates, err := p.resolveCandidates(chat, workspace, role)
		reason, summaryProvider := "error", ""
		endErr := ""
		attempts := []map[string]any{}
		// laneSession records the session this turn ran on, for the
		// end-of-turn lane-queue kick below.
		laneSession := ""
		// resolvedTarget records the candidate that actually runs the turn:
		// the planned first candidate at resolve time, replaced on failover.
		// Persisted on the turn row and published on the turn feed so the
		// pane's routing labels come from the execution record.
		resolved := agentdriver.Target{}
		if err != nil {
			slog.Warn("chat routing candidate resolution failed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "error", err)
		} else if len(candidates) > 0 {
			resolved = candidates[0].target
			p.recordTurnTarget(chat.ID, turnID, role, resolved, dispatchID)
		}
	candidateLoop:
		for _, candidate := range candidates {
			retryFresh := false
			for {
				var current *runtime
				var fresh bool
				attempt := map[string]any{"agent": candidate.target.Agent, "provider": candidate.target.Provider, "model": candidate.target.Model}
				// A branch's first turn starts a fresh session of its own:
				// forceNew + ephemeral (the stored canonical session is never
				// read or overwritten), but unlike anonymous parallel the
				// runtime stays resident under the branch key for continuations.
				branchFresh := target.branch != "" && target.resume == ""
				current, fresh, err = p.ensureBusRuntime(p.ctx, chat, role, candidate, turnID, key, forceNew || retryFresh || parallel || branchFresh, parallel || branchFresh, target.resume)
				if err != nil {
					attempt["outcome"], attempt["error"] = "start_error", err.Error()
					attempts = append(attempts, attempt)
					slog.Warn("agent start failed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "agent", candidate.target.Agent, "provider", candidate.target.Provider, "model", candidate.target.Model, "error", err)
					continue candidateLoop
				}
				summaryProvider = candidate.target.Agent
				if candidate.target.Agent != resolved.Agent || candidate.target.Provider != resolved.Provider || candidate.target.Model != resolved.Model {
					resolved = candidate.target
					p.recordTurnTarget(chat.ID, turnID, role, resolved, dispatchID)
				}
				// Stamp the session on the turn row and announce it on the turn
				// feed: the pane's session lanes and lane continuations build
				// from these records.
				laneSession = current.sessionID
				if laneSession != "" {
					if sessionErr := p.store.setTurnSession(turnID, laneSession); sessionErr != nil {
						slog.Warn("chat turn session persistence failed", "chat_id", chat.ID, "turn_id", turnID, "error", sessionErr)
					}
					p.publish("chat:_:turn", map[string]any{"chat_id": chat.ID, "turn_id": turnID, "role_id": role.ID, "role_name": role.Name, "phase": "session", "dispatch_id": dispatchID, "session_id": laneSession, "branch_id": target.branch})
					if target.branch != "" {
						// Denormalize the branch's latest session for display;
						// the turn rows stay authoritative.
						if branch, branchErr := p.store.branch(target.branch); branchErr == nil && branch != nil && branch.SessionID != laneSession {
							branch.SessionID = laneSession
							branch.UpdatedAt = nowMillis()
							if saveErr := p.store.saveBranch(branch); saveErr != nil {
								slog.Warn("chat branch session stamp failed", "branch_id", branch.ID, "error", saveErr)
							} else {
								p.publishBranch(branch, "updated")
							}
						}
					}
				}
				prompt := message
				contextBytes, promptMode := 0, "existing_session"
				if fresh {
					contextBridge := p.buildLineContext(chat, target.branch, message, before)
					contextBytes, promptMode = len(contextBridge), "new_session"
					prompt = initialPrompt(workspace, chat, role, contextBridge, message)
				} else if bridge := p.buildLineBridge(chat, target.branch, role.ID, message, before); bridge != "" {
					contextBytes, promptMode = len(bridge), "role_switch"
					prompt = bridge + "\n\nCurrent routed message follows:\n" + message
				}
				slog.Info("agent prompt prepared",
					"chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID,
					"agent", candidate.target.Agent, "provider", candidate.target.Provider, "model", candidate.target.Model,
					"mode", promptMode, "prompt_bytes", len(prompt), "context_bytes", contextBytes,
					"message_bytes", len(message), "common_prompt_bytes", len(commonPrompt(workspace, chat)), "role_prompt_bytes", len(initialRolePrompt(role)),
				)
				p.mu.Lock()
				current.activeTurn, current.cancelRequested, current.sawEvent, current.roleName = turnID, false, false, role.Name
				if current.ended == nil {
					current.ended = make(chan turnEnd, 1)
				}
				p.mu.Unlock()
				var end turnEnd
				end, err = p.promptBus(p.ctx, current, turnID, prompt)
				reason = end.reason
				if end.err != "" {
					endErr = end.err
				}
				p.mu.Lock()
				cancelled := current.cancelRequested
				if current.activeTurn == turnID {
					current.activeTurn, current.cancelRequested = "", false
				}
				if err != nil {
					delete(p.runtimes, key)
				}
				p.mu.Unlock()
				if cancelled {
					reason = "cancelled"
				} else if err != nil {
					reason = "error"
				} else if reason == "" {
					reason = "end_turn"
				}
				if cancelled || reason == "cancelled" {
					attempt["outcome"] = "cancelled"
					attempts = append(attempts, attempt)
					break candidateLoop
				}
				if err != nil {
					attempt["outcome"], attempt["error"] = "prompt_error", err.Error()
					attempts = append(attempts, attempt)
					slog.Warn("agent prompt failed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "agent", candidate.target.Agent, "provider", candidate.target.Provider, "model", candidate.target.Model, "error", err)
					continue candidateLoop
				}
				if reason == "error" {
					attempt["outcome"] = "turn_error"
					if endErr != "" {
						attempt["error"] = endErr
					}
					attempts = append(attempts, attempt)
					slog.Warn("agent turn ended with error", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "agent", candidate.target.Agent, "provider", candidate.target.Provider, "model", candidate.target.Model, "error", endErr)
					continue candidateLoop
				}
				if shouldRetryFreshHermesSession(candidate.target.Agent, fresh, reason, end.hadEvents, retryFresh) {
					attempt["outcome"] = "stale_session_retry"
					attempts = append(attempts, attempt)
					slog.Warn("hermes session refused without events; retrying with fresh session", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "session_id", current.sessionID)
					retryFresh = true
					continue
				}
				attempt["outcome"] = "completed"
				attempts = append(attempts, attempt)
				break candidateLoop
			}
		}
		if reason != "end_turn" && reason != "cancelled" {
			p.emitTurnFailure(chat.ID, turnID, role, reason, attempts, err, endErr)
		}
		if completeErr := p.store.completeTurn(turnID, reason); completeErr != nil {
			slog.Error("chat turn completion persistence failed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "stop_reason", reason, "error", completeErr)
		}
		p.retainTurnMessages(turnID, resolved)
		slog.Info("chat turn completed", "chat_id", chat.ID, "turn_id", turnID, "role_id", role.ID, "role_name", role.Name, "stop_reason", reason, "latency_ms", nowMillis()-turn.StartedAt, "attempts", attempts)
		p.publish("chat:"+chat.ID+":turn-completed", map[string]any{"chat_id": chat.ID, "turn_id": turnID, "stop_reason": reason, "role_id": role.ID, "role_name": role.Name, "attempts": attempts, "sender": map[string]any{"from": "role", "role_id": role.ID, "role_name": role.Name}})
		p.publish("chat:_:turn", map[string]any{"chat_id": chat.ID, "turn_id": turnID, "role_id": role.ID, "role_name": role.Name, "phase": "completed", "stop_reason": reason, "dispatch_id": dispatchID, "agent": resolved.Agent, "provider": resolved.Provider, "model": resolved.Model, "branch_id": target.branch})
		if reason != "cancelled" {
			p.wg.Add(1)
			go func(id, provider string) { defer p.wg.Done(); p.generateTurnSummary(id, provider) }(turnID, summaryProvider)
		}
		if parallel {
			// Send-now turns use a throwaway runtime; drop it once the turn
			// has fully ended so the map only holds reusable sessions.
			p.mu.Lock()
			delete(p.runtimes, key)
			p.mu.Unlock()
		}
		if laneSession != "" {
			// A lane continuation queued behind this turn waits on the
			// session, which a throwaway or cross-key lane run holds no busy
			// key for — kick its queue directly. (For a lane relay's own key
			// this no-ops: the deferred releaseBusy drains it.)
			p.startQueued(runtimeKey(chat.ID, role.ID) + "\x00lane\x00" + laneSession)
		}
		if err != nil || reason == "error" || reason == "cancelled" {
			break
		}
	}
}

func shouldRetryFreshHermesSession(agent string, fresh bool, reason string, hadEvents, alreadyRetried bool) bool {
	return agent == "hermes" && !fresh && reason == "refusal" && !hadEvents && !alreadyRetried
}

// recordTurnTarget persists the turn's routing target and announces it on
// the turn lifecycle feed (phase "target"), so panes label the turn with
// the candidate that actually runs it — the planned candidate right after
// resolution, the failover replacement when a later candidate takes over.
func (p *Plugin) recordTurnTarget(chatID, turnID string, role SuperRole, target agentdriver.Target, dispatchID string) {
	if err := p.store.setTurnTarget(turnID, target.Agent, target.Provider, target.Model); err != nil {
		slog.Warn("chat turn target persistence failed", "chat_id", chatID, "turn_id", turnID, "role_id", role.ID, "error", err)
	}
	p.publish("chat:_:turn", map[string]any{"chat_id": chatID, "turn_id": turnID, "role_id": role.ID, "role_name": role.Name, "phase": "target", "dispatch_id": dispatchID, "agent": target.Agent, "provider": target.Provider, "model": target.Model})
}

// emitTurnFailure records a failed/aborted turn as a visible "error" message
// block so the chat timeline shows what went wrong instead of going silent.
// relayErr is the routing/start/prompt error (may be nil), agentErr the error
// text the agent plugin reported on turn-ended (may be empty).
func (p *Plugin) emitTurnFailure(chatID, turnID string, role SuperRole, reason string, attempts []map[string]any, relayErr error, agentErr string) {
	text := turnFailureText(reason, attempts, relayErr, agentErr)
	payloadJSON, marshalErr := json.Marshal(map[string]any{"stop_reason": reason, "attempts": attempts})
	if marshalErr != nil {
		payloadJSON = []byte("{}")
	}
	block := &MessageBlock{ID: newID(), EventID: newID(), ChatID: chatID, TurnID: turnID, Kind: agentdriver.KindError, Text: text, Payload: string(payloadJSON), OccurredAt: nowMillis()}
	if err := p.store.addMessageBlock(block); err != nil {
		slog.Warn("chat turn failure block persistence failed", "chat_id", chatID, "turn_id", turnID, "error", err)
		return
	}
	payload := block.payload()
	payload["role_id"] = role.ID
	payload["role_name"] = role.Name
	p.publish("chat:"+chatID+":block", payload)
	slog.Info("chat turn failure surfaced", "chat_id", chatID, "turn_id", turnID, "role_id", role.ID, "stop_reason", reason)
}

// turnFailureText renders a one-line summary plus per-attempt detail lines.
func turnFailureText(reason string, attempts []map[string]any, relayErr error, agentErr string) string {
	summary := "Turn failed"
	if reason != "" && reason != "error" {
		summary = "Turn ended: " + reason
	}
	details := []string{}
	for _, attempt := range attempts {
		attemptErr, _ := attempt["error"].(string)
		if attemptErr == "" {
			continue
		}
		target := strings.Join(nonEmpty(
			stringValue(attempt["agent"]), stringValue(attempt["provider"]), stringValue(attempt["model"])), " / ")
		outcome := stringValue(attempt["outcome"])
		if target != "" {
			details = append(details, fmt.Sprintf("%s: %s (%s)", target, attemptErr, outcome))
		} else {
			details = append(details, attemptErr)
		}
	}
	if relayErr != nil {
		details = append(details, relayErr.Error())
	}
	if agentErr != "" && !containsString(details, agentErr) {
		details = append(details, agentErr)
	}
	if len(details) == 0 {
		return summary
	}
	return summary + "\n" + strings.Join(details, "\n")
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle || strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func initialPrompt(workspace Workspace, chat Chat, role SuperRole, history, message string) string {
	sections := nonEmpty(commonPrompt(workspace, chat), initialRolePrompt(role))
	if history != "" {
		sections = append(sections, history+"\n\nCurrent routed message follows:")
	}
	sections = append(sections, message)
	return strings.Join(sections, "\n\n")
}

func commonPrompt(workspace Workspace, chat Chat) string {
	return strings.TrimSpace(strings.Join(nonEmpty(workspace.CommonPrompt, chat.CommonPrompt), "\n\n"))
}

func initialRolePrompt(role SuperRole) string {
	return fmt.Sprintf("You are a persistent Super Workspace role named %q.\n\nRole prompt:\n%s\n\nOperate as this role only. Prefer work that matches the fixed rules, files, topic, and responsibilities above. If a later user message appears unrelated to this role, say so briefly and ask for clarification instead of silently switching tasks.", role.Name, fallback(role.Prompt, "(No role-specific prompt was provided.)"))
}
func nonEmpty(values ...string) []string {
	result := []string{}
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, strings.TrimSpace(value))
		}
	}
	return result
}
func fallback(value, fallbackValue string) string {
	if strings.TrimSpace(value) == "" {
		return fallbackValue
	}
	return value
}

func (p *Plugin) historyPrompt(chatID string, before int64, budget int) (string, error) {
	messages, err := p.store.history(chatID, before, budget)
	if err != nil {
		return "", err
	}
	return renderRecentHistory(messages, "Recent visible chat history:", routerHistoryByteBudget), nil
}

func (p *Plugin) handleStop(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	roleID, _ := value["role_id"].(string)
	turnID, _ := value["turn_id"].(string)
	if err == nil && chatID == "" {
		err = errBadRequest
	}
	stopped := false
	if err == nil {
		stopped, err = p.stopTurn(chatID, roleID, turnID)
	}
	p.reply(frame, map[string]any{"stopped": stopped}, err)
}

// stopTurn cancels in-flight turns of a chat. roleID narrows to one role;
// turnID narrows further to one specific turn — required now that parallel
// send-now turns of the same role can run side by side and the pane stops
// them individually.
func (p *Plugin) stopTurn(chatID, roleID, turnID string) (bool, error) {
	p.mu.Lock()
	targets := []*runtime{}
	for key, current := range p.runtimes {
		if strings.HasPrefix(key, chatID+"\x00") && current.activeTurn != "" && (roleID == "" || current.roleID == roleID) && (turnID == "" || current.activeTurn == turnID) {
			current.cancelRequested = true
			targets = append(targets, current)
		}
	}
	p.mu.Unlock()
	var result error
	for _, current := range targets {
		if p.client == nil {
			break // unit-test plugin without a bus: the cancel flag alone ends the relay turn
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var err error
		_, err = p.client.Request(ctx, current.pluginID+":_:cancel", map[string]any{"session_id": current.sessionID}, 5*time.Second)
		cancel()
		result = errors.Join(result, err)
	}
	return len(targets) > 0, result
}
func (p *Plugin) publishMessage(message *Message) {
	p.publish("chat:"+message.ChatID+":message", message.payload())
}
func (p *Plugin) publish(channel string, value any) {
	if p.client != nil {
		if err := p.client.Publish(context.Background(), channel, value); err != nil {
			slog.Warn("chat bus publish failed", "channel", channel, "error", err)
		}
	}
}
