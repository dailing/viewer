package chat

// Background-process line hold.
//
// Hermes under ACP never delivers its in-process completion notifications
// (its completion queue has no consumer in acp mode), so a turn that spawned
// background terminal processes ends with the work still running and no way
// for the result to come back. This file closes the loop viewer-side: when a
// relay finishes, the line's agent sessions are polled (provider RPC
// <plugin>:_:background, served by viewer.agent-hermes from Hermes's
// processes.json checkpoint); a line with live background work stays busy —
// new user messages queue per the existing line semantics — and a watcher
// polls until the last process exits, then injects a completion dispatch
// into the SAME agent session (resume) so the agent reads the output with
// process_manage and reports. The completion turn itself can spawn new
// background work, in which case the line re-holds.
//
// Scope: the serial line path only. Parallel (send-now) turns fork
// auto-branches with their own sessions and never hold a line.

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"viewer/sdk/go/busclient"
)

// backgroundProcess is one live entry of the provider's background reply.
type backgroundProcess struct {
	SessionID string `json:"session_id"`
	Command   string `json:"command"`
	PID       int    `json:"pid"`
}

// backgroundBlock is one held line: the agent session whose background
// processes keep the line busy, plus the last seen process list (for the
// completion message) and consecutive poll-error count (release tolerance).
type backgroundBlock struct {
	chatID    string
	branchID  string
	role      SuperRole
	pluginID  string
	sessionID string
	processes []backgroundProcess
	errors    int
	since     int64
}

// relaySession records the agent session one relay target's turn ran on,
// returned by runRelay so the caller can test it for background work
// before releasing the line.
type relaySession struct {
	pluginID  string
	sessionID string
	branch    string
	role      SuperRole
}

const (
	backgroundMaxErrors  = 5 // consecutive poll failures before the block is released (agent plugin restart, etc.)
	backgroundRPCTimeout = 5 * time.Second
)

// backgroundPollInterval is a var so tests can shorten the watcher's tick.
var backgroundPollInterval = 3 * time.Second

// trackAgentRegistry mirrors the kernel's retained plugins:_:list feed into
// agentSlots (plugin id → declared slot channels).
func (p *Plugin) trackAgentRegistry(frame busclient.Frame) {
	entries, _ := frame.Value.([]any)
	slots := map[string]map[string]bool{}
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		manifest, _ := entry["manifest"].(map[string]any)
		id, _ := manifest["id"].(string)
		if id == "" {
			continue
		}
		declared := slots[id]
		if declared == nil {
			declared = map[string]bool{}
			slots[id] = declared
		}
		slotMap, _ := manifest["slots"].(map[string]any)
		for channel := range slotMap {
			declared[channel] = true
		}
	}
	p.mu.Lock()
	p.agentSlots = slots
	p.mu.Unlock()
}

// supportsBackground reports whether the plugin's manifest declares the
// background slot. The probe RPC is only ever sent when this holds: chat's
// own *:_ event subscription matches every agent channel, so a request to
// an undeclared channel finds a "subscriber" (chat itself) and hangs to
// the client timeout instead of fast-failing with no_route.
func (p *Plugin) supportsBackground(pluginID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.agentSlots[pluginID][pluginID+":_:background"]
}

// queryBackground asks the agent plugin for the session's live background
// processes. Any RPC failure (including no_route for providers without the
// capability) is returned as an error; callers decide whether that means
// "no background" (turn-end probe) or "retry" (watcher).
func (p *Plugin) queryBackground(pluginID, sessionID string) ([]backgroundProcess, error) {
	if p.client == nil || pluginID == "" || sessionID == "" {
		return nil, nil
	}
	if !p.supportsBackground(pluginID) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(p.ctx, backgroundRPCTimeout)
	defer cancel()
	value, err := p.client.Request(ctx, pluginID+":_:background", map[string]any{"session_id": sessionID}, backgroundRPCTimeout)
	if err != nil {
		return nil, err
	}
	var reply struct {
		Processes []backgroundProcess `json:"processes"`
	}
	if err := decodeInto(value, &reply); err != nil {
		return nil, err
	}
	return reply.Processes, nil
}

// releaseBusyOrHold replaces releaseBusy at relay end: a line whose relay
// sessions still have live background processes stays busy (queued messages
// keep queueing) under a background block; everything else releases
// normally. Probe failures count as no background — a broken provider must
// never wedge the line.
func (p *Plugin) releaseBusyOrHold(keys []string, sessions []relaySession) {
	held := map[string]bool{}
	for _, key := range keys {
		if p.holdForBackground(key, sessions) {
			held[key] = true
		}
	}
	rest := make([]string, 0, len(keys))
	for _, key := range keys {
		if !held[key] {
			rest = append(rest, key)
		}
	}
	if len(rest) > 0 {
		p.releaseBusy(rest)
	}
}

func (p *Plugin) holdForBackground(key string, sessions []relaySession) bool {
	for _, session := range sessions {
		processes, err := p.queryBackground(session.pluginID, session.sessionID)
		if err != nil || len(processes) == 0 {
			continue
		}
		p.mu.Lock()
		p.background[key] = &backgroundBlock{
			chatID: strings.SplitN(key, "\x00", 2)[0], branchID: session.branch,
			role: session.role, pluginID: session.pluginID, sessionID: session.sessionID,
			processes: processes, since: nowMillis(),
		}
		p.mu.Unlock()
		slog.Info("chat line held for background processes", "chat_id", strings.SplitN(key, "\x00", 2)[0], "branch", session.branch, "role_id", session.role.ID, "session_id", session.sessionID, "processes", len(processes))
		p.publishBackground(key)
		p.ensureBackgroundWatcher()
		return true
	}
	return false
}

// ensureBackgroundWatcher starts the shared poll loop on the first block.
func (p *Plugin) ensureBackgroundWatcher() {
	p.mu.Lock()
	if p.backgroundWatching {
		p.mu.Unlock()
		return
	}
	p.backgroundWatching = true
	p.mu.Unlock()
	p.wg.Add(1)
	go p.watchBackground()
}

func (p *Plugin) watchBackground() {
	defer p.wg.Done()
	ticker := time.NewTicker(backgroundPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
		}
		p.mu.Lock()
		if len(p.background) == 0 {
			p.backgroundWatching = false
			p.mu.Unlock()
			return
		}
		blocks := make(map[string]*backgroundBlock, len(p.background))
		for key, block := range p.background {
			blocks[key] = block
		}
		p.mu.Unlock()
		for key, block := range blocks {
			processes, err := p.queryBackground(block.pluginID, block.sessionID)
			p.mu.Lock()
			current := p.background[key]
			if current == nil {
				p.mu.Unlock()
				continue // released meanwhile (stop escape, completion)
			}
			if err != nil {
				current.errors++
				release := current.errors >= backgroundMaxErrors
				p.mu.Unlock()
				if release {
					slog.Warn("chat background block released after repeated poll failures", "chat_id", current.chatID, "session_id", current.sessionID, "error", err)
					p.dropBackgroundBlock(key, "poll_errors")
				}
				continue
			}
			current.errors = 0
			if len(processes) > 0 {
				current.processes = processes
			}
			p.mu.Unlock()
			if len(processes) == 0 {
				p.completeBackground(key)
			}
		}
	}
}

// dropBackgroundBlock removes a block and frees its line without a
// completion turn (poll-error tolerance, stop escape, vanished chat).
func (p *Plugin) dropBackgroundBlock(key, reason string) {
	p.mu.Lock()
	_, existed := p.background[key]
	delete(p.background, key)
	p.mu.Unlock()
	if !existed {
		return
	}
	p.publishBackground(key)
	p.releaseBusy([]string{key})
	slog.Info("chat background block dropped", "line_key", strings.ReplaceAll(key, "\x00", "/"), "reason", reason)
}

// completeBackground fires when the held session's last background process
// exited: it injects a completion dispatch into the SAME agent session
// (resume) on the still-busy line, then lets the normal release drain the
// user queue. The completion turn's own end goes through
// releaseBusyOrHold again, so newly spawned background work re-holds.
func (p *Plugin) completeBackground(key string) {
	p.mu.Lock()
	block := p.background[key]
	if block == nil {
		p.mu.Unlock()
		return
	}
	delete(p.background, key)
	p.mu.Unlock()
	p.publishBackground(key)
	slog.Info("chat background processes finished; dispatching completion", "chat_id", block.chatID, "branch", block.branchID, "session_id", block.sessionID, "held_ms", nowMillis()-block.since)
	chat, err := p.store.chat(block.chatID)
	if err != nil || chat == nil {
		p.releaseBusy([]string{key})
		return
	}
	workspace, err := p.workspace(p.ctx)
	if err != nil {
		slog.Warn("chat background completion aborted: workspace load failed", "chat_id", block.chatID, "error", err)
		p.releaseBusy([]string{key})
		return
	}
	message := backgroundCompletionText(block)
	dispatchID := newID()
	user := &Message{ID: newID(), ChatID: chat.ID, TurnID: dispatchID, Role: "user", Text: message, SenderFrom: "user", CreatedAt: nowMillis()}
	if err := p.store.addMessage(user); err != nil {
		slog.Error("chat background completion message persistence failed", "chat_id", block.chatID, "error", err)
		p.releaseBusy([]string{key})
		return
	}
	p.retainVisibleMessage(user, "", "")
	batchID, inputNode, stale, err := p.intakeBatch(chat.ID, block.branchID, dispatchID)
	if err != nil {
		slog.Error("chat background completion intake failed", "chat_id", block.chatID, "error", err)
		p.releaseBusy([]string{key})
		return
	}
	target := relayTarget{role: block.role, resume: block.sessionID, branch: block.branchID, batch: batchID, inputNode: inputNode}
	if stale {
		target.resume = ""
	}
	p.publishMessage(user, block.branchID)
	// The completion resume inherits the line's raw state (set by the last
	// dispatch on it): on a raw line the synthetic prompt stays injection-free.
	p.mu.Lock()
	raw := p.lineRaw[key]
	p.mu.Unlock()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		sessions := p.runRelay(*chat, workspace, []relayTarget{target}, message, user.CreatedAt, stale, false, raw, dispatchID)
		p.releaseBusyOrHold([]string{key}, sessions)
	}()
}

// releaseBackgroundForChat is the stop-button escape hatch: with no
// in-flight turn to cancel, stop releases the chat's background blocks so
// the user can take the line back (queued entries start draining).
func (p *Plugin) releaseBackgroundForChat(chatID string) int {
	p.mu.Lock()
	keys := []string{}
	for key := range p.background {
		if strings.HasPrefix(key, chatID+"\x00") {
			keys = append(keys, key)
		}
	}
	p.mu.Unlock()
	for _, key := range keys {
		p.dropBackgroundBlock(key, "stopped")
	}
	return len(keys)
}

func backgroundCompletionText(block *backgroundBlock) string {
	lines := make([]string, 0, len(block.processes))
	for _, process := range block.processes {
		lines = append(lines, fmt.Sprintf("- %s（进程会话 id: %s）", process.Command, process.SessionID))
	}
	if len(lines) == 0 {
		lines = append(lines, "-（进程列表在释放前已不可读）")
	}
	return "[后台任务完成通知] 你在此会话中启动的后台进程已全部结束：\n" + strings.Join(lines, "\n") +
		"\n请使用 process_manage 查看它们的输出与退出状态，向我汇报结果，然后继续后续工作。"
}

// publishBackground pushes one line's background-hold state (active with
// the live process list, or inactive after release) on the background feed.
func (p *Plugin) publishBackground(key string) {
	p.mu.Lock()
	block := p.background[key]
	p.mu.Unlock()
	chatID := strings.SplitN(key, "\x00", 2)[0]
	payload := map[string]any{"chat_id": chatID, "active": false}
	if block != nil {
		processes := make([]map[string]any, 0, len(block.processes))
		for _, process := range block.processes {
			processes = append(processes, map[string]any{"session_id": process.SessionID, "command": process.Command, "pid": process.PID})
		}
		payload = map[string]any{"chat_id": chatID, "active": true, "branch_id": block.branchID, "session_id": block.sessionID, "role_id": block.role.ID, "role_name": block.role.Name, "processes": processes, "since": block.since}
	}
	p.publish("chat:_:background", payload)
}

// backgroundChatIDs returns the deduplicated, sorted ids of chats holding a
// background block; chats:list merges them into the running list so the
// Dock dot survives the turn's end while background work runs.
func (p *Plugin) backgroundChatIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := map[string]bool{}
	result := []string{}
	for key := range p.background {
		chatID := strings.SplitN(key, "\x00", 2)[0]
		if !seen[chatID] {
			seen[chatID] = true
			result = append(result, chatID)
		}
	}
	sort.Strings(result)
	return result
}
