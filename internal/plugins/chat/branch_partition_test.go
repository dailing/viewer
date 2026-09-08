package chat

// Branch context partitions (framework v0.66): a branch binds no role —
// dispatching on it routes exactly like a mainline dispatch (explicit
// role_ids or LLM router), each role keeps its own session lane on the
// branch, force_new_session restarts a role's branch lane, and archiving
// without merging shelves the branch out of every line's lineage.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"viewer/internal/agentdriver"
	"viewer/internal/kernel"
	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

func TestBranchContextPartition(t *testing.T) {
	config := kernel.DefaultConfig()
	config.Host, config.Port = "127.0.0.1", 0
	server := kernel.New(config)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	defer server.Shutdown(context.Background())
	url := fmt.Sprintf("ws://127.0.0.1:%d/ws", server.Port())

	configClient := busclient.New(url, busclient.Manifest{ID: "partition-config", Version: "0.1.0", Slots: map[string]any{"config:_:get": map[string]any{}}, Emits: map[string]any{}})
	_, _ = configClient.Subscribe("config:_:get", func(frame busclient.Frame) {
		_ = pluginrpc.Respond(configClient, frame, nil)
	})
	if err := configClient.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer configClient.Close()

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
	// Router stub always picks role-b; the merge draft shares the stub.
	p.llmFn = func(_ context.Context, _ []map[string]string, jsonMode bool, _ int) (completionResult, error) {
		if jsonMode {
			return completionResult{Content: `{"role_ids":["role-b"],"rationale":"stub"}`, Model: "stub"}, nil
		}
		return completionResult{Content: "stub", Model: "stub"}, nil
	}

	candidate := RoutingCandidateConfig{ID: "cand", AgentID: "hermes", ProviderID: "default", ModelID: "m", Enabled: true}
	roles := []SuperRole{
		{ID: "role-b", Name: "B", Description: "writer", RoutingPolicyID: "pol-b", CreatedAt: nowMillis(), UpdatedAt: nowMillis()},
		{ID: "role-c", Name: "C", Description: "reviewer", RoutingPolicyID: "pol-c", CreatedAt: nowMillis(), UpdatedAt: nowMillis()},
	}
	routing := RoutingConfig{DefaultRoutingPolicyID: "pol-b", RoutingPolicies: []RoutingPolicyConfig{
		{ID: "pol-b", Name: "B policy", Enabled: true, Candidates: []RoutingCandidateConfig{candidate}},
		{ID: "pol-c", Name: "C policy", Enabled: true, Candidates: []RoutingCandidateConfig{candidate}},
	}}
	if err := p.store.importDomain(roles, routing); err != nil {
		t.Fatal(err)
	}
	chat := Chat{ID: "chat-p", Name: "partition chat", Root: t.TempDir(), MemberRoleIDsJSON: `["role-b","role-c"]`, CreatedAt: nowMillis(), UpdatedAt: nowMillis()}
	if err := p.store.saveChat(&chat); err != nil {
		t.Fatal(err)
	}

	caller := busclient.New(url, busclient.Manifest{ID: "partition-caller", Version: "0.1.0", Slots: map[string]any{}, Emits: map[string]any{}})
	if err := caller.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer caller.Close()

	request := func(channel string, payload map[string]any) map[string]any {
		t.Helper()
		value, err := caller.Request(ctx, channel, payload, 10*time.Second)
		if err != nil {
			t.Fatalf("%s %v: %v", channel, payload, err)
		}
		result, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("%s reply not an object: %+v", channel, value)
		}
		return result
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
	// Turn-end persistence trails the turn-ended frame; archive/merge
	// validation reads ended_at, so wait for the branch to go idle.
	waitBranchIdle := func(branchID string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			running, err := p.store.branchHasRunningTurn(branchID)
			if err == nil && !running {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("branch %s still has a running turn", branchID)
	}
	turnOf := func(turnID string) *Turn {
		t.Helper()
		turn, err := p.store.turn(turnID)
		if err != nil || turn == nil {
			t.Fatalf("turn %s missing: %v", turnID, err)
		}
		return turn
	}

	created := request("chat:_:branches:create", map[string]any{"chat_id": "chat-p", "name": "write"})
	branchID, _ := created["id"].(string)
	if branchID == "" {
		t.Fatalf("branches:create reply: %+v", created)
	}

	// 1. Role B opens the branch: fresh session, turn stamped.
	request("chat:_:dispatch", map[string]any{"chat_id": "chat-p", "message": "draft section", "branch_id": branchID, "role_ids": []string{"role-b"}})
	bFirst := nextPrompt()
	if turn := turnOf(bFirst.turnID); turn.BranchID != branchID || turn.RoleID != "role-b" {
		t.Fatalf("B's first branch turn: %+v", turn)
	}
	endTurn(bFirst)

	// 2. A DIFFERENT role on the same branch gets its own fresh session —
	// the branch binds no single owner.
	request("chat:_:dispatch", map[string]any{"chat_id": "chat-p", "message": "review it", "branch_id": branchID, "role_ids": []string{"role-c"}})
	cFirst := nextPrompt()
	if cFirst.sessionID == bFirst.sessionID {
		t.Fatalf("role C must start its own branch session, got B's %s", cFirst.sessionID)
	}
	if turn := turnOf(cFirst.turnID); turn.BranchID != branchID || turn.RoleID != "role-c" {
		t.Fatalf("C's first branch turn: %+v", turn)
	}
	endTurn(cFirst)

	// 3. Each role resumes its OWN branch lane.
	request("chat:_:dispatch", map[string]any{"chat_id": "chat-p", "message": "revise", "branch_id": branchID, "role_ids": []string{"role-b"}})
	bSecond := nextPrompt()
	if bSecond.sessionID != bFirst.sessionID {
		t.Fatalf("role B should resume its branch lane %s, got %s", bFirst.sessionID, bSecond.sessionID)
	}
	endTurn(bSecond)

	// 4. force_new_session restarts the role's branch lane.
	request("chat:_:dispatch", map[string]any{"chat_id": "chat-p", "message": "fresh take", "branch_id": branchID, "role_ids": []string{"role-b"}, "force_new_session": true})
	bFresh := nextPrompt()
	if bFresh.sessionID == bFirst.sessionID {
		t.Fatalf("force_new_session on a branch must start a new session, resumed %s", bFresh.sessionID)
	}
	endTurn(bFresh)

	// 5. No role picks → the LLM router runs on the branch exactly like a
	// mainline dispatch (stub picks role-b → resumes its lane).
	request("chat:_:dispatch", map[string]any{"chat_id": "chat-p", "message": "routed message", "branch_id": branchID})
	routed := nextPrompt()
	if routed.sessionID != bFresh.sessionID {
		t.Fatalf("router-picked role B should resume its latest branch session %s, got %s", bFresh.sessionID, routed.sessionID)
	}
	endTurn(routed)

	// 6. Archive WITHOUT merge: the branch shelves, dispatch is refused,
	// re-archive is refused, and its turns join no line's lineage.
	waitBranchIdle(branchID)
	archived := request("chat:_:branches:archive", map[string]any{"chat_id": "chat-p", "branch_ids": []string{branchID}})
	if archived["archived"] != true {
		t.Fatalf("branches:archive reply: %+v", archived)
	}
	if _, err := caller.Request(ctx, "chat:_:dispatch", map[string]any{"chat_id": "chat-p", "message": "too late", "branch_id": branchID}, 10*time.Second); err == nil {
		t.Fatal("dispatch to an archived branch should fail")
	}
	if _, err := caller.Request(ctx, "chat:_:branches:archive", map[string]any{"chat_id": "chat-p", "branch_ids": []string{branchID}}, 10*time.Second); err == nil {
		t.Fatal("re-archiving should fail")
	}
	lineage, err := p.lineageTurns("chat-p", "", nowMillis()+1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range lineage {
		if turn.BranchID == branchID {
			t.Fatalf("archive-only branch turn %s leaked into the mainline lineage", turn.ID)
		}
	}
	row, err := p.store.branch(branchID)
	if err != nil || row == nil || row.ArchivedAt == nil || row.MergeMessageID != "" || row.MergedIntoBranchID != "" {
		t.Fatalf("archive-only branch record: %+v err=%v", row, err)
	}

	// 7. A running branch refuses archiving.
	second := request("chat:_:branches:create", map[string]any{"chat_id": "chat-p", "name": "running"})
	secondID, _ := second["id"].(string)
	request("chat:_:dispatch", map[string]any{"chat_id": "chat-p", "message": "long work", "branch_id": secondID, "role_ids": []string{"role-b"}})
	running := nextPrompt()
	if _, err := caller.Request(ctx, "chat:_:branches:archive", map[string]any{"chat_id": "chat-p", "branch_ids": []string{secondID}}, 10*time.Second); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("archiving a running branch should fail with the running error, got %v", err)
	}
	endTurn(running)
	waitBranchIdle(secondID)
	done := request("chat:_:branches:archive", map[string]any{"chat_id": "chat-p", "branch_ids": []string{secondID}})
	if done["archived"] != true {
		t.Fatalf("archive after turn end: %+v", done)
	}
}
