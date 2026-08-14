package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"viewer/internal/agentdriver"
	"viewer/internal/busclient"
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

func TestProviderValidationAndMessageSender(t *testing.T) {
	codex := SuperRole{Name: "Codex", Provider: "codex-app-server"}
	if err := normalizeRole(&codex, true); err != nil {
		t.Fatalf("codex-app-server should be accepted: %v", err)
	}
	role := SuperRole{Name: "X", Provider: "codex"}
	if !errors.Is(normalizeRole(&role, true), errProviderM6c) {
		t.Fatal("expected M6c provider error")
	}
	message := Message{ID: "m", ChatID: "c", TurnID: "t", Role: "assistant", Text: "x", SenderFrom: "role", RoleID: "r", RoleName: "R"}
	sender := message.payload()["sender"].(map[string]any)
	if sender["role_id"] != "r" || sender["role_name"] != "R" || sender["from"] != "role" {
		t.Fatal(sender)
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
	p.handleAgentEvent(busclient.Frame{Channel: "viewer.agent-hermes:_:event", Value: agentdriver.EventFrame{SessionID: "session", Kind: "tool_call", RawJSON: string(raw), Block: agentdriver.Block{Kind: "tool_call", Text: "Read", Payload: `{"name":"Read","status":"pending"}`}}})
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
