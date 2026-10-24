package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"viewer/internal/agentdriver"
	"viewer/internal/kernel"
	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

// dispatchReply mirrors the dispatch RPC reply shape used across chat tests.
type dispatchReply struct {
	Started []string `json:"started_role_ids"`
	Queued  []string `json:"queued_role_ids"`
	ID      string   `json:"dispatch_id"`
}

// backgroundFixture boots a kernel, a config mock, and a fake agent plugin
// that declares the _:background slot; the test controls how many live
// background processes the slot reports.
type backgroundFixture struct {
	p       *Plugin
	agent   *busclient.Client
	caller  *busclient.Client
	prompts chan bgPromptRecord
	running atomic.Int32 // live background processes the mock reports
}

type bgPromptRecord struct{ sessionID, turnID, text string }

func newBackgroundFixture(t *testing.T) *backgroundFixture {
	t.Helper()
	config := kernel.DefaultConfig()
	config.Host, config.Port = "127.0.0.1", 0
	server := kernel.New(config)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	url := fmt.Sprintf("ws://127.0.0.1:%d/ws", server.Port())

	configClient := busclient.New(url, busclient.Manifest{ID: "bg-config", Version: "0.1.0", Slots: map[string]any{"config:_:get": map[string]any{}}, Emits: map[string]any{}})
	_, _ = configClient.Subscribe("config:_:get", func(frame busclient.Frame) {
		_ = pluginrpc.Respond(configClient, frame, nil)
	})
	if err := configClient.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configClient.Close() })

	f := &backgroundFixture{prompts: make(chan bgPromptRecord, 16)}
	agent := busclient.New(url, busclient.Manifest{
		ID:      "viewer.agent-hermes",
		Version: "0.3.0",
		Slots: map[string]any{
			"viewer.agent-hermes:_:start": map[string]any{}, "viewer.agent-hermes:_:prompt": map[string]any{},
			"viewer.agent-hermes:_:background": map[string]any{},
		},
		Emits: map[string]any{"viewer.agent-hermes:_:catalog": map[string]any{}, "viewer.agent-hermes:_:turn-ended": map[string]any{}},
	})
	f.agent = agent
	var mu sync.Mutex
	starts := 0
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
		record := bgPromptRecord{}
		record.sessionID, _ = value["session_id"].(string)
		record.turnID, _ = value["turn_id"].(string)
		record.text, _ = value["text"].(string)
		f.prompts <- record
		_ = pluginrpc.Respond(agent, frame, map[string]any{"ok": true})
	})
	_, _ = agent.Subscribe("viewer.agent-hermes:_:background", func(frame busclient.Frame) {
		processes := []map[string]any{}
		if f.running.Load() > 0 {
			for i := int32(0); i < f.running.Load(); i++ {
				processes = append(processes, map[string]any{"session_id": fmt.Sprintf("proc-%d", i), "command": "sleep 100", "pid": 1000 + i})
			}
		}
		_ = pluginrpc.Respond(agent, frame, map[string]any{"processes": processes})
	})
	if err := agent.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close() })
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
	t.Cleanup(func() { _ = p.Close() })
	f.p = p

	caller := busclient.New(url, busclient.Manifest{ID: "bg-caller", Version: "0.1.0", Slots: map[string]any{}, Emits: map[string]any{}})
	if err := caller.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = caller.Close() })
	f.caller = caller

	role := SuperRole{ID: "role-b", Name: "B", Description: "background test role", RoutingPolicyID: "pol-b", CreatedAt: nowMillis(), UpdatedAt: nowMillis()}
	policy := RoutingPolicyConfig{ID: "pol-b", Name: "B policy", Enabled: true, Candidates: []RoutingCandidateConfig{{ID: "cand-b", AgentID: "hermes", ProviderID: "default", ModelID: "m", Enabled: true}}}
	if err := p.store.importDomain([]SuperRole{role}, RoutingConfig{DefaultRoutingPolicyID: "pol-b", RoutingPolicies: []RoutingPolicyConfig{policy}}); err != nil {
		t.Fatal(err)
	}
	chat := Chat{ID: "chat-b", Name: "B chat", Root: t.TempDir(), MemberRoleIDsJSON: `["role-b"]`, CreatedAt: nowMillis(), UpdatedAt: nowMillis()}
	if err := p.store.saveChat(&chat); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *backgroundFixture) dispatch(t *testing.T, payload map[string]any) dispatchReply {
	t.Helper()
	payload["chat_id"] = "chat-b"
	value, err := f.caller.Request(context.Background(), "chat:_:dispatch", payload, 10*time.Second)
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

func (f *backgroundFixture) nextPrompt(t *testing.T) bgPromptRecord {
	t.Helper()
	select {
	case record := <-f.prompts:
		return record
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for agent prompt")
		return bgPromptRecord{}
	}
}

func (f *backgroundFixture) endTurn(t *testing.T, record bgPromptRecord) {
	t.Helper()
	if err := f.agent.Publish(context.Background(), "viewer.agent-hermes:_:turn-ended", map[string]any{"session_id": record.sessionID, "turn_id": record.turnID, "stop_reason": "end_turn"}); err != nil {
		t.Fatalf("end turn %s: %v", record.turnID, err)
	}
}

func (f *backgroundFixture) blockCount() int {
	f.p.mu.Lock()
	defer f.p.mu.Unlock()
	return len(f.p.background)
}

// A turn that ends with live background processes holds its line: a follow-up
// message queues, and when the last process exits the watcher injects a
// completion dispatch into the SAME session; only after that turn ends does
// the queued user message start.
func TestBackgroundHoldAndCompletion(t *testing.T) {
	defer func(old time.Duration) { backgroundPollInterval = old }(backgroundPollInterval)
	backgroundPollInterval = 20 * time.Millisecond
	f := newBackgroundFixture(t)
	f.running.Store(1)

	reply := f.dispatch(t, map[string]any{"message": "run something long", "role_ids": []string{"role-b"}})
	if len(reply.Started) != 1 {
		t.Fatalf("first dispatch should start: %+v", reply)
	}
	first := f.nextPrompt(t)
	f.endTurn(t, first)

	// The turn-end probe sees one live process: the line must stay busy.
	deadline := time.Now().Add(2 * time.Second)
	for f.blockCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.blockCount() != 1 {
		t.Fatalf("expected a background block, got %d", f.blockCount())
	}

	// A user message during the hold queues instead of starting.
	reply = f.dispatch(t, map[string]any{"message": "follow-up", "role_ids": []string{"role-b"}})
	if len(reply.Started) != 0 || len(reply.Queued) != 1 {
		t.Fatalf("dispatch during background hold should queue: %+v", reply)
	}
	select {
	case record := <-f.prompts:
		t.Fatalf("queued message started during background hold: %+v", record)
	case <-time.After(300 * time.Millisecond):
	}

	// The process exits: the watcher injects a completion dispatch into the
	// same session.
	f.running.Store(0)
	completion := f.nextPrompt(t)
	if completion.sessionID != first.sessionID {
		t.Fatalf("completion should resume session %s, got %s", first.sessionID, completion.sessionID)
	}
	if !strings.Contains(completion.text, "后台任务完成通知") || !strings.Contains(completion.text, "process_manage") {
		t.Fatalf("completion text should name the finished processes and process_manage, got %q", completion.text)
	}
	if f.blockCount() != 0 {
		t.Fatalf("block should be cleared at completion, got %d", f.blockCount())
	}

	// The completion turn ends with no new background work; the queued user
	// message now starts.
	f.endTurn(t, completion)
	queued := f.nextPrompt(t)
	if !strings.Contains(queued.text, "follow-up") {
		t.Fatalf("queued message should start after the completion turn, got %q", queued.text)
	}
	f.endTurn(t, queued)
}

// The stop button is the escape hatch: with no in-flight turn it releases
// the background block and the queued message drains.
func TestBackgroundStopReleasesHold(t *testing.T) {
	defer func(old time.Duration) { backgroundPollInterval = old }(backgroundPollInterval)
	backgroundPollInterval = 20 * time.Millisecond
	f := newBackgroundFixture(t)
	f.running.Store(1)

	f.dispatch(t, map[string]any{"message": "long", "role_ids": []string{"role-b"}})
	first := f.nextPrompt(t)
	f.endTurn(t, first)
	deadline := time.Now().Add(2 * time.Second)
	for f.blockCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.blockCount() != 1 {
		t.Fatalf("expected a background block, got %d", f.blockCount())
	}
	reply := f.dispatch(t, map[string]any{"message": "queued", "role_ids": []string{"role-b"}})
	if len(reply.Queued) != 1 {
		t.Fatalf("expected the follow-up to queue: %+v", reply)
	}

	value, err := f.caller.Request(context.Background(), "chat:_:stop", map[string]any{"chat_id": "chat-b"}, 10*time.Second)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	stopped, _ := value.(map[string]any)["stopped"].(bool)
	if !stopped {
		t.Fatalf("stop should report stopped=true for a background hold, got %v", value)
	}
	if f.blockCount() != 0 {
		t.Fatalf("stop should drop the block, got %d", f.blockCount())
	}
	queued := f.nextPrompt(t)
	if !strings.Contains(queued.text, "queued") {
		t.Fatalf("queued message should start after the stop release, got %q", queued.text)
	}
	f.endTurn(t, queued)
}

// A turn without background work releases its line immediately — the slot
// probe returns zero processes and no block is created.
func TestBackgroundNoProcessesNoHold(t *testing.T) {
	defer func(old time.Duration) { backgroundPollInterval = old }(backgroundPollInterval)
	backgroundPollInterval = 20 * time.Millisecond
	f := newBackgroundFixture(t) // running stays 0

	f.dispatch(t, map[string]any{"message": "quick", "role_ids": []string{"role-b"}})
	first := f.nextPrompt(t)
	f.endTurn(t, first)
	time.Sleep(200 * time.Millisecond)
	if f.blockCount() != 0 {
		t.Fatalf("no background processes means no block, got %d", f.blockCount())
	}
	reply := f.dispatch(t, map[string]any{"message": "next", "role_ids": []string{"role-b"}})
	if len(reply.Started) != 1 {
		t.Fatalf("line should be free immediately: %+v", reply)
	}
	second := f.nextPrompt(t)
	f.endTurn(t, second)
}
