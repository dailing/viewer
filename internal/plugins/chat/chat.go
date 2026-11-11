// Package chat implements the Viewer Super Workspace bus plugin.
package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"viewer/internal/agentdriver"
	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

var Manifest = busclient.Manifest{
	ID: "chat", Version: "1.0.0",
	Slots: map[string]any{
		"chat:_:workspace:get": map[string]any{}, "chat:_:workspace:patch": map[string]any{},
		"chat:_:roles:list": map[string]any{}, "chat:_:roles:create": map[string]any{}, "chat:_:roles:patch": map[string]any{}, "chat:_:roles:delete": map[string]any{},
		"chat:_:routing:get": map[string]any{}, "chat:_:routing:put": map[string]any{},
		"chat:_:chats:list": map[string]any{}, "chat:_:chats:create": map[string]any{}, "chat:_:chats:patch": map[string]any{}, "chat:_:chats:reorder": map[string]any{}, "chat:_:chats:delete": map[string]any{}, "chat:_:chats:activate": map[string]any{},
		"chat:_:tasks:create": map[string]any{}, "chat:_:tasks:get": map[string]any{}, "chat:_:tasks:list": map[string]any{}, "chat:_:tasks:patch": map[string]any{}, "chat:_:tasks:end": map[string]any{}, "chat:_:tasks:pin": map[string]any{},
		"chat:_:tasks:summary": map[string]any{}, "chat:_:tasks:summary-retry": map[string]any{}, "chat:_:turn-summaries:retry": map[string]any{},
		"chat:_:send-message": map[string]any{}, "chat:_:history": map[string]any{}, "chat:_:turns:list": map[string]any{}, "chat:_:role-sessions:invalidate": map[string]any{},
		"chat:_:decisions:list": map[string]any{}, "chat:_:decisions:respond": map[string]any{}, "chat:_:decisions:cancel": map[string]any{}, "chat:_:decisions:open": map[string]any{}, "chat:_:decisions:context": map[string]any{}, "chat:_:tasks:export": map[string]any{},
		"chat:_:stop": map[string]any{}, "chat:_:queued-cancel": map[string]any{}, "chat:_:queued-update": map[string]any{}, "chat:_:draft:get": map[string]any{}, "chat:_:draft:set": map[string]any{},
		"chat:_:agent-catalog": map[string]any{}, "chat:_:agent-catalog-refresh": map[string]any{}, "chat:_:blocks:list": map[string]any{},
		"chat:_:voice:invoke": map[string]any{},
	},
	Emits: map[string]any{
		"chat:*:message": map[string]any{}, "chat:*:block": map[string]any{}, "chat:*:turn-completed": map[string]any{}, "chat:_:active": map[string]any{},
		"chat:_:turn": map[string]any{}, "chat:_:queue": map[string]any{}, "chat:_:task": map[string]any{}, "chat:_:draft:sync": map[string]any{},
		"chat:_:background": map[string]any{}, "chat:_:decision": map[string]any{}, "chat:_:task-summary": map[string]any{},
		"voice-catalog:_:chat": map[string]any{},
	},
}

type Option func(*Plugin)

func WithHTTPClient(client *http.Client) Option { return func(p *Plugin) { p.httpClient = client } }

// turnEnd carries the terminal outcome of one agent prompt: the wire stop
// reason plus the agent-reported error text (empty when the agent gave none).
type turnEnd struct {
	reason    string
	err       string
	hadEvents bool
}

type runtime struct {
	sessionID, profile, cwd, activeTurn, roleID, roleName string
	pluginID, providerKey                                 string
	target                                                agentdriver.Target
	ended                                                 chan turnEnd
	cancelRequested                                       bool
	sawEvent                                              bool
}
type Plugin struct {
	operationMu        sync.Mutex
	generation         string
	dataDir            string
	store              *store
	client             *busclient.Client
	httpClient         *http.Client
	llmFn              llmCompleter
	ctx                context.Context
	cancel             context.CancelFunc
	mu                 sync.Mutex
	runtimes           map[string]*runtime
	busy               map[string]bool
	queues             map[string][]queuedMessage
	lineRaw            map[string]bool // lineKey → last dispatch's raw flag (background resumes inherit it)
	agents             map[string]string
	catalogs           map[string]agentdriver.Catalog
	turnTasks          map[string]string
	taskSummaryRunning map[string]bool
	taskSummaryPending map[string]bool
	openText           map[string]*Message                 // turnID → currently open assistant text message (deltas append until sealed)
	openBlock          map[string]*MessageBlock            // turnID → currently open streaming block (agent_text/thinking deltas append until sealed)
	openToolCalls      map[string]map[string]*MessageBlock // turnID → tool_call_id → open tool_call block (status updates merge in place)
	activeChatID       string
	closed             bool
	patchedBanks       map[string]bool // chat Hindsight banks whose config was already patched this process
	// background holds one line busy after its relay ended while the turn's
	// agent session still has live background processes (background.go).
	background         map[string]*backgroundBlock
	backgroundWatching bool
	// agentSlots mirrors the kernel's plugins:_:list registry: plugin id →
	// declared RPC slot channels. Background probes are gated on the slot's
	// presence — a request to an undeclared channel never fast-fails
	// (chat's own *:_ event subscription matches every agent channel, so
	// the broker finds a "subscriber") and would hang to its timeout.
	agentSlots map[string]map[string]bool
	wg         sync.WaitGroup
}

var (
	errBadRequest = errors.New("task_id and text are required")
	errQueueFull  = errors.New("QueueFull: task has too many queued messages")
	errNotQueued  = errors.New("the message is no longer queued (already started or cancelled)")
	errTaskEnded  = errors.New("task has ended")
	errTaskBusy   = errors.New("task is still running or queued")
)

func New(dataDir string, options ...Option) (*Plugin, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	database, err := openStore(dataDir)
	if err != nil {
		return nil, err
	}
	p := &Plugin{generation: newID(), dataDir: dataDir, store: database, runtimes: map[string]*runtime{}, busy: map[string]bool{}, queues: map[string][]queuedMessage{}, lineRaw: map[string]bool{}, agents: defaultAgents(), catalogs: map[string]agentdriver.Catalog{}, turnTasks: map[string]string{}, taskSummaryRunning: map[string]bool{}, taskSummaryPending: map[string]bool{}, openText: map[string]*Message{}, openBlock: map[string]*MessageBlock{}, openToolCalls: map[string]map[string]*MessageBlock{}, patchedBanks: map[string]bool{}, background: map[string]*backgroundBlock{}, agentSlots: map[string]map[string]bool{}, httpClient: defaultHTTPClient()}
	for _, option := range options {
		option(p)
	}
	return p, nil
}

func (p *Plugin) Start(ctx context.Context, kernelWS string, managed bool) error {
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.client = busclient.New(kernelWS, Manifest, busclient.WithManaged(managed))
	// Protocol errors (e.g. frame_too_large when a reply exceeds the kernel
	// frame limit) arrive only on this connection's error mailbox; without a
	// callback they are invisible and the RPC caller just times out.
	p.client.OnError(func(entry busclient.ErrorEntry) {
		slog.Warn("bus protocol error", "plugin", Manifest.ID, "code", entry.Code, "message", entry.Message, "detail", entry.Detail)
	})
	handlers := map[string]func(busclient.Frame){
		"chat:_:workspace:get": p.handleWorkspaceGet, "chat:_:workspace:patch": p.handleWorkspacePatch,
		"chat:_:roles:list": p.handleRolesList, "chat:_:roles:create": p.handleRolesCreate, "chat:_:roles:patch": p.handleRolesPatch, "chat:_:roles:delete": p.handleRolesDelete,
		"chat:_:routing:get": p.handleRoutingGet, "chat:_:routing:put": p.handleRoutingPut,
		"chat:_:chats:list": p.handleChatsList, "chat:_:chats:create": p.handleChatsCreate, "chat:_:chats:patch": p.handleChatsPatch, "chat:_:chats:reorder": p.handleChatsReorder, "chat:_:chats:delete": p.handleChatsDelete, "chat:_:chats:activate": p.handleChatsActivate,
		"chat:_:tasks:create": p.handleTaskCreate, "chat:_:tasks:get": p.handleTaskGet, "chat:_:tasks:list": p.handleTasksList, "chat:_:tasks:patch": p.handleTaskPatch, "chat:_:tasks:end": p.handleTaskEnd, "chat:_:tasks:pin": p.handleTaskPin,
		"chat:_:tasks:summary": p.handleTaskSummary, "chat:_:tasks:summary-retry": p.handleTaskSummaryRetry, "chat:_:turn-summaries:retry": p.handleTurnSummaryRetry,
		"chat:_:send-message": p.handleDispatch, "chat:_:history": p.handleChatsList, "chat:_:turns:list": p.handleTurnsList, "chat:_:role-sessions:invalidate": p.handleRoleSessionInvalidate,
		"chat:_:decisions:list": p.handleDecisionsList, "chat:_:decisions:respond": p.handleDecisionRespond, "chat:_:decisions:cancel": p.handleDecisionCancel, "chat:_:decisions:open": p.handleDecisionOpen, "chat:_:decisions:context": p.handleDecisionContext, "chat:_:tasks:export": p.handleTaskExport,
		"chat:_:stop": p.handleStop, "chat:_:queued-cancel": p.handleQueuedCancel, "chat:_:queued-update": p.handleQueuedUpdate, "chat:_:draft:get": p.handleDraftGet, "chat:_:draft:set": p.handleDraftSet,
		"chat:_:agent-catalog": p.handleAgentCatalog, "chat:_:agent-catalog-refresh": p.handleAgentCatalogRefresh, "chat:_:blocks:list": p.handleBlocksList,
		"chat:_:voice:invoke": p.handleVoiceInvoke,
	}
	for pattern, handler := range handlers {
		asyncHandler := handler
		if _, err := p.client.Subscribe(pattern, func(frame busclient.Frame) { go asyncHandler(frame) }); err != nil {
			return err
		}
	}
	if _, err := p.client.Subscribe("*:_", p.handleAgentBusFrame); err != nil {
		return err
	}
	if _, err := p.client.Subscribe("plugins:_:list", p.trackAgentRegistry); err != nil {
		return err
	}
	if err := p.client.Connect(ctx); err != nil {
		return err
	}
	p.llmFn = p.llmCompleteViaBus
	if err := p.recoverTaskWork(); err != nil {
		return err
	}
	if err := p.migrateLegacyDomainConfig(ctx); err != nil {
		return err
	}
	var configured map[string]string
	if err := p.configGet(ctx, "agents", &configured); err == nil && len(configured) > 0 {
		for agentID, pluginID := range configured {
			if strings.TrimSpace(pluginID) != "" {
				p.agents[agentID] = strings.TrimSpace(pluginID)
			}
		}
	}
	activeID, err := p.store.activeChatID()
	if err != nil {
		return err
	}
	p.activeChatID = activeID
	if activeID != "" {
		if err := p.client.Set(context.Background(), "chat:_:active", activeID); err != nil {
			return err
		}
	}
	p.publishVoiceCatalog()
	return nil
}

func (p *Plugin) reply(frame busclient.Frame, value any, err error) {
	if err == nil {
		_ = pluginrpc.Respond(p.client, frame, value)
		return
	}
	code := "chat_error"
	if errors.Is(err, errBadRequest) {
		code = "bad_request"
	}
	if errors.Is(err, errQueueFull) {
		code = "queue_full"
	}
	if errors.Is(err, errNotQueued) {
		code = "not_queued"
	}
	if errors.Is(err, errTaskEnded) {
		code = "task_ended"
	}
	if errors.Is(err, errTaskBusy) {
		code = "task_busy"
	}
	_ = pluginrpc.RespondError(p.client, frame, code, err.Error())
}
func frameObject(frame busclient.Frame) (map[string]any, error) {
	value, ok := pluginrpc.Object(frame)
	if !ok {
		return nil, errors.New("payload must be an object")
	}
	return value, nil
}

// requestInt64 reads a JSON-number field from an RPC request (numbers decode
// as float64), returning 0 when absent or malformed.
func requestInt64(request map[string]any, field string) int64 {
	raw, ok := request[field].(float64)
	if !ok {
		return 0
	}
	return int64(raw)
}

// requestString reads a JSON-string field from an RPC request, returning ""
// when absent or malformed.
func requestString(request map[string]any, field string) string {
	raw, _ := request[field].(string)
	return raw
}

func (p *Plugin) handleWorkspaceGet(frame busclient.Frame) {
	value, err := p.workspace(p.ctx)
	p.reply(frame, value, err)
}
func (p *Plugin) handleWorkspacePatch(frame busclient.Frame) {
	patch, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	workspace, err := p.workspace(p.ctx)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	if value, ok := patch["name"].(string); ok {
		workspace.Name = strings.TrimSpace(value)
	}
	if value, ok := patch["common_prompt"].(string); ok {
		workspace.CommonPrompt = strings.TrimSpace(value)
	}
	err = p.configSet(p.ctx, "workspace", map[string]any{"id": workspace.ID, "name": workspace.Name, "common_prompt": workspace.CommonPrompt})
	p.reply(frame, workspace, err)
}
func (p *Plugin) handleRolesList(frame busclient.Frame) {
	roles, err := p.store.roles()
	p.reply(frame, roles, err)
}
func (p *Plugin) handleRolesCreate(frame busclient.Frame) {
	value, err := frameObject(frame)
	var role SuperRole
	if err == nil {
		err = decodeInto(value, &role)
	}
	if err == nil {
		err = normalizeRole(&role, true)
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	err = p.store.saveRole(&role)
	p.reply(frame, role, err)
}
func (p *Plugin) handleRolesPatch(frame busclient.Frame) {
	value, err := frameObject(frame)
	id, _ := value["id"].(string)
	if err != nil || id == "" {
		p.reply(frame, nil, errors.New("id is required"))
		return
	}
	roles, err := p.store.roles()
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	index := -1
	for i := range roles {
		if roles[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		p.reply(frame, nil, errors.New("role not found"))
		return
	}
	encoded, _ := jsonMap(roles[index])
	for key, item := range value {
		if key != "id" && key != "created_at" && key != "updated_at" {
			encoded[key] = item
		}
	}
	role := roles[index]
	err = decodeInto(encoded, &role)
	if err == nil {
		role.ID = id
		err = normalizeRole(&role, false)
	}
	if err == nil {
		err = p.store.saveRole(&role)
	}
	p.reply(frame, role, err)
}
func (p *Plugin) handleRolesDelete(frame busclient.Frame) {
	value, err := frameObject(frame)
	id, _ := value["id"].(string)
	if err != nil || id == "" {
		p.reply(frame, nil, errors.New("id is required"))
		return
	}
	roles, err := p.store.roles()
	found := false
	for _, role := range roles {
		found = found || role.ID == id
	}
	if err == nil && !found {
		p.reply(frame, nil, errors.New("role not found"))
		return
	}
	if err == nil {
		err = p.store.deleteRole(id)
	}
	if err == nil {
		chats, listErr := p.store.chats()
		if listErr != nil {
			err = listErr
		}
		for i := range chats {
			members := decodeStrings(chats[i].MemberRoleIDsJSON)
			filtered := members[:0]
			for _, memberID := range members {
				if memberID != id {
					filtered = append(filtered, memberID)
				}
			}
			overrides := decodeStringMap(chats[i].RoleRoutingOverridesJSON)
			_, hadOverride := overrides[id]
			delete(overrides, id)
			if len(filtered) != len(members) || hadOverride {
				chats[i].MemberRoleIDsJSON = encodeJSON(filtered)
				chats[i].RoleRoutingOverridesJSON = encodeJSON(overrides)
				chats[i].UpdatedAt = nowMillis()
				if saveErr := p.store.saveChat(&chats[i]); saveErr != nil {
					err = saveErr
					break
				}
			}
		}
	}
	p.reply(frame, map[string]any{"deleted": true, "id": id}, err)
}
func (p *Plugin) handleRoutingGet(frame busclient.Frame) {
	policies, err := p.store.routingPolicies()
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	defaultID, err := p.store.defaultRoutingPolicyID()
	p.reply(frame, RoutingConfig{DefaultRoutingPolicyID: defaultID, RoutingPolicies: policies}, err)
}
func (p *Plugin) handleRoutingPut(frame busclient.Frame) {
	value, err := frameObject(frame)
	var routing RoutingConfig
	if err == nil {
		err = decodeInto(value, &routing)
	}
	if err == nil {
		err = validateRouting(routing)
	}
	if err == nil {
		err = p.store.replaceRouting(routing)
	}
	p.reply(frame, routing, err)
}

func validateRouting(value RoutingConfig) error {
	ids := map[string]bool{}
	for _, policy := range value.RoutingPolicies {
		if strings.TrimSpace(policy.ID) == "" || ids[policy.ID] {
			return errors.New("routing policy ids must be non-empty and unique")
		}
		ids[policy.ID] = true
		candidateIDs := map[string]bool{}
		for _, candidate := range policy.Candidates {
			if candidate.ID == "" || candidateIDs[candidate.ID] {
				return fmt.Errorf("candidate ids must be non-empty and unique in policy %s", policy.ID)
			}
			candidateIDs[candidate.ID] = true
		}
	}
	if value.DefaultRoutingPolicyID != "" && !ids[value.DefaultRoutingPolicyID] {
		return errors.New("default routing policy must reference an existing policy")
	}
	return nil
}

func (p *Plugin) handleChatsList(frame busclient.Frame) {
	request, _ := pluginrpc.Object(frame)
	chats, err := p.store.chats()
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	payload := make([]map[string]any, 0, len(chats))
	for _, chat := range chats {
		payload = append(payload, chat.payload())
	}
	result := map[string]any{"chats": payload, "active_chat_id": p.activeChatID, "running_chat_ids": p.runningChatIDs()}
	taskID := requestString(request, "task_id")
	if taskID == "" {
		p.reply(frame, result, nil)
		return
	}
	task, err := p.store.task(taskID)
	if err != nil || task == nil {
		if err == nil {
			err = errors.New("task not found")
		}
		p.reply(frame, nil, err)
		return
	}
	result["task"] = task.payload()
	result["running_turns"] = p.runningTaskTurns(taskID)
	result["background_task_ids"] = p.backgroundTaskIDs(task.ChatID)
	targets, err := p.store.chatTurnTargets(taskID)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	result["turn_targets"] = turnTargetsPayload(targets)
	sessions, err := p.store.chatTurnSessions(taskID)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	result["turn_sessions"] = turnSessionsPayload(sessions)
	queued := []map[string]any{}
	for _, entry := range p.queuedSnapshot(task.ChatID) {
		if entry["task_id"] == taskID {
			queued = append(queued, entry)
		}
	}
	result["queued_messages"] = queued
	if request["include_messages"] != true {
		p.reply(frame, result, nil)
		return
	}
	var messages []Message
	var hasMore, hasMoreAfter bool
	if around := requestInt64(request, "around"); around > 0 {
		messages, hasMore, hasMoreAfter, err = p.store.historyAround(taskID, around, requestString(request, "message_id"), int(requestInt64(request, "limit")))
	} else if after := requestInt64(request, "after"); after > 0 {
		messages, hasMoreAfter, err = p.store.historyPageAfter(taskID, after, requestString(request, "after_id"), int(requestInt64(request, "limit")))
	} else {
		messages, hasMore, err = p.store.historyPage(taskID, requestInt64(request, "before"), requestString(request, "before_id"), int(requestInt64(request, "limit")))
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	values := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		values = append(values, message.payload())
	}
	result["messages"] = values
	result["has_more"] = hasMore
	result["has_more_after"] = hasMoreAfter
	p.reply(frame, result, nil)
}

// blocksReplyBudget approximates the encoded size of one blocks:list reply.
// The kernel rejects published frames above protocol.DefaultFrameSize (1 MiB)
// asynchronously (frame_too_large to the publisher's error mailbox only), so
// an unbounded reply would vanish silently and the caller would hang until
// its RPC timeout. The estimate charges 2x text+payload length for JSON
// escaping plus a fixed per-block envelope.
const blocksReplyBudget = 700 * 1024

// turnTargetsPayload renders targeted turns as turn_id → execution target
// for the pane's routing labels. dispatch_id links a turn back to its
// triggering user message (whose turn_id is the dispatch id), letting the
// user box's "→" label rebuild from the same records.
func turnTargetsPayload(turns []Turn) map[string]any {
	values := make(map[string]any, len(turns))
	for _, turn := range turns {
		values[turn.ID] = map[string]any{
			"dispatch_id": turn.DispatchID,
			"role_id":     turn.RoleID,
			"role_name":   turn.RoleName,
			"agent":       turn.Agent,
			"provider":    turn.Provider,
			"model":       turn.Model,
		}
	}
	return values
}

// turnSessionsPayload renders sessioned turns as turn_id → session info so
// the pane can group boxes into session lanes and target lane continuations.
// started_at lets the pane order lanes and pick a lane's latest turn without
// a second query.
func turnSessionsPayload(turns []Turn) map[string]any {
	values := make(map[string]any, len(turns))
	for _, turn := range turns {
		values[turn.ID] = map[string]any{
			"session_id":  turn.SessionID,
			"dispatch_id": turn.DispatchID,
			"role_id":     turn.RoleID,
			"role_name":   turn.RoleName,
			"started_at":  turn.StartedAt,
			"task_id":     turn.TaskID,
		}
	}
	return values
}

func (p *Plugin) handleBlocksList(frame busclient.Frame) {
	request, _ := pluginrpc.Object(frame)
	taskID := requestString(request, "task_id")
	if taskID == "" {
		p.reply(frame, nil, errBadRequest)
		return
	}
	task, err := p.store.task(taskID)
	if err != nil || task == nil {
		if err == nil {
			err = errors.New("task not found")
		}
		p.reply(frame, nil, err)
		return
	}
	blocks, err := p.store.chatMessageBlocks(taskID, requestInt64(request, "after"), requestInt64(request, "before"))
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	turns, err := p.store.chatTurns(taskID)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	turnRoles := make(map[string]Turn, len(turns))
	for _, turn := range turns {
		turnRoles[turn.ID] = turn
	}
	values, truncated, nextAfter := budgetBlockPayloads(blocks, turnRoles)
	result := map[string]any{"blocks": values}
	targeted := make([]Turn, 0, len(turns))
	sessioned := make([]Turn, 0, len(turns))
	for _, turn := range turns {
		if turn.Agent != "" {
			targeted = append(targeted, turn)
		}
		if turn.SessionID != "" {
			sessioned = append(sessioned, turn)
		}
	}
	result["turn_targets"] = turnTargetsPayload(targeted)
	result["turn_sessions"] = turnSessionsPayload(sessioned)
	if truncated {
		// Resume with after=next_after; boundary blocks sharing the same
		// occurred_at are re-sent and deduplicated by id on the client.
		result["truncated"] = true
		result["next_after"] = nextAfter
	}
	p.reply(frame, result, nil)
}

// budgetBlockPayloads renders blocks to reply payloads, stopping before the
// estimated encoded size would exceed blocksReplyBudget (but always emitting
// at least one block so the cursor keeps advancing). nextAfter is the
// occurred_at of the first omitted block — an inclusive resume cursor.
func budgetBlockPayloads(blocks []MessageBlock, turnRoles map[string]Turn) (values []map[string]any, truncated bool, nextAfter int64) {
	values = make([]map[string]any, 0, len(blocks))
	size := 0
	for index, block := range blocks {
		estimate := 2*(len(block.Text)+len(block.Payload)) + 256
		if len(values) > 0 && size+estimate > blocksReplyBudget {
			return values, true, blocks[index].OccurredAt
		}
		payload := block.payload()
		if turn, ok := turnRoles[block.TurnID]; ok {
			payload["role_id"] = turn.RoleID
			payload["role_name"] = turn.RoleName
		}
		values = append(values, payload)
		size += estimate
	}
	return values, false, 0
}
func (p *Plugin) handleChatsCreate(frame busclient.Frame) {
	value, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	now := nowMillis()
	chat := Chat{ID: newID(), Name: "New Chat", Type: "group", CreatedAt: now, UpdatedAt: now, MemberRoleIDsJSON: "[]", RoleRoutingOverridesJSON: "{}"}
	applyChatPatch(&chat, value)
	if strings.TrimSpace(chat.Root) == "" {
		p.reply(frame, nil, errors.New("root is required"))
		return
	}
	chat.Root, err = filepath.Abs(chat.Root)
	var task *Task
	if err == nil {
		task = &Task{ID: newID(), ChatID: chat.ID, Title: "主线", TitleSource: "default", CreatedBy: "chat", Pinned: chat.Pinned, CreatedAt: now, UpdatedAt: now}
		err = p.store.createChatWithDefaultTask(&chat, task)
	}
	if err == nil {
		p.publishVoiceCatalog()
		p.publishTask(task, "created")
	}
	reply := chat.payload()
	if task != nil {
		reply["default_task_id"] = task.ID
		reply["task"] = task.payload()
	}
	p.reply(frame, reply, err)
}
func (p *Plugin) handleChatsReorder(frame busclient.Frame) {
	value, err := frameObject(frame)
	var orderedIDs []string
	if err == nil {
		err = decodeInto(value["ordered_ids"], &orderedIDs)
	}
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	chats, err := p.store.reorderChats(orderedIDs)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	payload := make([]map[string]any, 0, len(chats))
	for _, chat := range chats {
		payload = append(payload, chat.payload())
	}
	p.reply(frame, map[string]any{"chats": payload}, nil)
}
func (p *Plugin) handleChatsPatch(frame busclient.Frame) {
	value, err := frameObject(frame)
	id, _ := value["id"].(string)
	if err != nil || id == "" {
		p.reply(frame, nil, errors.New("id is required"))
		return
	}
	chat, err := p.store.chat(id)
	if err != nil || chat == nil {
		if err == nil {
			err = errors.New("chat not found")
		}
		p.reply(frame, nil, err)
		return
	}
	applyChatPatch(chat, value)
	chat.UpdatedAt = nowMillis()
	if chat.Root == "" {
		err = errors.New("root is required")
	} else {
		chat.Root, err = filepath.Abs(chat.Root)
	}
	if err == nil {
		err = p.store.saveChat(chat)
	}
	if err == nil {
		p.publishVoiceCatalog()
	}
	p.reply(frame, chat.payload(), err)
}
func applyChatPatch(chat *Chat, value map[string]any) {
	if item, ok := value["name"].(string); ok {
		chat.Name = strings.TrimSpace(item)
		if chat.Name == "" {
			chat.Name = "New Chat"
		}
	}
	if item, ok := value["type"].(string); ok {
		chat.Type = item
	}
	if item, ok := value["pinned"].(bool); ok {
		chat.Pinned = item
	}
	if item, ok := value["root"].(string); ok {
		chat.Root = strings.TrimSpace(item)
	}
	if item, ok := value["common_prompt"].(string); ok {
		chat.CommonPrompt = strings.TrimSpace(item)
	}
	if item, ok := value["member_role_ids"]; ok {
		var ids []string
		if decodeInto(item, &ids) == nil {
			chat.MemberRoleIDsJSON = encodeJSON(ids)
		}
	}
	if item, ok := value["role_routing_policy_overrides"]; ok {
		var overrides map[string]string
		if decodeInto(item, &overrides) == nil {
			chat.RoleRoutingOverridesJSON = encodeJSON(overrides)
		}
	}
}
func (p *Plugin) handleChatsDelete(frame busclient.Frame) {
	p.operationMu.Lock()
	defer p.operationMu.Unlock()
	value, err := frameObject(frame)
	id, _ := value["id"].(string)
	if err == nil && id == "" {
		err = errors.New("id is required")
	}
	if err == nil {
		err = p.store.deleteChat(id)
	}
	if err == nil && p.activeChatID == id {
		p.activeChatID = ""
		err = p.store.setActiveChatID("")
		if err == nil {
			err = p.client.Set(context.Background(), "chat:_:active", "")
		}
	}
	if err == nil {
		p.publishVoiceCatalog()
	}
	p.reply(frame, map[string]any{"deleted": err == nil, "id": id}, err)
}
func (p *Plugin) handleChatsActivate(frame busclient.Frame) {
	value, err := frameObject(frame)
	id, _ := value["id"].(string)
	var chat *Chat
	if err == nil {
		chat, err = p.store.chat(id)
		if chat == nil && err == nil {
			err = errors.New("chat not found")
		}
	}
	if err == nil {
		p.activeChatID = id
		err = p.store.setActiveChatID(id)
		if err == nil {
			err = p.client.Set(context.Background(), "chat:_:active", id)
		}
	}
	if chat == nil {
		p.reply(frame, nil, err)
	} else {
		p.reply(frame, chat.payload(), err)
	}
}

func jsonMap(value any) (map[string]any, error) {
	result := map[string]any{}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(data, &result)
	return result, err
}

func (p *Plugin) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Unlock()
	p.wg.Wait()
	var busErr error
	if p.client != nil {
		busErr = p.client.Close()
	}
	return errors.Join(busErr, p.store.close())
}
func newID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}
