package chat

// Branch context partitions (framework v0.66+): a branch binds no role —
// dispatching on it routes exactly like a mainline dispatch (explicit
// role_ids or LLM router), each role keeps its own session lane on the
// branch, force_new_session restarts a role's branch lane, and archiving
// (v0.78) merges the branch into the chat's terminal 归档 line.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

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
	// validation reads ended_at AND the line's busy flag (cleared only when
	// the relay finishes settling the batch), so wait for both to go idle.
	waitBranchIdle := func(branchID string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			running, err := p.store.branchHasRunningTurn(branchID)
			p.mu.Lock()
			busy := p.busy[lineKey("chat-p", branchID)]
			p.mu.Unlock()
			if err == nil && !running && !busy {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("branch %s still busy", branchID)
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

	// 6. Archive = merge into the chat's terminal 归档 line (v0.78): the
	// line is born on first archive, the source closes as merged into it,
	// dispatch to the source is refused, re-archive is refused, and the
	// source's turns join the 归档 line's snapshot — never the mainline's.
	waitBranchIdle(branchID)
	archiveLineID := archiveBranchID("chat-p")
	archived := request("chat:_:branches:archive", map[string]any{"chat_id": "chat-p", "branch_ids": []string{branchID}})
	if archived["archived"] != true || archived["archive_branch_id"] != archiveLineID {
		t.Fatalf("branches:archive reply: %+v", archived)
	}
	if _, err := caller.Request(ctx, "chat:_:dispatch", map[string]any{"chat_id": "chat-p", "message": "too late", "branch_id": branchID}, 10*time.Second); err == nil {
		t.Fatal("dispatch to an archived branch should fail")
	}
	if _, err := caller.Request(ctx, "chat:_:branches:archive", map[string]any{"chat_id": "chat-p", "branch_ids": []string{branchID}}, 10*time.Second); err == nil {
		t.Fatal("re-archiving should fail")
	}
	archiveRow, err := p.store.branch(archiveLineID)
	if err != nil || archiveRow == nil || archiveRow.Name != archiveBranchName || archiveRow.State != branchStateOpen {
		t.Fatalf("archive line record: %+v err=%v", archiveRow, err)
	}
	archiveHead, err := p.store.lineHead("chat-p", archiveLineID)
	if err != nil || archiveHead == nil {
		t.Fatalf("archive line head: %v err=%v", archiveHead, err)
	}
	archiveSnapshot, err := p.store.snapshotTurns("chat-p", archiveHead.HeadNodeID)
	if err != nil {
		t.Fatal(err)
	}
	archivedContent := false
	for _, turn := range archiveSnapshot.Turns {
		if turn.BranchID == branchID {
			archivedContent = true
		}
	}
	if !archivedContent {
		t.Fatal("archived branch turns should join the 归档 line's snapshot")
	}
	mainlineHead, err := p.store.lineHead("chat-p", "")
	if err != nil {
		t.Fatal(err)
	}
	if mainlineHead == nil {
		// No mainline batch ever ran: create the head on demand (same as
		// the view-filtered load does) so the snapshot check is meaningful.
		err = p.store.db.Transaction(func(tx *gorm.DB) error {
			var headErr error
			mainlineHead, headErr = ensureLineHead(tx, "chat-p", "")
			return headErr
		})
		if err != nil || mainlineHead == nil {
			t.Fatalf("mainline head: %v err=%v", mainlineHead, err)
		}
	}
	snapshot, err := p.store.snapshotTurns("chat-p", mainlineHead.HeadNodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range snapshot.Turns {
		if turn.BranchID == branchID {
			t.Fatalf("archived branch turn %s leaked into the mainline snapshot", turn.ID)
		}
	}
	row, err := p.store.branch(branchID)
	if err != nil || row == nil || row.ArchivedAt == nil || row.MergedAt == nil || row.MergeNodeID == "" || row.State != branchStateMerged || row.MergedIntoBranchID != archiveLineID {
		t.Fatalf("archived branch record: %+v err=%v", row, err)
	}

	// 7. A running branch refuses archiving.
	second := request("chat:_:branches:create", map[string]any{"chat_id": "chat-p", "name": "busy-lane"})
	secondID, _ := second["id"].(string)
	request("chat:_:dispatch", map[string]any{"chat_id": "chat-p", "message": "long work", "branch_id": secondID, "role_ids": []string{"role-b"}})
	running := nextPrompt()
	if _, err := caller.Request(ctx, "chat:_:branches:archive", map[string]any{"chat_id": "chat-p", "branch_ids": []string{secondID}}, 10*time.Second); err == nil || !strings.Contains(err.Error(), "busy-lane") {
		t.Fatalf("archiving a running branch should fail naming the busy line, got %v", err)
	}
	endTurn(running)
	waitBranchIdle(secondID)
	done := request("chat:_:branches:archive", map[string]any{"chat_id": "chat-p", "branch_ids": []string{secondID}})
	if done["archived"] != true {
		t.Fatalf("archive after turn end: %+v", done)
	}

	// 8. A turn on a MERGED line stays forkable: its content joined the
	// target line's history, so the fork attributes to the surviving line
	// ("" = mainline here).
	mergeSrc := request("chat:_:branches:create", map[string]any{"chat_id": "chat-p", "name": "to-merge"})
	mergeSrcID, _ := mergeSrc["id"].(string)
	request("chat:_:dispatch", map[string]any{"chat_id": "chat-p", "message": "merge me", "branch_id": mergeSrcID, "role_ids": []string{"role-b"}})
	mergedTurn := nextPrompt()
	endTurn(mergedTurn)
	waitBranchIdle(mergeSrcID)
	mergedReply := request("chat:_:branches:merge", map[string]any{"chat_id": "chat-p", "branch_ids": []string{mergeSrcID}})
	if mergedReply["merged"] != true {
		t.Fatalf("branches:merge reply: %+v", mergedReply)
	}
	forked := request("chat:_:branches:create", map[string]any{"chat_id": "chat-p", "name": "post-merge-fork", "from_turn_id": mergedTurn.turnID})
	forkedID, _ := forked["id"].(string)
	forkedRow, err := p.store.branch(forkedID)
	if err != nil || forkedRow == nil || forkedRow.ParentBranchID != "" || forkedRow.ForkTurnID != mergedTurn.turnID {
		t.Fatalf("fork from a merged turn should attribute to the mainline: %+v err=%v", forkedRow, err)
	}

	// 9. A turn on an ARCHIVED line stays forkable too: archiving is a
	// merge, so the fork resolves the merge chain and attributes to the
	// surviving 归档 line.
	forkedArchive := request("chat:_:branches:create", map[string]any{"chat_id": "chat-p", "name": "post-archive-fork", "from_turn_id": bFirst.turnID})
	forkedArchiveID, _ := forkedArchive["id"].(string)
	forkedArchiveRow, err := p.store.branch(forkedArchiveID)
	if err != nil || forkedArchiveRow == nil || forkedArchiveRow.ParentBranchID != archiveLineID || forkedArchiveRow.ForkTurnID != bFirst.turnID {
		t.Fatalf("fork from an archived turn should attribute to the 归档 line: %+v err=%v", forkedArchiveRow, err)
	}

	// 10. The 归档 line is terminal: it refuses being merged away,
	// archived, or deleted.
	if _, err := caller.Request(ctx, "chat:_:branches:merge", map[string]any{"chat_id": "chat-p", "branch_ids": []string{archiveLineID}}, 10*time.Second); err == nil {
		t.Fatal("merging the archive line away should fail")
	}
	if _, err := caller.Request(ctx, "chat:_:branches:archive", map[string]any{"chat_id": "chat-p", "branch_ids": []string{archiveLineID}}, 10*time.Second); err == nil {
		t.Fatal("archiving the archive line should fail")
	}
	if _, err := caller.Request(ctx, "chat:_:branches:delete", map[string]any{"id": archiveLineID}, 10*time.Second); err == nil {
		t.Fatal("deleting the archive line should fail")
	}
}
