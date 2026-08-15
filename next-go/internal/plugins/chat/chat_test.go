package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"viewer/internal/agentdriver"
	"viewer/internal/busclient"
	"viewer/internal/kernel"
	"viewer/internal/plugins/pluginrpc"
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

func TestRouterHTTPCompletion(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization=%q", got)
		}
		_ = json.NewDecoder(request.Body).Decode(&captured)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"{\"role_ids\":[\"r2\"],\"rationale\":\"best\"}"}}]}`))
	}))
	defer server.Close()
	roles := []SuperRole{{ID: "r1", Name: "One", Description: "alpha", Provider: "hermes"}, {ID: "r2", Name: "Two", Description: "beta", Provider: "hermes"}}
	ids, rationale, err := routeWithLLM(context.Background(), server.Client(), LLMConfig{Endpoint: server.URL, APIKey: "secret", Model: "router"}, "choose", roles, "history")
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
