package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"viewer/internal/acp"
)

type fakeAgent struct {
	mu                  sync.Mutex
	sessionID           string
	loadErr             error
	updates             func(acp.Update)
	prompts             []string
	cancelled           bool
	newCalls, loadCalls int
}

func newFakeAgent(id string) *fakeAgent                                 { return &fakeAgent{sessionID: id} }
func (f *fakeAgent) Initialize(context.Context) (map[string]any, error) { return map[string]any{}, nil }
func (f *fakeAgent) NewSession(context.Context, string) (string, error) {
	f.newCalls++
	return f.sessionID, nil
}
func (f *fakeAgent) LoadSession(context.Context, string, string) error {
	f.loadCalls++
	return f.loadErr
}
func (f *fakeAgent) OnUpdate(callback func(acp.Update)) { f.updates = callback }
func (f *fakeAgent) Stderr() string                     { return "" }
func (f *fakeAgent) Close() error                       { return nil }
func (f *fakeAgent) Cancel(context.Context, string) error {
	f.mu.Lock()
	f.cancelled = true
	f.mu.Unlock()
	return nil
}
func (f *fakeAgent) Prompt(_ context.Context, sessionID, text string) (string, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, text)
	f.mu.Unlock()
	if f.updates != nil {
		f.updates(acp.Update{SessionID: sessionID, Value: map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "answer"}}})
	}
	return "end_turn", nil
}

func TestChatRoleSessionLoadAndFallback(t *testing.T) {
	dir := t.TempDir()
	p, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	defer p.Close()
	chat := Chat{ID: "chat", Root: dir}
	role := SuperRole{ID: "role", Name: "Role", Provider: "hermes"}
	if err := p.store.saveRoleSession(&RoleSession{ChatID: chat.ID, RoleID: role.ID, Provider: "hermes", ProviderProfile: "test", ProviderSessionID: "old", CWD: dir}); err != nil {
		t.Fatal(err)
	}
	first := newFakeAgent("new")
	p.factory = func(context.Context) (agent, string, error) { return first, "test", nil }
	runtime, fresh, err := p.ensureRuntime(context.Background(), chat, role)
	if err != nil || !fresh || runtime.sessionID != "old" || first.loadCalls != 1 || first.newCalls != 0 {
		t.Fatalf("runtime=%#v fresh=%v err=%v load=%d new=%d", runtime, fresh, err, first.loadCalls, first.newCalls)
	}
	p.mu.Lock()
	delete(p.runtimes, runtimeKey(chat.ID, role.ID))
	p.mu.Unlock()
	first.loadErr = errors.New("gone")
	second := newFakeAgent("replacement")
	second.loadErr = errors.New("gone")
	p.factory = func(context.Context) (agent, string, error) { return second, "test", nil }
	runtime, _, err = p.ensureRuntime(context.Background(), chat, role)
	if err != nil || runtime.sessionID != "replacement" || second.newCalls != 1 {
		t.Fatalf("fallback runtime=%#v err=%v", runtime, err)
	}
}

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

func TestUpdateTextOnly(t *testing.T) {
	if got := updateText(map[string]any{"sessionUpdate": "tool_call", "text": "ignored"}); got != "" {
		t.Fatal(got)
	}
	if got := updateText(map[string]any{"session_update": "agent_message_chunk", "content": map[string]any{"text": "ok"}}); got != "ok" {
		t.Fatal(got)
	}
}
