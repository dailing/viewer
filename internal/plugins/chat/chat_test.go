package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"viewer/internal/agentdriver"
	"viewer/internal/kernel"
	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

func TestRenderAndParseRouter(t *testing.T) {
	roles := []SuperRole{{ID: "one", Name: "One", Description: "first", Provider: "hermes"}, {ID: "two", Name: "Two", Description: "second", Provider: "hermes"}}
	prompt := renderDispatchPrompt("current", roles, "User: earlier")
	for _, want := range []string{"current", "User: earlier", `"description": "first"`, `"id": "two"`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	ids, rationale, err := parseRoute("```json\n{\"role_ids\":[\"two\",\"bogus\",\"two\"],\"rationale\":\"fit\"}\n```", roles)
	if err != nil || len(ids) != 1 || ids[0] != "two" || rationale != "fit" {
		t.Fatalf("ids=%v rationale=%q err=%v", ids, rationale, err)
	}
	ids, rationale, err = parseRoute("乱码", roles)
	if err != nil || len(ids) != 1 || ids[0] != "one" || !strings.Contains(rationale, "fell back") {
		t.Fatalf("fallback ids=%v rationale=%q err=%v", ids, rationale, err)
	}
}

func TestRenderRecentHistoryCapsBytesAndKeepsNewest(t *testing.T) {
	messages := []Message{
		{Role: "assistant", RoleID: "old", RoleName: "old-role", Text: strings.Repeat("old ", 80)},
		{Role: "user", Text: "最新消息必须保留"},
	}
	const budget = 96
	got := renderRecentHistory(messages, "Recent:", budget)
	if len(got) > budget {
		t.Fatalf("rendered history bytes = %d, want <= %d: %q", len(got), budget, got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("rendered history is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "最新消息必须保留") {
		t.Fatalf("newest message missing: %q", got)
	}
	if strings.Count(got, "old ") >= 80 {
		t.Fatalf("older content should be truncated before newest content: %q", got)
	}
}

func TestCapRecentContextPreservesSuffixWithinByteBudget(t *testing.T) {
	input := strings.Repeat("较早内容", 80) + "\nLATEST-CONTEXT"
	const budget = 100
	got := capRecentContext(input, budget)
	if len(got) > budget {
		t.Fatalf("context bytes = %d, want <= %d", len(got), budget)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("context is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "LATEST-CONTEXT") {
		t.Fatalf("recent suffix missing: %q", got)
	}
	if !strings.HasPrefix(got, "Older context omitted") {
		t.Fatalf("omission marker missing: %q", got)
	}
}

func TestRouterCompletion(t *testing.T) {
	// The HTTP layer (endpoint normalization, auth header, JSON mode) is the
	// llm plugin's job and is tested there; here the completer is faked.
	var captured []map[string]string
	complete := func(_ context.Context, messages []map[string]string, jsonMode bool, _ int) (completionResult, error) {
		captured = messages
		if !jsonMode {
			t.Errorf("router should request json mode")
		}
		return completionResult{Content: `{"role_ids":["r2"],"rationale":"best"}`, Model: "fake"}, nil
	}
	roles := []SuperRole{{ID: "r1", Name: "One", Description: "alpha", Provider: "hermes"}, {ID: "r2", Name: "Two", Description: "beta", Provider: "hermes"}}
	ids, rationale, err := routeWithLLM(context.Background(), complete, "choose", roles, "history")
	if err != nil || len(ids) != 1 || ids[0] != "r2" || rationale != "best" {
		t.Fatalf("ids=%v rationale=%q err=%v", ids, rationale, err)
	}
	data, _ := json.Marshal(captured)
	if !strings.Contains(string(data), "history") || !strings.Contains(string(data), "choose") {
		t.Fatalf("request prompt missing context: %s", data)
	}
}

func TestRoleNormalizationAndMessageSender(t *testing.T) {
	role := SuperRole{Name: " Role ", Prompt: " Run prompt "}
	if err := normalizeRole(&role, true); err != nil || role.Name != "Role" || role.Prompt != "Run prompt" || role.SessionPolicy != "reuse" {
		t.Fatalf("role=%#v err=%v", role, err)
	}
	message := Message{ID: "m", ChatID: "c", TurnID: "t", Role: "assistant", Text: "x", SenderFrom: "role", RoleID: "r", RoleName: "R"}
	sender := message.payload()["sender"].(map[string]any)
	if sender["role_id"] != "r" || sender["role_name"] != "R" || sender["from"] != "role" {
		t.Fatal(sender)
	}
}

func TestLegacyRolesAndRoutingMigrateFromConfigToPluginDB(t *testing.T) {
	config := kernel.DefaultConfig()
	config.Host, config.Port = "127.0.0.1", 0
	server := kernel.New(config)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer server.Shutdown(context.Background())
	url := fmt.Sprintf("ws://127.0.0.1:%d/ws", server.Port())
	legacy := map[string]any{
		"roles":   []map[string]any{{"id": "legacy-role", "name": "Legacy", "description": "dispatch", "prompt": "run", "provider": "hermes", "model": "legacy-model", "session_policy": "reuse"}},
		"routing": map[string]any{"default_routing_policy_id": "", "routing_policies": []any{}},
	}
	configClient := busclient.New(url, busclient.Manifest{ID: "chat-migration-config", Version: "0.1.0", Slots: map[string]any{"config:_:get": map[string]any{}, "config:_:set": map[string]any{}}, Emits: map[string]any{}})
	_, _ = configClient.Subscribe("config:_:get", func(frame busclient.Frame) {
		value, _ := frame.Value.(map[string]any)
		key, _ := value["key"].(string)
		_ = pluginrpc.Respond(configClient, frame, legacy[key])
	})
	_, _ = configClient.Subscribe("config:_:set", func(frame busclient.Frame) {
		value, _ := frame.Value.(map[string]any)
		key, _ := value["key"].(string)
		if value["value"] == nil {
			delete(legacy, key)
		} else {
			legacy[key] = value["value"]
		}
		_ = pluginrpc.Respond(configClient, frame, map[string]any{"key": key})
	})
	if err := configClient.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer configClient.Close()

	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(ctx, url, false); err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	roles, err := p.store.roles()
	if err != nil || len(roles) != 1 || roles[0].RoutingPolicyID == "" || roles[0].Provider != "" || roles[0].Model != nil {
		t.Fatalf("roles=%#v err=%v", roles, err)
	}
	policies, err := p.store.routingPolicies()
	if err != nil || len(policies) != 1 || len(policies[0].Candidates) != 1 || policies[0].Candidates[0].AgentID != "hermes" || policies[0].Candidates[0].ProviderID != "default" {
		t.Fatalf("policies=%#v err=%v", policies, err)
	}
	if _, ok := legacy["roles"]; ok {
		t.Fatal("legacy roles key was not removed")
	}
	if _, ok := legacy["routing"]; ok {
		t.Fatal("legacy routing key was not removed")
	}
	columns, err := p.store.db.Migrator().ColumnTypes(&SuperRole{})
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range columns {
		if column.Name() == "provider" || column.Name() == "model" {
			t.Fatalf("legacy direct target column persisted: %s", column.Name())
		}
	}
}

func TestForceNewSessionStartsFreshSession(t *testing.T) {
	config := kernel.DefaultConfig()
	config.Host, config.Port = "127.0.0.1", 0
	server := kernel.New(config)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	defer server.Shutdown(context.Background())
	url := fmt.Sprintf("ws://127.0.0.1:%d/ws", server.Port())

	configClient := busclient.New(url, busclient.Manifest{ID: "force-new-config", Version: "0.1.0", Slots: map[string]any{"config:_:get": map[string]any{}}, Emits: map[string]any{}})
	_, _ = configClient.Subscribe("config:_:get", func(frame busclient.Frame) {
		_ = pluginrpc.Respond(configClient, frame, nil)
	})
	if err := configClient.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer configClient.Close()

	// Fake agent runtime: answers _:start, records the requested session_id
	// (empty = fresh session wanted) and hands out incrementing session ids.
	var mu sync.Mutex
	requestedIDs := []string{}
	starts := 0
	agent := busclient.New(url, busclient.Manifest{ID: "viewer.agent-hermes", Version: "0.1.0", Slots: map[string]any{"viewer.agent-hermes:_:start": map[string]any{}}, Emits: map[string]any{}})
	_, _ = agent.Subscribe("viewer.agent-hermes:_:start", func(frame busclient.Frame) {
		value, _ := frame.Value.(map[string]any)
		requested, _ := value["session_id"].(string)
		mu.Lock()
		starts++
		requestedIDs = append(requestedIDs, requested)
		sessionID := fmt.Sprintf("sess-%d", starts)
		if requested != "" {
			sessionID = requested // a real agent resumes and keeps the id
		}
		mu.Unlock()
		_ = pluginrpc.Respond(agent, frame, map[string]any{"session_id": sessionID, "resumed": requested != ""})
	})
	if err := agent.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(ctx, url, false); err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	chat := Chat{ID: "chat-fn", Root: t.TempDir()}
	role := SuperRole{ID: "role-fn", Name: "FN"}
	candidate := resolvedCandidate{pluginID: "viewer.agent-hermes", target: agentdriver.Target{Agent: "hermes", Provider: "default", Model: "m"}}
	canonicalKey := runtimeKey(chat.ID, role.ID)

	first, fresh, err := p.ensureBusRuntime(ctx, chat, role, candidate, "turn-1", canonicalKey, false, false)
	if err != nil || !fresh || first.sessionID != "sess-1" {
		t.Fatalf("first start: fresh=%v session=%#v err=%v", fresh, first, err)
	}
	again, fresh, err := p.ensureBusRuntime(ctx, chat, role, candidate, "turn-2", canonicalKey, false, false)
	if err != nil || fresh || again.sessionID != "sess-1" {
		t.Fatalf("second dispatch must reuse the in-memory runtime: fresh=%v session=%s err=%v", fresh, again.sessionID, err)
	}
	// Drop the in-memory runtime to also cover the role_sessions restore path:
	// a normal dispatch must resume sess-1, a forced one must NOT.
	p.mu.Lock()
	delete(p.runtimes, runtimeKey(chat.ID, role.ID))
	p.mu.Unlock()
	resumed, fresh, err := p.ensureBusRuntime(ctx, chat, role, candidate, "turn-3", canonicalKey, false, false)
	if err != nil || fresh || resumed.sessionID != "sess-1" {
		t.Fatalf("dispatch after restart must resume the stored session: fresh=%v session=%s err=%v", fresh, resumed.sessionID, err)
	}
	forced, fresh, err := p.ensureBusRuntime(ctx, chat, role, candidate, "turn-4", canonicalKey, true, false)
	if err != nil || !fresh || forced.sessionID == "sess-1" {
		t.Fatalf("force_new_session must start a fresh session: fresh=%v session=%s err=%v", fresh, forced.sessionID, err)
	}
	after, fresh, err := p.ensureBusRuntime(ctx, chat, role, candidate, "turn-5", canonicalKey, false, false)
	if err != nil || fresh || after.sessionID != forced.sessionID {
		t.Fatalf("the session after a forced one must be reused: fresh=%v session=%s err=%v", fresh, after.sessionID, err)
	}
	// Ephemeral (parallel send-now) runtimes must neither resume nor overwrite
	// the stored role session.
	ephemeral, fresh, err := p.ensureBusRuntime(ctx, chat, role, candidate, "turn-6", canonicalKey+"\x00turn-6", true, true)
	if err != nil || !fresh || ephemeral.sessionID == after.sessionID {
		t.Fatalf("ephemeral runtime must start a fresh session: fresh=%v session=%s err=%v", fresh, ephemeral.sessionID, err)
	}
	stored, err := p.store.roleSession(chat.ID, role.ID)
	if err != nil || stored == nil || stored.ProviderSessionID != after.sessionID {
		t.Fatalf("ephemeral runtime must not overwrite the stored session: stored=%#v err=%v", stored, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if starts != 4 {
		t.Fatalf("expected 4 agent starts (initial, resume, forced, ephemeral), got %d (%v)", starts, requestedIDs)
	}
	// turn-3 asked to resume sess-1; turn-4 (forced) and turn-6 (ephemeral)
	// must have asked for brand-new sessions (empty session_id).
	if requestedIDs[1] != "sess-1" || requestedIDs[2] != "" || requestedIDs[3] != "" {
		t.Fatalf("start requests: %v", requestedIDs)
	}
}

// TestQueuedAndParallelDispatch covers the message-queue semantics end to end
// over a real kernel: a dispatch to a busy chat+role is queued and starts only
// after the in-flight turn ends, while a parallel dispatch bypasses the busy
// lock immediately on a throwaway session that never touches the stored role
// session.
func TestQueuedAndParallelDispatch(t *testing.T) {
	config := kernel.DefaultConfig()
	config.Host, config.Port = "127.0.0.1", 0
	server := kernel.New(config)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	defer server.Shutdown(context.Background())
	url := fmt.Sprintf("ws://127.0.0.1:%d/ws", server.Port())

	configClient := busclient.New(url, busclient.Manifest{ID: "queue-config", Version: "0.1.0", Slots: map[string]any{"config:_:get": map[string]any{}}, Emits: map[string]any{}})
	_, _ = configClient.Subscribe("config:_:get", func(frame busclient.Frame) {
		_ = pluginrpc.Respond(configClient, frame, nil)
	})
	if err := configClient.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer configClient.Close()

	// Fake agent: answers _:start with incrementing session ids (echoing a
	// requested resume id), records every _:prompt, and lets the test drive
	// turn ends by publishing _:turn-ended frames manually.
	type promptRecord struct{ sessionID, turnID, text string }
	prompts := make(chan promptRecord, 16)
	var mu sync.Mutex
	starts := 0
	agent := busclient.New(url, busclient.Manifest{
		ID:      "viewer.agent-hermes",
		Version: "0.1.0",
		Slots:   map[string]any{"viewer.agent-hermes:_:start": map[string]any{}, "viewer.agent-hermes:_:prompt": map[string]any{}},
		Emits:   map[string]any{"viewer.agent-hermes:_:catalog": map[string]any{}, "viewer.agent-hermes:_:event": map[string]any{}, "viewer.agent-hermes:_:turn-ended": map[string]any{}},
	})
	_, _ = agent.Subscribe("viewer.agent-hermes:_:start", func(frame busclient.Frame) {
		value, _ := frame.Value.(map[string]any)
		requested, _ := value["session_id"].(string)
		mu.Lock()
		starts++
		sessionID := fmt.Sprintf("sess-%d", starts)
		if requested != "" {
			sessionID = requested
		}
		mu.Unlock()
		_ = pluginrpc.Respond(agent, frame, map[string]any{"session_id": sessionID, "resumed": requested != ""})
	})
	_, _ = agent.Subscribe("viewer.agent-hermes:_:prompt", func(frame busclient.Frame) {
		value, _ := frame.Value.(map[string]any)
		record := promptRecord{}
		record.sessionID, _ = value["session_id"].(string)
		record.turnID, _ = value["turn_id"].(string)
		record.text, _ = value["text"].(string)
		prompts <- record
		_ = pluginrpc.Respond(agent, frame, map[string]any{"ok": true})
	})
	if err := agent.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if err := agent.Set(ctx, "viewer.agent-hermes:_:catalog", agentdriver.Catalog{Agent: "hermes", Providers: []agentdriver.ProviderCatalog{{Provider: "default", Models: []string{"m"}}}}); err != nil {
		t.Fatal(err)
	}

	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(ctx, url, false); err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	role := SuperRole{ID: "role-q", Name: "Q", Description: "queue test role", RoutingPolicyID: "pol-q", CreatedAt: nowMillis(), UpdatedAt: nowMillis()}
	policy := RoutingPolicyConfig{ID: "pol-q", Name: "Q policy", Enabled: true, Candidates: []RoutingCandidateConfig{{ID: "cand-q", AgentID: "hermes", ProviderID: "default", ModelID: "m", Enabled: true}}}
	if err := p.store.importDomain([]SuperRole{role}, RoutingConfig{DefaultRoutingPolicyID: "pol-q", RoutingPolicies: []RoutingPolicyConfig{policy}}); err != nil {
		t.Fatal(err)
	}
	chat := Chat{ID: "chat-q", Name: "Q chat", Root: t.TempDir(), MemberRoleIDsJSON: `["role-q"]`, CreatedAt: nowMillis(), UpdatedAt: nowMillis()}
	if err := p.store.saveChat(&chat); err != nil {
		t.Fatal(err)
	}

	caller := busclient.New(url, busclient.Manifest{ID: "queue-caller", Version: "0.1.0", Slots: map[string]any{}, Emits: map[string]any{}})
	if err := caller.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer caller.Close()

	type dispatchReply struct {
		Started []string `json:"started_role_ids"`
		Queued  []string `json:"queued_role_ids"`
		ID      string   `json:"dispatch_id"`
	}
	dispatch := func(payload map[string]any) dispatchReply {
		value, err := caller.Request(ctx, "chat:_:dispatch", payload, 10*time.Second)
		if err != nil {
			t.Fatalf("dispatch %v: %v", payload, err)
		}
		var reply dispatchReply
		raw, _ := json.Marshal(value)
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatalf("decode dispatch reply: %v", err)
		}
		return reply
	}
	nextPrompt := func() promptRecord {
		select {
		case record := <-prompts:
			return record
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for agent prompt")
			return promptRecord{}
		}
	}
	endTurn := func(record promptRecord) {
		if err := agent.Publish(ctx, "viewer.agent-hermes:_:turn-ended", map[string]any{"session_id": record.sessionID, "turn_id": record.turnID, "stop_reason": "end_turn"}); err != nil {
			t.Fatalf("end turn %s: %v", record.turnID, err)
		}
	}

	// 1. First dispatch starts immediately.
	reply := dispatch(map[string]any{"chat_id": "chat-q", "message": "first", "role_ids": []string{"role-q"}})
	if len(reply.Started) != 1 || len(reply.Queued) != 0 {
		t.Fatalf("first dispatch should start immediately: %+v", reply)
	}
	firstDispatchID := reply.ID
	first := nextPrompt()
	if !strings.Contains(first.text, "first") {
		t.Fatalf("first prompt text %q should contain the message", first.text)
	}

	// 2. Second dispatch to the busy role is queued, not started.
	reply = dispatch(map[string]any{"chat_id": "chat-q", "message": "second", "role_ids": []string{"role-q"}})
	if len(reply.Started) != 0 || len(reply.Queued) != 1 || reply.Queued[0] != "role-q" {
		t.Fatalf("second dispatch should be queued: %+v", reply)
	}
	queuedDispatchID := reply.ID
	select {
	case record := <-prompts:
		t.Fatalf("queued message started while the first turn was still running: %+v", record)
	case <-time.After(400 * time.Millisecond):
	}

	// 3. Parallel dispatch bypasses the busy lock on a throwaway session.
	reply = dispatch(map[string]any{"chat_id": "chat-q", "message": "third", "role_ids": []string{"role-q"}, "parallel_dispatch": true})
	if len(reply.Started) != 1 || len(reply.Queued) != 0 {
		t.Fatalf("parallel dispatch should start immediately: %+v", reply)
	}
	parallel := nextPrompt()
	if !strings.Contains(parallel.text, "third") {
		t.Fatalf("parallel prompt text %q should contain the message", parallel.text)
	}
	if parallel.sessionID == first.sessionID {
		t.Fatalf("parallel dispatch must use a fresh session, got %s", parallel.sessionID)
	}
	stored, err := p.store.roleSession("chat-q", "role-q")
	if err != nil || stored == nil || stored.ProviderSessionID != first.sessionID {
		t.Fatalf("parallel dispatch must not overwrite the stored session: stored=%#v err=%v", stored, err)
	}

	// 4. Ending the first turn releases the queued message onto the canonical
	// session.
	endTurn(first)
	queued := nextPrompt()
	if !strings.Contains(queued.text, "second") {
		t.Fatalf("queued prompt text %q should contain the queued message", queued.text)
	}
	if queued.sessionID != first.sessionID {
		t.Fatalf("queued turn should reuse the canonical session %s, got %s", first.sessionID, queued.sessionID)
	}
	endTurn(queued)
	endTurn(parallel)

	// 5. Everything drains: no busy keys, no queues, only the canonical
	// runtime remains (the parallel throwaway entry is cleaned up).
	deadline := time.Now().Add(3 * time.Second)
	for {
		p.mu.Lock()
		busyCount, queueCount, runtimeCount := len(p.busy), len(p.queues), len(p.runtimes)
		p.mu.Unlock()
		if busyCount == 0 && queueCount == 0 && runtimeCount == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state did not drain: busy=%d queues=%d runtimes=%d", busyCount, queueCount, runtimeCount)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 6. Every turn persisted its execution target (agent/provider/model)
	// and links back to its dispatch — the queued turn proves the dispatch
	// id threads through the busy queue.
	assertTarget := func(turnID, dispatchID string) {
		t.Helper()
		turn, err := p.store.turn(turnID)
		if err != nil || turn == nil {
			t.Fatalf("turn %s missing: %v", turnID, err)
		}
		if turn.Agent != "hermes" || turn.Provider != "default" || turn.Model != "m" {
			t.Fatalf("turn %s target = %q/%q/%q, want hermes/default/m", turnID, turn.Agent, turn.Provider, turn.Model)
		}
		if turn.DispatchID != dispatchID {
			t.Fatalf("turn %s dispatch = %q, want %q", turnID, turn.DispatchID, dispatchID)
		}
	}
	assertTarget(first.turnID, firstDispatchID)
	assertTarget(queued.turnID, queuedDispatchID)
	assertTarget(parallel.turnID, reply.ID)

	// 7. chats:list exposes the per-turn targets for the pane's labels.
	value, err := caller.Request(ctx, "chat:_:chats:list", map[string]any{"chat_id": "chat-q"}, 10*time.Second)
	if err != nil {
		t.Fatalf("chats:list: %v", err)
	}
	var list struct {
		TurnTargets map[string]struct {
			DispatchID string `json:"dispatch_id"`
			RoleName   string `json:"role_name"`
			Agent      string `json:"agent"`
			Provider   string `json:"provider"`
			Model      string `json:"model"`
		} `json:"turn_targets"`
	}
	raw, _ := json.Marshal(value)
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode chats:list reply: %v", err)
	}
	target, ok := list.TurnTargets[first.turnID]
	if !ok {
		t.Fatalf("chats:list turn_targets missing turn %s: %v", first.turnID, list.TurnTargets)
	}
	if target.Agent != "hermes" || target.Provider != "default" || target.Model != "m" || target.DispatchID != firstDispatchID || target.RoleName != "Q" {
		t.Fatalf("turn_targets[%s] = %+v, want hermes/default/m dispatch %s role Q", first.turnID, target, firstDispatchID)
	}
}

func TestRoutingPolicySelectsEnabledOnlineCandidates(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.catalogs["viewer.agent-hermes"] = agentdriver.Catalog{Agent: "hermes"}
	workspace := Workspace{RoutingPolicies: []RoutingPolicyConfig{{
		ID: "policy", Enabled: true, AutoFailover: true, MaxAttempts: 2,
		Candidates: []RoutingCandidateConfig{
			{ID: "disabled", AgentID: "hermes", ProviderID: "skip", Enabled: false},
			{ID: "offline", AgentID: "opencode", ProviderID: "default", Enabled: true},
			{ID: "first", AgentID: "hermes", ProviderID: "one", ModelID: "m1", Enabled: true, Parameters: map[string]any{"x": "opaque"}},
			{ID: "second", AgentID: "hermes", ProviderID: "two", ModelID: "m2", Enabled: true},
			{ID: "capped", AgentID: "hermes", ProviderID: "three", Enabled: true},
		},
	}}}
	resolved, err := p.resolveCandidates(Chat{}, workspace, SuperRole{RoutingPolicyID: "policy"})
	if err != nil || len(resolved) != 2 || resolved[0].target.Provider != "one" || resolved[1].target.Provider != "two" || resolved[0].target.Parameters["x"] != "opaque" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
}

func TestRoutingPolicyWithoutFailoverOrZeroMaxAttemptsSelectsOne(t *testing.T) {
	for _, policy := range []RoutingPolicyConfig{
		{ID: "disabled-failover", Enabled: true, AutoFailover: false, MaxAttempts: 9},
		{ID: "zero-attempts", Enabled: true, AutoFailover: true, MaxAttempts: 0},
	} {
		t.Run(policy.ID, func(t *testing.T) {
			p, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			p.catalogs["viewer.agent-hermes"] = agentdriver.Catalog{Agent: "hermes"}
			policy.Candidates = []RoutingCandidateConfig{
				{ID: "first", AgentID: "hermes", ProviderID: "one", Enabled: true},
				{ID: "second", AgentID: "hermes", ProviderID: "two", Enabled: true},
			}
			resolved, err := p.resolveCandidates(Chat{}, Workspace{RoutingPolicies: []RoutingPolicyConfig{policy}}, SuperRole{RoutingPolicyID: policy.ID})
			if err != nil || len(resolved) != 1 || resolved[0].target.Provider != "one" {
				t.Fatalf("resolved=%#v err=%v", resolved, err)
			}
		})
	}
}

func TestRunningChatIDs(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if ids := p.runningChatIDs(); len(ids) != 0 {
		t.Fatalf("expected no running chats, got %v", ids)
	}
	p.runtimes[runtimeKey("chat-b", "role-1")] = &runtime{sessionID: "s1", activeTurn: "turn-1"}
	p.runtimes[runtimeKey("chat-a", "role-1")] = &runtime{sessionID: "s2", activeTurn: "turn-2"}
	// Second concurrent turn in chat-b must not duplicate the id; an idle
	// runtime (no active turn) must not appear at all.
	p.runtimes[runtimeKey("chat-b", "role-2")] = &runtime{sessionID: "s3", activeTurn: "turn-3"}
	p.runtimes[runtimeKey("chat-c", "role-1")] = &runtime{sessionID: "s4"}
	ids := p.runningChatIDs()
	if len(ids) != 2 || ids[0] != "chat-a" || ids[1] != "chat-b" {
		t.Fatalf("ids=%v", ids)
	}
}

func TestRunningTurnsAndTurnTargetedStop(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if turns := p.runningTurns("chat-a"); len(turns) != 0 {
		t.Fatalf("expected no running turns, got %v", turns)
	}
	// Canonical runtime plus a parallel send-now runtime (key carries the
	// turn id) for the same role: both must surface individually; the other
	// chat and idle runtimes must not leak in.
	p.runtimes[runtimeKey("chat-a", "role-1")] = &runtime{sessionID: "s1", activeTurn: "turn-1", roleID: "role-1", roleName: "Role"}
	p.runtimes[runtimeKey("chat-a", "role-1")+"\x00turn-2"] = &runtime{sessionID: "s2", activeTurn: "turn-2", roleID: "role-1", roleName: "Role"}
	p.runtimes[runtimeKey("chat-b", "role-1")] = &runtime{sessionID: "s3", activeTurn: "turn-3", roleID: "role-1"}
	p.runtimes[runtimeKey("chat-a", "role-2")] = &runtime{sessionID: "s4"}
	turns := p.runningTurns("chat-a")
	if len(turns) != 2 || turns[0]["turn_id"] != "turn-1" || turns[1]["turn_id"] != "turn-2" || turns[0]["role_id"] != "role-1" {
		t.Fatalf("turns=%v", turns)
	}
	// A turn-targeted stop cancels exactly that turn; the parallel sibling
	// keeps running untouched.
	if stopped, err := p.stopTurn("chat-a", "", "turn-2"); !stopped || err != nil {
		t.Fatalf("stopped=%v err=%v", stopped, err)
	}
	if !p.runtimes[runtimeKey("chat-a", "role-1")+"\x00turn-2"].cancelRequested {
		t.Fatal("turn-2 should be cancel-requested")
	}
	if p.runtimes[runtimeKey("chat-a", "role-1")].cancelRequested {
		t.Fatal("turn-1 must not be touched by a turn-2 stop")
	}
	if stopped, err := p.stopTurn("chat-a", "", "turn-9"); stopped || err != nil {
		t.Fatalf("unknown turn: stopped=%v err=%v", stopped, err)
	}
}

func TestHandleUpdatePersistsRawBeforeVisibleTextFilter(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		delete(p.runtimes, runtimeKey("chat", "role"))
		_ = p.Close()
	}()
	p.runtimes[runtimeKey("chat", "role")] = &runtime{sessionID: "session", activeTurn: "turn", roleID: "role", roleName: "Role", pluginID: "viewer.agent-hermes", providerKey: "hermes/default"}
	raw := json.RawMessage(`{"sessionId":"session","update":{"sessionUpdate":"tool_call","title":"Read","status":"pending"}}`)
	p.handleAgentEvent(busclient.Frame{Channel: "viewer.agent-hermes:_:event", Value: agentdriver.EventFrame{SessionID: "session", TurnID: "turn", Kind: "tool_call", RawJSON: string(raw), Block: agentdriver.Block{Kind: "tool_call", Text: "Read", Payload: `{"name":"Read","status":"pending"}`}}})
	var events []TurnEvent
	if err := p.store.db.Find(&events).Error; err != nil || len(events) != 1 || events[0].RawJSON != string(raw) || events[0].Seq != 0 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	var blocks []MessageBlock
	if err := p.store.db.Find(&blocks).Error; err != nil || len(blocks) != 1 || blocks[0].Kind != "tool_call" {
		t.Fatalf("blocks=%#v err=%v", blocks, err)
	}
	var count int64
	if err := p.store.db.Model(&Message{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("visible messages=%d err=%v", count, err)
	}
	var schemas []string
	if err := p.store.db.Raw("SELECT sql FROM sqlite_master WHERE type = 'table' AND name IN ('turn_events', 'message_blocks') ORDER BY name").Scan(&schemas).Error; err != nil || len(schemas) != 2 {
		t.Fatalf("schemas=%#v err=%v", schemas, err)
	}
	for _, schema := range schemas {
		t.Log(schema)
	}
}

func TestToolCallUpdatesMergeByCallID(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		delete(p.runtimes, runtimeKey("chat", "role"))
		_ = p.Close()
	}()
	p.runtimes[runtimeKey("chat", "role")] = &runtime{sessionID: "session", activeTurn: "turn", roleID: "role", roleName: "Role", pluginID: "viewer.agent-hermes", providerKey: "hermes/default"}
	send := func(kind string, block agentdriver.Block) {
		p.handleAgentEvent(busclient.Frame{Channel: "viewer.agent-hermes:_:event", Value: agentdriver.EventFrame{SessionID: "session", TurnID: "turn", Kind: kind, Block: block}})
	}
	send("tool_call", agentdriver.Block{Kind: "tool_call", Text: "Read", Payload: `{"name":"Read","status":"pending","tool_call_id":"call-1"}`})
	send("tool_call_update", agentdriver.Block{Kind: "tool_call", Payload: `{"status":"in_progress","tool_call_id":"call-1"}`})
	send("tool_call_update", agentdriver.Block{Kind: "tool_call", Payload: `{"status":"completed","tool_call_id":"call-1"}`})
	send("tool_call", agentdriver.Block{Kind: "tool_call", Text: "Write", Payload: `{"name":"Write","status":"pending","tool_call_id":"call-2"}`})
	send("tool_call", agentdriver.Block{Kind: "tool_call", Text: "NoID", Payload: `{"name":"NoID","status":"pending"}`})
	send("tool_call", agentdriver.Block{Kind: "tool_call", Text: "NoID", Payload: `{"name":"NoID","status":"completed"}`})

	var blocks []MessageBlock
	if err := p.store.db.Find(&blocks).Error; err != nil {
		t.Fatal(err)
	}
	// call-1 collapses to one block; call-2, and the two id-less calls, stay separate.
	if len(blocks) != 4 {
		t.Fatalf("blocks=%#v", blocks)
	}
	var merged *MessageBlock
	for index := range blocks {
		if payloadString(blocks[index].Payload, "tool_call_id") == "call-1" {
			merged = &blocks[index]
		}
	}
	if merged == nil || merged.Text != "Read" {
		t.Fatalf("merged=%#v", merged)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(merged.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["name"] != "Read" || payload["status"] != "completed" || payload["tool_call_id"] != "call-1" {
		t.Fatalf("merged payload=%v", payload)
	}
	// turn-ended drops the open-call index so a later turn can't merge into it.
	p.handleAgentTurnEnded(busclient.Frame{Channel: "viewer.agent-hermes:_:turn-ended", Value: agentdriver.TurnEndedFrame{SessionID: "session", TurnID: "turn", StopReason: "end_turn"}})
	if len(p.openToolCalls) != 0 {
		t.Fatalf("openToolCalls=%v", p.openToolCalls)
	}
}

func TestCommandOutputDeltasAggregateByActivityID(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.runtimes[runtimeKey("chat", "role")] = &runtime{sessionID: "session", activeTurn: "turn", roleID: "role", roleName: "Role", pluginID: "viewer.agent-codex", providerKey: "codex/default"}
	send := func(block agentdriver.Block) {
		p.handleAgentEvent(busclient.Frame{Channel: "viewer.agent-codex:_:event", Value: agentdriver.EventFrame{SessionID: "session", TurnID: "turn", Kind: "command", Block: block}})
	}
	send(agentdriver.Block{Kind: "command", Text: "go test ./...", Payload: `{"activity_id":"exec-1","command":"go test ./...","status":"inProgress"}`})
	send(agentdriver.Block{Kind: "command", Payload: `{"activity_id":"exec-1","output":"first\n"}`})
	send(agentdriver.Block{Kind: "command", Payload: `{"activity_id":"exec-1","output":"second\n"}`})
	// The completed item supplies authoritative aggregate output; it replaces
	// the accumulated deltas instead of duplicating them.
	send(agentdriver.Block{Kind: "command", Text: "go test ./...", Payload: `{"activity_id":"exec-1","output":"first\nsecond\n","output_complete":true,"status":"completed"}`})

	var blocks []MessageBlock
	if err := p.store.db.Find(&blocks).Error; err != nil || len(blocks) != 1 {
		t.Fatalf("blocks=%#v err=%v", blocks, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(blocks[0].Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if blocks[0].Text != "go test ./..." || payload["output"] != "first\nsecond\n" || payload["status"] != "completed" {
		t.Fatalf("block=%#v payload=%v", blocks[0], payload)
	}
}

func TestAgentTextDeltasAggregatePerSegment(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		delete(p.runtimes, runtimeKey("chat", "role"))
		_ = p.Close()
	}()
	p.runtimes[runtimeKey("chat", "role")] = &runtime{sessionID: "session", activeTurn: "turn", roleID: "role", roleName: "Role", pluginID: "viewer.agent-hermes", providerKey: "hermes/default"}
	send := func(kind, text string) {
		p.handleAgentEvent(busclient.Frame{Channel: "viewer.agent-hermes:_:event", Value: agentdriver.EventFrame{SessionID: "session", TurnID: "turn", Kind: kind, Block: agentdriver.Block{Kind: kind, Text: text}}})
	}
	send("agent_text", "Hello")
	send("agent_text", " world")
	messages, err := p.store.turnMessages("turn")
	if err != nil || len(messages) != 1 || messages[0].Text != "Hello world" {
		t.Fatalf("after deltas messages=%#v err=%v", messages, err)
	}
	firstID, firstCreated := messages[0].ID, messages[0].CreatedAt
	send("tool_call", "Read") // seals the current text segment
	send("agent_text", "next")
	messages, err = p.store.turnMessages("turn")
	if err != nil || len(messages) != 2 || messages[1].Text != "next" || messages[1].ID == firstID {
		t.Fatalf("after seal messages=%#v err=%v", messages, err)
	}
	if messages[0].Text != "Hello world" || messages[0].CreatedAt != firstCreated {
		t.Fatalf("first segment mutated: %#v", messages[0])
	}
	p.handleAgentTurnEnded(busclient.Frame{Channel: "viewer.agent-hermes:_:turn-ended", Value: agentdriver.TurnEndedFrame{TurnID: "turn", StopReason: "end_turn"}})
	send("agent_text", "later") // after turn end a delta starts a fresh row
	messages, err = p.store.turnMessages("turn")
	if err != nil || len(messages) != 3 || messages[2].Text != "later" {
		t.Fatalf("after turn end messages=%#v err=%v", messages, err)
	}
}

func TestStreamingBlocksAggregatePerSegment(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		delete(p.runtimes, runtimeKey("chat", "role"))
		_ = p.Close()
	}()
	p.runtimes[runtimeKey("chat", "role")] = &runtime{sessionID: "session", activeTurn: "turn", roleID: "role", roleName: "Role", pluginID: "viewer.agent-hermes", providerKey: "hermes/default"}
	send := func(kind, text string) {
		p.handleAgentEvent(busclient.Frame{Channel: "viewer.agent-hermes:_:event", Value: agentdriver.EventFrame{SessionID: "session", TurnID: "turn", Kind: kind, Block: agentdriver.Block{Kind: kind, Text: text}}})
	}
	blocks := func() []MessageBlock {
		var rows []MessageBlock
		if err := p.store.db.Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		return rows
	}
	byText := func(rows []MessageBlock, text string) *MessageBlock {
		for index := range rows {
			if rows[index].Text == text {
				return &rows[index]
			}
		}
		return nil
	}
	send("thinking", "Let")
	send("thinking", " me")
	send("thinking", " think")
	rows := blocks()
	if len(rows) != 1 || rows[0].Kind != "thinking" || rows[0].Text != "Let me think" {
		t.Fatalf("after deltas blocks=%#v", rows)
	}
	firstID := rows[0].ID
	send("agent_text", "answer") // different kind seals the thinking segment
	send("thinking", "more")     // opens a new thinking block, does not append to the first
	rows = blocks()
	if len(rows) != 3 {
		t.Fatalf("after seal blocks=%#v", rows)
	}
	first, more := byText(rows, "Let me think"), byText(rows, "more")
	if first == nil || first.ID != firstID || more == nil || more.ID == firstID || byText(rows, "answer") == nil {
		t.Fatalf("after seal blocks=%#v firstID=%s", rows, firstID)
	}
	p.handleAgentTurnEnded(busclient.Frame{Channel: "viewer.agent-hermes:_:turn-ended", Value: agentdriver.TurnEndedFrame{TurnID: "turn", StopReason: "end_turn"}})
	send("thinking", "later") // after turn end a delta starts a fresh block
	rows = blocks()
	if len(rows) != 4 || byText(rows, "later") == nil || byText(rows, "morelater") != nil {
		t.Fatalf("after turn end blocks=%#v", rows)
	}
}

func TestChatMessageBlocksOrderingAndPayload(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	// Insert out of chronological order; listing must sort by occurred_at, id.
	later := &MessageBlock{ID: "b2", EventID: "e2", ChatID: "chat", TurnID: "turn", Kind: "tool_result", Text: "ok", Payload: `{"status":"completed"}`, OccurredAt: 2000}
	earlier := &MessageBlock{ID: "b1", EventID: "e1", ChatID: "chat", TurnID: "turn", Kind: "tool_call", Text: "Read", Payload: `{"name":"Read"}`, OccurredAt: 1000}
	other := &MessageBlock{ID: "b3", EventID: "e3", ChatID: "other-chat", TurnID: "turn", Kind: "thinking", Payload: "{}", OccurredAt: 500}
	for _, block := range []*MessageBlock{later, other, earlier} {
		if err := p.store.addMessageBlock(block); err != nil {
			t.Fatal(err)
		}
	}
	blocks, err := p.store.chatMessageBlocks("chat", 0, 0)
	if err != nil || len(blocks) != 2 || blocks[0].ID != "b1" || blocks[1].ID != "b2" {
		t.Fatalf("blocks=%#v err=%v", blocks, err)
	}
	payload := blocks[0].payload()
	if payload["kind"] != "tool_call" || payload["turn_id"] != "turn" || payload["occurred_at"] != int64(1000) || payload["payload"] != `{"name":"Read"}` {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestTurnFailureSurfacedAsErrorBlock(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	attempts := []map[string]any{
		{"agent": "hermes", "provider": "custom", "model": "k3", "outcome": "turn_error", "error": "boom"},
		{"agent": "codex-app-server", "provider": "openai", "model": "gpt", "outcome": "start_error", "error": "dial failed"},
	}
	p.emitTurnFailure("chat", "turn", SuperRole{ID: "role", Name: "Role"}, "error", attempts, nil, "boom")
	blocks, err := p.store.chatMessageBlocks("chat", 0, 0)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("blocks=%#v err=%v", blocks, err)
	}
	block := blocks[0]
	if block.Kind != "error" || block.TurnID != "turn" {
		t.Fatalf("block=%#v", block)
	}
	for _, want := range []string{"Turn failed", "hermes / custom / k3: boom (turn_error)", "dial failed"} {
		if !strings.Contains(block.Text, want) {
			t.Fatalf("text %q missing %q", block.Text, want)
		}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(block.Payload), &payload); err != nil || payload["stop_reason"] != "error" {
		t.Fatalf("payload=%q err=%v", block.Payload, err)
	}
}

func TestTurnFailureTextNonErrorReason(t *testing.T) {
	text := turnFailureText("refusal", nil, nil, "")
	if text != "Turn ended: refusal" {
		t.Fatalf("text=%q", text)
	}
	text = turnFailureText("error", nil, errors.New("no dispatchable chat roles have descriptions"), "")
	if !strings.Contains(text, "Turn failed") || !strings.Contains(text, "no dispatchable") {
		t.Fatalf("text=%q", text)
	}
}

func TestHermesRefusalFreshRetryGuard(t *testing.T) {
	tests := []struct {
		name           string
		agent          string
		fresh          bool
		reason         string
		hadEvents      bool
		alreadyRetried bool
		want           bool
	}{
		{name: "stale hermes session", agent: "hermes", reason: "refusal", want: true},
		{name: "fresh session", agent: "hermes", fresh: true, reason: "refusal"},
		{name: "visible refusal", agent: "hermes", reason: "refusal", hadEvents: true},
		{name: "one retry only", agent: "hermes", reason: "refusal", alreadyRetried: true},
		{name: "other agent", agent: "opencode", reason: "refusal"},
		{name: "other reason", agent: "hermes", reason: "error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRetryFreshHermesSession(tt.agent, tt.fresh, tt.reason, tt.hadEvents, tt.alreadyRetried); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTurnEndReportsWhetherAgentEmittedEvents(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	current := &runtime{sessionID: "session", activeTurn: "turn-1", roleID: "role", roleName: "Role", pluginID: "viewer.agent-hermes", providerKey: "hermes/default", ended: make(chan turnEnd, 1)}
	p.runtimes[runtimeKey("chat", "role")] = current

	p.handleAgentTurnEnded(busclient.Frame{Channel: "viewer.agent-hermes:_:turn-ended", Value: agentdriver.TurnEndedFrame{TurnID: "turn-1", StopReason: "refusal"}})
	if end := <-current.ended; end.hadEvents {
		t.Fatal("turn without events reported hadEvents=true")
	}

	current.activeTurn, current.sawEvent = "turn-2", false
	p.handleAgentEvent(busclient.Frame{Channel: "viewer.agent-hermes:_:event", Value: agentdriver.EventFrame{SessionID: "session", TurnID: "turn-2", Kind: "agent_text", Block: agentdriver.Block{Kind: "agent_text", Text: "visible"}}})
	p.handleAgentTurnEnded(busclient.Frame{Channel: "viewer.agent-hermes:_:turn-ended", Value: agentdriver.TurnEndedFrame{TurnID: "turn-2", StopReason: "refusal"}})
	if end := <-current.ended; !end.hadEvents {
		t.Fatal("turn with an event reported hadEvents=false")
	}
}

func TestChatMessageBlocksWindow(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	for _, block := range []*MessageBlock{
		{ID: "b1", EventID: "e1", ChatID: "chat", TurnID: "turn", Kind: "thinking", Payload: "{}", OccurredAt: 100},
		{ID: "b2", EventID: "e2", ChatID: "chat", TurnID: "turn", Kind: "tool_call", Payload: "{}", OccurredAt: 1000},
		{ID: "b3", EventID: "e3", ChatID: "chat", TurnID: "turn", Kind: "tool_result", Payload: "{}", OccurredAt: 1000},
		{ID: "b4", EventID: "e4", ChatID: "chat", TurnID: "turn", Kind: "thinking", Payload: "{}", OccurredAt: 1999},
		{ID: "b5", EventID: "e5", ChatID: "chat", TurnID: "turn", Kind: "thinking", Payload: "{}", OccurredAt: 2000},
		{ID: "b6", EventID: "e6", ChatID: "chat", TurnID: "turn", Kind: "thinking", Payload: "{}", OccurredAt: 9999},
	} {
		if err := p.store.addMessageBlock(block); err != nil {
			t.Fatal(err)
		}
	}
	// Window [1000, 2000): lower inclusive, upper exclusive — tiles without
	// gaps or duplicates when the timeline fetches blocks per loaded span.
	blocks, err := p.store.chatMessageBlocks("chat", 1000, 2000)
	if err != nil || len(blocks) != 3 || blocks[0].ID != "b2" || blocks[1].ID != "b3" || blocks[2].ID != "b4" {
		t.Fatalf("window blocks=%#v err=%v", blocks, err)
	}
	// Unbounded still returns everything in display order.
	blocks, err = p.store.chatMessageBlocks("chat", 0, 0)
	if err != nil || len(blocks) != 6 {
		t.Fatalf("full blocks=%#v err=%v", blocks, err)
	}
}

func TestBudgetBlockPayloadsTruncatesAndResumes(t *testing.T) {
	// Fill past the reply budget: 9 blocks x 100KB text (estimated 2x +
	// envelope) exceeds the 700KB budget partway through.
	blocks := make([]MessageBlock, 0, 9)
	for i := 0; i < 9; i++ {
		blocks = append(blocks, MessageBlock{
			ID: "b" + string(rune('a'+i)), ChatID: "chat", TurnID: "turn",
			Kind: "tool_call", Text: string(make([]byte, 100*1024)), Payload: "{}",
			OccurredAt: int64(1000 + i),
		})
	}
	// The budget must actually engage on this fixture.
	first, firstTruncated, firstNext := budgetBlockPayloads(blocks, map[string]Turn{})
	if !firstTruncated || len(first) == 0 || len(first) >= len(blocks) {
		t.Fatalf("first page: values=%d truncated=%v", len(first), firstTruncated)
	}
	if firstNext != blocks[len(first)].OccurredAt {
		t.Fatalf("nextAfter=%d want %d", firstNext, blocks[len(first)].OccurredAt)
	}
	// Paging forward with after=next_after eventually covers every block
	// (the client refetches the cut boundary block and dedups by id).
	covered := map[string]bool{}
	page := blocks
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("paging did not converge")
		}
		values, truncated, nextAfter := budgetBlockPayloads(page, map[string]Turn{})
		if len(values) == 0 {
			t.Fatal("empty page")
		}
		for _, value := range values {
			covered[value["id"].(string)] = true
		}
		if !truncated {
			break
		}
		var cut int
		for cut < len(page) && page[cut].OccurredAt < nextAfter {
			cut++
		}
		page = page[cut:]
	}
	if len(covered) != len(blocks) {
		t.Fatalf("pages cover %d of %d blocks", len(covered), len(blocks))
	}
	// Small sets pass through untruncated; a single oversize block is still
	// emitted so the cursor advances.
	all, truncated, _ := budgetBlockPayloads(blocks[:2], map[string]Turn{})
	if truncated || len(all) != 2 {
		t.Fatalf("small set: values=%d truncated=%v", len(all), truncated)
	}
	huge := []MessageBlock{{ID: "big", Text: string(make([]byte, blocksReplyBudget)), Payload: "{}", OccurredAt: 5}}
	one, truncated, _ := budgetBlockPayloads(append(huge, blocks[0]), map[string]Turn{})
	if len(one) != 1 || !truncated {
		t.Fatalf("oversize single block: values=%d truncated=%v", len(one), truncated)
	}
}

func TestHistoryPagePagination(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	// Seed 8 messages; m5/m6 share created_at so the composite cursor
	// (created_at, id) is exercised at the page boundary.
	seeds := []*Message{
		{ID: "m1", ChatID: "chat", TurnID: "t1", Role: "user", Text: "1", CreatedAt: 1000},
		{ID: "m2", ChatID: "chat", TurnID: "t2", Role: "assistant", Text: "2", CreatedAt: 2000},
		{ID: "m3", ChatID: "chat", TurnID: "t3", Role: "user", Text: "3", CreatedAt: 3000},
		{ID: "m4", ChatID: "chat", TurnID: "t4", Role: "assistant", Text: "4", CreatedAt: 4000},
		{ID: "m5", ChatID: "chat", TurnID: "t5", Role: "user", Text: "5", CreatedAt: 5000},
		{ID: "m6", ChatID: "chat", TurnID: "t6", Role: "assistant", Text: "6", CreatedAt: 5000},
		{ID: "m7", ChatID: "chat", TurnID: "t7", Role: "user", Text: "7", CreatedAt: 6000},
		{ID: "m8", ChatID: "chat", TurnID: "t8", Role: "assistant", Text: "8", CreatedAt: 7000},
	}
	for _, message := range seeds {
		if err := p.store.addMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	// Walk pages newest-first with a page size of 3, following the same
	// composite cursor the RPC layer passes through.
	var seen []string
	var cursorTs int64
	var cursorID string
	hasMore := true
	pages := 0
	for hasMore && pages < 10 {
		page, more, err := p.store.historyPage("chat", cursorTs, cursorID, 3)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		// Pages arrive ascending; within a page ids must be ascending too.
		for i, message := range page {
			seen = append(seen, message.ID)
			if i > 0 && (page[i-1].CreatedAt > message.CreatedAt || (page[i-1].CreatedAt == message.CreatedAt && page[i-1].ID > message.ID)) {
				t.Fatalf("page %d not ascending: %#v", pages, page)
			}
		}
		if len(page) == 0 {
			t.Fatalf("empty page with hasMore=true")
		}
		cursorTs, cursorID = page[0].CreatedAt, page[0].ID
		hasMore = more
	}
	// Page 1 (newest 3) = [m6 m7 m8], page 2 = [m3 m4 m5], page 3 = [m1 m2]:
	// consecutive ascending slices of the global order, no gaps, no dupes.
	want := []string{"m6", "m7", "m8", "m3", "m4", "m5", "m1", "m2"}
	if len(seen) != len(want) {
		t.Fatalf("pages=%d seen=%v want=%v", pages, seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("seen=%v want=%v", seen, want)
		}
	}
	if hasMore {
		t.Fatal("expected no more pages after exhausting")
	}
	// A cursor with no id falls back to timestamp-only semantics.
	page, more, err := p.store.historyPage("chat", 4000, "", 3)
	if err != nil || more || len(page) != 3 || page[0].ID != "m1" || page[2].ID != "m3" {
		t.Fatalf("timestamp-only page=%#v more=%v err=%v", page, more, err)
	}
}

func TestHistoryPageAfterCursor(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	// Same seed shape as TestHistoryPagePagination: m5/m6 share created_at so
	// the composite (created_at, id) boundary is exercised.
	seeds := []*Message{
		{ID: "m1", ChatID: "chat", TurnID: "t1", Role: "user", Text: "1", CreatedAt: 1000},
		{ID: "m2", ChatID: "chat", TurnID: "t2", Role: "assistant", Text: "2", CreatedAt: 2000},
		{ID: "m3", ChatID: "chat", TurnID: "t3", Role: "user", Text: "3", CreatedAt: 3000},
		{ID: "m4", ChatID: "chat", TurnID: "t4", Role: "assistant", Text: "4", CreatedAt: 4000},
		{ID: "m5", ChatID: "chat", TurnID: "t5", Role: "user", Text: "5", CreatedAt: 5000},
		{ID: "m6", ChatID: "chat", TurnID: "t6", Role: "assistant", Text: "6", CreatedAt: 5000},
		{ID: "m7", ChatID: "chat", TurnID: "t7", Role: "user", Text: "7", CreatedAt: 6000},
		{ID: "m8", ChatID: "chat", TurnID: "t8", Role: "assistant", Text: "8", CreatedAt: 7000},
		{ID: "other", ChatID: "other-chat", TurnID: "t9", Role: "user", Text: "x", CreatedAt: 9000},
	}
	for _, message := range seeds {
		if err := p.store.addMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(values []Message) []string {
		out := make([]string, 0, len(values))
		for _, value := range values {
			out = append(out, value.ID)
		}
		return out
	}
	// Composite inclusive cursor: the boundary row itself is re-fetched so a
	// reconnecting client can replace its possibly-still-streaming copy.
	page, more, err := p.store.historyPageAfter("chat", 5000, "m5", 50)
	if err != nil || more || strings.Join(ids(page), ",") != "m5,m6,m7,m8" {
		t.Fatalf("inclusive page=%v more=%v err=%v", ids(page), more, err)
	}
	// Same created_at, newer id: the older row stays outside the boundary.
	page, more, err = p.store.historyPageAfter("chat", 5000, "m6", 50)
	if err != nil || more || strings.Join(ids(page), ",") != "m6,m7,m8" {
		t.Fatalf("tie page=%v more=%v err=%v", ids(page), more, err)
	}
	// Newest row alone, then nothing past the end.
	page, more, err = p.store.historyPageAfter("chat", 7000, "m8", 50)
	if err != nil || more || strings.Join(ids(page), ",") != "m8" {
		t.Fatalf("top page=%v more=%v err=%v", ids(page), more, err)
	}
	page, more, err = p.store.historyPageAfter("chat", 8000, "", 50)
	if err != nil || more || len(page) != 0 {
		t.Fatalf("empty page=%v more=%v err=%v", ids(page), more, err)
	}
	// A cursor with no id falls back to timestamp-only semantics.
	page, more, err = p.store.historyPageAfter("chat", 5000, "", 50)
	if err != nil || more || strings.Join(ids(page), ",") != "m5,m6,m7,m8" {
		t.Fatalf("timestamp-only page=%v more=%v err=%v", ids(page), more, err)
	}
	// Other chats are never returned.
	for _, value := range page {
		if value.ChatID != "chat" {
			t.Fatalf("leaked message from other chat: %#v", value)
		}
	}
	// Limit boundary: walk deltas following the client merge loop (dedupe by
	// id); pages arrive ascending, the boundary row repeats, and the walk
	// exhausts without gaps or stalls.
	var seen []string
	cursorTs, cursorID := int64(5000), "m5"
	for pages := 0; pages < 10; pages++ {
		page, more, err = p.store.historyPageAfter("chat", cursorTs, cursorID, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		seen = append(seen, ids(page)...)
		cursorTs, cursorID = page[len(page)-1].CreatedAt, page[len(page)-1].ID
		if !more {
			break
		}
	}
	unique := map[string]bool{}
	for _, id := range seen {
		unique[id] = true
	}
	want := map[string]bool{"m5": true, "m6": true, "m7": true, "m8": true}
	if len(unique) != len(want) {
		t.Fatalf("delta walk unique=%v want=%v", unique, want)
	}
	for id := range want {
		if !unique[id] {
			t.Fatalf("delta walk missing %s (seen=%v)", id, seen)
		}
	}
}

// Routing resolution is layered: a chat-level per-role override beats the
// role's own routing policy, which beats the workspace default policy.
func TestResolveCandidatesLayeredOverride(t *testing.T) {
	candidate := func(id, provider string) RoutingCandidateConfig {
		return RoutingCandidateConfig{ID: id, AgentID: "opencode", ProviderID: provider, ModelID: "m", Enabled: true}
	}
	workspace := Workspace{
		DefaultRoutingPolicyID: "policy-default",
		RoutingPolicies: []RoutingPolicyConfig{
			{ID: "policy-default", Enabled: true, Candidates: []RoutingCandidateConfig{candidate("c-default", "p-default")}},
			{ID: "policy-role", Enabled: true, Candidates: []RoutingCandidateConfig{candidate("c-role", "p-role")}},
			{ID: "policy-chat", Enabled: true, Candidates: []RoutingCandidateConfig{candidate("c-chat", "p-chat")}},
		},
	}
	plugin := &Plugin{
		agents:   map[string]string{"opencode": "viewer.agent-opencode"},
		catalogs: map[string]agentdriver.Catalog{"viewer.agent-opencode": {}},
	}
	providerOf := func(chat Chat, role SuperRole) string {
		resolved, err := plugin.resolveCandidates(chat, workspace, role)
		if err != nil {
			t.Fatalf("resolveCandidates: %v", err)
		}
		if len(resolved) != 1 {
			t.Fatalf("resolved=%d candidates, want 1", len(resolved))
		}
		return resolved[0].target.Provider
	}

	plainChat := Chat{ID: "chat", RoleRoutingOverridesJSON: "{}"}
	roleWithPolicy := SuperRole{ID: "role", RoutingPolicyID: "policy-role"}
	roleNoPolicy := SuperRole{ID: "role"}

	if got := providerOf(plainChat, roleWithPolicy); got != "p-role" {
		t.Fatalf("role default layer provider=%q, want p-role", got)
	}
	if got := providerOf(plainChat, roleNoPolicy); got != "p-default" {
		t.Fatalf("workspace default layer provider=%q, want p-default", got)
	}
	overrideChat := Chat{ID: "chat", RoleRoutingOverridesJSON: encodeJSON(map[string]string{"role": "policy-chat"})}
	if got := providerOf(overrideChat, roleWithPolicy); got != "p-chat" {
		t.Fatalf("chat override layer provider=%q, want p-chat", got)
	}
	if got := providerOf(overrideChat, roleNoPolicy); got != "p-chat" {
		t.Fatalf("chat override over workspace default provider=%q, want p-chat", got)
	}
}
