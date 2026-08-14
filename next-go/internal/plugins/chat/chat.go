// Package chat implements the Viewer Super Workspace bus plugin.
package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"viewer/internal/agentdriver"
	"viewer/internal/busclient"
	"viewer/internal/plugins/pluginrpc"
)

var Manifest = busclient.Manifest{
	ID: "chat", Version: "0.2.0",
	Slots: map[string]any{
		"chat:_:workspace:get": map[string]any{}, "chat:_:workspace:patch": map[string]any{},
		"chat:_:roles:list": map[string]any{}, "chat:_:roles:create": map[string]any{}, "chat:_:roles:patch": map[string]any{}, "chat:_:roles:delete": map[string]any{},
		"chat:_:routing:get": map[string]any{}, "chat:_:routing:put": map[string]any{},
		"chat:_:chats:list": map[string]any{}, "chat:_:chats:create": map[string]any{}, "chat:_:chats:patch": map[string]any{}, "chat:_:chats:delete": map[string]any{}, "chat:_:chats:activate": map[string]any{},
		"chat:_:dispatch": map[string]any{}, "chat:_:send-message": map[string]any{}, "chat:_:stop": map[string]any{},
		"chat:_:agent-catalog": map[string]any{},
	},
	Emits: map[string]any{
		"chat:*:message": map[string]any{}, "chat:*:turn-completed": map[string]any{}, "chat:_:active": map[string]any{},
	},
}

type agent interface {
	Initialize(context.Context) (map[string]any, error)
	NewSession(context.Context, string) (string, error)
	LoadSession(context.Context, string, string) error
	Prompt(context.Context, string, string) (string, error)
	Cancel(context.Context, string) error
	OnUpdate(func(driverEvent))
	Stderr() string
	Close() error
}
type agentFactory func(context.Context) (agent, string, error)
type Option func(*Plugin)

func WithAgentFactory(factory agentFactory) Option { return func(p *Plugin) { p.factory = factory } }
func WithHTTPClient(client *http.Client) Option    { return func(p *Plugin) { p.httpClient = client } }

type runtime struct {
	agent                                                 agent
	sessionID, profile, cwd, activeTurn, roleID, roleName string
	pluginID, providerKey                                 string
	target                                                agentdriver.Target
	ended                                                 chan string
	cancelRequested                                       bool
	eventSeq                                              int
}
type Plugin struct {
	dataDir      string
	store        *store
	client       *busclient.Client
	factory      agentFactory
	httpClient   *http.Client
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	runtimes     map[string]*runtime
	busy         map[string]bool
	agents       map[string]string
	catalogs     map[string]agentdriver.Catalog
	activeChatID string
	closed       bool
	wg           sync.WaitGroup
}

var (
	errBadRequest  = errors.New("chat_id and message are required")
	errTurnActive  = errors.New("RoutingTargetBusy: chat role already has a turn in progress")
	errProviderM6c = errors.New("provider must be hermes or codex-app-server; opencode is not implemented")
)

func New(dataDir string, options ...Option) (*Plugin, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	database, err := openStore(dataDir)
	if err != nil {
		return nil, err
	}
	p := &Plugin{dataDir: dataDir, store: database, runtimes: map[string]*runtime{}, busy: map[string]bool{}, agents: defaultAgents(), catalogs: map[string]agentdriver.Catalog{}, httpClient: defaultHTTPClient()}
	for _, option := range options {
		option(p)
	}
	return p, nil
}

func (p *Plugin) agentForRole(ctx context.Context, role SuperRole) (agent, string, error) {
	if role.Provider == "codex-app-server" {
		model := ""
		if role.Model != nil {
			model = strings.TrimSpace(*role.Model)
		}
		return p.newCodexAgent(ctx, model)
	}
	return p.factory(ctx)
}
func envBool(name string, fallback bool) bool {
	value, exists := os.LookupEnv(name)
	if !exists {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func (p *Plugin) Start(ctx context.Context, kernelWS string, managed bool) error {
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.client = busclient.New(kernelWS, Manifest, busclient.WithManaged(managed))
	handlers := map[string]func(busclient.Frame){
		"chat:_:workspace:get": p.handleWorkspaceGet, "chat:_:workspace:patch": p.handleWorkspacePatch,
		"chat:_:roles:list": p.handleRolesList, "chat:_:roles:create": p.handleRolesCreate, "chat:_:roles:patch": p.handleRolesPatch, "chat:_:roles:delete": p.handleRolesDelete,
		"chat:_:routing:get": p.handleRoutingGet, "chat:_:routing:put": p.handleRoutingPut,
		"chat:_:chats:list": p.handleChatsList, "chat:_:chats:create": p.handleChatsCreate, "chat:_:chats:patch": p.handleChatsPatch, "chat:_:chats:delete": p.handleChatsDelete, "chat:_:chats:activate": p.handleChatsActivate,
		"chat:_:dispatch": p.handleDispatch, "chat:_:send-message": p.handleDispatch, "chat:_:stop": p.handleStop,
		"chat:_:agent-catalog": p.handleAgentCatalog,
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
	if err := p.client.Connect(ctx); err != nil {
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
	if errors.Is(err, errTurnActive) {
		code = "routing_target_busy"
	}
	if errors.Is(err, errProviderM6c) {
		code = "unsupported_provider"
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
	workspace, err := p.workspace(p.ctx)
	p.reply(frame, workspace.Roles, err)
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
	workspace, err := p.workspace(p.ctx)
	if err == nil {
		workspace.Roles = append(workspace.Roles, role)
		err = p.configSet(p.ctx, "roles", workspace.Roles)
	}
	p.reply(frame, role, err)
}
func (p *Plugin) handleRolesPatch(frame busclient.Frame) {
	value, err := frameObject(frame)
	id, _ := value["id"].(string)
	if err != nil || id == "" {
		p.reply(frame, nil, errors.New("id is required"))
		return
	}
	workspace, err := p.workspace(p.ctx)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	index := -1
	for i := range workspace.Roles {
		if workspace.Roles[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		p.reply(frame, nil, errors.New("role not found"))
		return
	}
	encoded, _ := jsonMap(workspace.Roles[index])
	for key, item := range value {
		if key != "id" && key != "created_at" && key != "updated_at" {
			encoded[key] = item
		}
	}
	role := workspace.Roles[index]
	err = decodeInto(encoded, &role)
	if err == nil {
		role.ID = id
		err = normalizeRole(&role, false)
	}
	if err == nil {
		workspace.Roles[index] = role
		err = p.configSet(p.ctx, "roles", workspace.Roles)
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
	workspace, err := p.workspace(p.ctx)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	roles := make([]SuperRole, 0, len(workspace.Roles))
	found := false
	for _, role := range workspace.Roles {
		if role.ID == id {
			found = true
		} else {
			roles = append(roles, role)
		}
	}
	if !found {
		p.reply(frame, nil, errors.New("role not found"))
		return
	}
	err = p.configSet(p.ctx, "roles", roles)
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
	workspace, err := p.workspace(p.ctx)
	p.reply(frame, RoutingConfig{workspace.DefaultRoutingPolicyID, workspace.RoutingPolicies}, err)
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
		err = p.configSet(p.ctx, "routing", routing)
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
	result := map[string]any{"chats": payload, "active_chat_id": p.activeChatID}
	if request != nil && request["include_messages"] == true {
		chatID, _ := request["chat_id"].(string)
		messages, historyErr := p.store.history(chatID, 0, 0)
		if historyErr != nil {
			p.reply(frame, nil, historyErr)
			return
		}
		values := make([]map[string]any, 0, len(messages))
		for _, message := range messages {
			values = append(values, message.payload())
		}
		result["messages"] = values
	}
	p.reply(frame, result, nil)
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
	if err == nil {
		err = p.store.saveChat(&chat)
	}
	p.reply(frame, chat.payload(), err)
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
	items := make([]*runtime, 0, len(p.runtimes))
	for _, item := range p.runtimes {
		items = append(items, item)
	}
	p.mu.Unlock()
	for _, item := range items {
		if item.agent != nil {
			_ = item.agent.Close()
		}
	}
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
