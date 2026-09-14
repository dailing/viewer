package chat

// History-DAG unit tests (docs/chat-branch-dag-plan.md): the one-shot
// legacy→DAG migration (fixture with forked, merged, archive-only, and
// orphan-input lines), batch settlement (single result, join node, crash
// recovery), and snapshot membership. Pure store tests — no kernel, no
// bus: everything runs against a real sqlite database.

import (
	"testing"
)

// legacyFixture builds a pre-DAG database shape: a mainline with three
// turns, branch B forked after m2 and merged into the mainline (with a
// legacy merge message), branch C forked off B and merged into B, branch D
// archived without merging, and an orphan user input whose dispatch never
// produced a turn.
func legacyFixture(t *testing.T, s *store, chatID string) {
	t.Helper()
	end := func(turnID string) {
		t.Helper()
		if err := s.completeTurn(turnID, "end_turn"); err != nil {
			t.Fatalf("complete %s: %v", turnID, err)
		}
	}
	turn := func(id, branch string, started int64) {
		t.Helper()
		if err := s.beginTurn(&Turn{ID: id, ChatID: chatID, RoleID: "role-x", RoleName: "X", DispatchID: "d-" + id, BranchID: branch, StartedAt: started}); err != nil {
			t.Fatalf("begin %s: %v", id, err)
		}
		end(id)
	}
	user := func(id, dispatch string, created int64) {
		t.Helper()
		if err := s.addMessage(&Message{ID: id, ChatID: chatID, Role: "user", TurnID: dispatch, Text: "input " + id, CreatedAt: created}); err != nil {
			t.Fatalf("user message %s: %v", id, err)
		}
	}
	// Mainline: m1, m2, (B forks here), m3.
	turn("m1", "", 100)
	user("u-m1", "d-m1", 99)
	turn("m2", "", 200)
	// Branch B off m2, two turns, merged into the mainline via msg-b.
	if err := s.createBranch(&Branch{ID: "B", ChatID: chatID, Name: "B", ForkTurnID: "m2", CreatedAt: 250, UpdatedAt: 250}); err != nil {
		t.Fatal(err)
	}
	turn("b1", "B", 300)
	turn("b2", "B", 400)
	// Branch C off b2, one turn, merged into B via msg-c.
	if err := s.createBranch(&Branch{ID: "C", ChatID: chatID, Name: "C", ForkTurnID: "b2", ParentBranchID: "B", CreatedAt: 450, UpdatedAt: 450}); err != nil {
		t.Fatal(err)
	}
	turn("c1", "C", 500)
	// Branch D off m1, archived WITHOUT merging (archive-only).
	if err := s.createBranch(&Branch{ID: "D", ChatID: chatID, Name: "D", ForkTurnID: "m1", CreatedAt: 550, UpdatedAt: 550}); err != nil {
		t.Fatal(err)
	}
	turn("d1", "D", 600)
	turn("m3", "", 700)
	// Legacy merge records: C → B (msg-c), B → mainline (msg-b). A confirm
	// stamped every source with the same merge message id.
	if err := s.addMessage(&Message{ID: "msg-c", ChatID: chatID, Role: "user", TurnID: "d-msg-c", Text: "merge C into B", CreatedAt: 800}); err != nil {
		t.Fatal(err)
	}
	if err := s.addMessage(&Message{ID: "msg-b", ChatID: chatID, Role: "user", TurnID: "d-msg-b", Text: "merge B into mainline", CreatedAt: 900}); err != nil {
		t.Fatal(err)
	}
	archive := func(id, messageID, into string, at int64) {
		t.Helper()
		if err := s.db.Model(&Branch{}).Where("id = ?", id).Updates(map[string]any{"archived_at": at, "merge_message_id": messageID, "merged_into_branch_id": into}).Error; err != nil {
			t.Fatalf("archive %s: %v", id, err)
		}
	}
	archive("C", "msg-c", "B", 800)
	archive("B", "msg-b", "", 900)
	if err := s.db.Model(&Branch{}).Where("id = ?", "D").Update("archived_at", int64(1000)).Error; err != nil {
		t.Fatal(err)
	}
	// Orphan input: its dispatch never produced a turn.
	user("u-orphan", "d-orphan", 950)
}

func snapshotIDs(t *testing.T, s *store, chatID, branchID string) (map[string]bool, []string) {
	t.Helper()
	var head LineHead
	if err := s.db.Where("chat_id = ? AND branch_id = ?", chatID, branchID).Limit(1).Find(&head).Error; err != nil || head.HeadNodeID == "" {
		t.Fatalf("line head %s/%s missing: %v", chatID, branchID, err)
	}
	snapshot, err := s.snapshotTurns(chatID, head.HeadNodeID)
	if err != nil {
		t.Fatalf("snapshot %s/%s: %v", chatID, branchID, err)
	}
	set := map[string]bool{}
	for _, turn := range snapshot.Turns {
		set[turn.ID] = true
	}
	return set, snapshot.InputMessageIDs
}

func requireMembers(t *testing.T, set map[string]bool, want ...string) {
	t.Helper()
	for _, id := range want {
		if !set[id] {
			t.Fatalf("snapshot missing %s (has %v)", id, set)
		}
	}
}

func requireAbsent(t *testing.T, set map[string]bool, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if set[id] {
			t.Fatalf("snapshot must not contain %s (has %v)", id, set)
		}
	}
}

func TestHistoryMigrationBuildsAuthoritativeGraph(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	chatID := "chat-mig"
	if err := p.store.saveChat(&Chat{ID: chatID, Name: "migration", Root: t.TempDir(), CreatedAt: nowMillis(), UpdatedAt: nowMillis()}); err != nil {
		t.Fatal(err)
	}
	legacyFixture(t, p.store, chatID)

	report := &migrationReport{}
	if err := p.store.migrateChatGraph(chatID, report); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := p.store.validateGraph(report); err != nil {
		t.Fatalf("validate: %v", err)
	}

	// Mainline absorbs B (and C through B) at the merge node's historical
	// spot; archive-only D never joins another line.
	mainline, inputs := snapshotIDs(t, p.store, chatID, "")
	requireMembers(t, mainline, "m1", "m2", "m3", "b1", "b2", "c1")
	requireAbsent(t, mainline, "d1")
	found := false
	for _, id := range inputs {
		if id == "u-orphan" {
			found = true
		}
	}
	if !found {
		t.Fatalf("orphan input u-orphan missing from mainline snapshot inputs: %v", inputs)
	}

	// B's own head retains its full lineage including merged C.
	branchB, _ := snapshotIDs(t, p.store, chatID, "B")
	requireMembers(t, branchB, "m1", "m2", "b1", "b2", "c1")
	requireAbsent(t, branchB, "m3", "d1")

	// C forked off b2: mainline turns after the fork point are absent.
	branchC, _ := snapshotIDs(t, p.store, chatID, "C")
	requireMembers(t, branchC, "m1", "m2", "b1", "b2", "c1")
	requireAbsent(t, branchC, "m3")

	// D keeps only its own shelved line.
	branchD, _ := snapshotIDs(t, p.store, chatID, "D")
	requireMembers(t, branchD, "m1", "d1")
	requireAbsent(t, branchD, "m2", "m3", "b1", "b2", "c1")

	// Branch states were backfilled.
	for id, want := range map[string]string{"B": branchStateMerged, "C": branchStateMerged, "D": branchStateArchived} {
		branch, err := p.store.branch(id)
		if err != nil || branch == nil || branch.State != want {
			t.Fatalf("branch %s state = %+v, want %s (err=%v)", id, branch, want, err)
		}
		if want == branchStateMerged && (branch.MergedAt == nil || branch.MergeNodeID == "") {
			t.Fatalf("branch %s merged markers missing: %+v", id, branch)
		}
	}

	// The merge node splices into the mainline chain at its historical time
	// (after m3, which predates the merge) with parents = the mainline
	// chain's previous node + B's FINAL chain head. B's head is the nested
	// merge:msg-c node (C grafted into B first), so C's content reaches the
	// mainline through it — never through a stale b2 edge. The legacy
	// summary message msg-c survives as an orphan input node on the
	// mainline chain.
	var mergeEdges []HistoryEdge
	if err := p.store.db.Where("child_id = ?", "merge:msg-b").Find(&mergeEdges).Error; err != nil {
		t.Fatal(err)
	}
	parents := map[string]bool{}
	for _, edge := range mergeEdges {
		parents[edge.ParentID] = true
	}
	if len(parents) != 2 || !parents["merge:msg-c"] {
		t.Fatalf("merge:msg-b parents should be the chain prev + B's head (merge:msg-c), got %v", parents)
	}
	var m3Edge []HistoryEdge
	if err := p.store.db.Where("child_id = ?", turnNodeID("m3")).Find(&m3Edge).Error; err != nil {
		t.Fatal(err)
	}
	if len(m3Edge) != 1 || m3Edge[0].ParentID != turnNodeID("m2") {
		t.Fatalf("m3 chains after m2 (the merge happened later), got %+v", m3Edge)
	}

	// Idempotent: a second migration pass leaves the graph untouched.
	var nodesBefore, edgesBefore int64
	p.store.db.Model(&HistoryNode{}).Where("chat_id = ?", chatID).Count(&nodesBefore)
	p.store.db.Model(&HistoryEdge{}).Count(&edgesBefore)
	if err := p.store.migrateChatGraph(chatID, &migrationReport{}); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var nodesAfter, edgesAfter int64
	p.store.db.Model(&HistoryNode{}).Where("chat_id = ?", chatID).Count(&nodesAfter)
	p.store.db.Model(&HistoryEdge{}).Count(&edgesAfter)
	if nodesBefore != nodesAfter || edgesBefore != edgesAfter {
		t.Fatalf("migration not idempotent: nodes %d→%d edges %d→%d", nodesBefore, nodesAfter, edgesBefore, edgesAfter)
	}
}

// TestGraphBatchSettlement covers the runtime-side graph writes without a
// kernel: intake snapshots pin the batch input, single results advance the
// head directly, multi-role results收束 into a join node, and startup
// recovery finalizes interrupted turns plus orphan inputs idempotently.
func TestGraphBatchSettlement(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	chatID := "chat-graph"
	if err := p.store.saveChat(&Chat{ID: chatID, Name: "graph", Root: t.TempDir(), CreatedAt: nowMillis(), UpdatedAt: nowMillis()}); err != nil {
		t.Fatal(err)
	}
	end := func(turn *Turn) *Turn {
		t.Helper()
		if err := p.store.completeTurn(turn.ID, "end_turn"); err != nil {
			t.Fatal(err)
		}
		done, err := p.store.turn(turn.ID)
		if err != nil || done == nil || done.EndedAt == nil {
			t.Fatalf("turn %s not ended: %v", turn.ID, err)
		}
		return done
	}

	// Batch 1: one role — the result node becomes the head directly.
	batch1, input1, stale, err := p.intakeBatch(chatID, "", "d1")
	if err != nil || stale {
		t.Fatalf("intake batch1: input=%s stale=%v err=%v", input1, stale, err)
	}
	if input1 != mainlineRootNodeID(chatID) {
		t.Fatalf("first intake should snapshot the mainline root, got %s", input1)
	}
	t1 := &Turn{ID: "t1", ChatID: chatID, RoleID: "role-x", DispatchID: "d1", BatchID: batch1, StartedAt: 100}
	if err := p.store.beginTurn(t1); err != nil {
		t.Fatal(err)
	}
	node1, err := p.publishTurnNode(chatID, "", end(t1), input1)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.advanceBatch(chatID, "", batch1, []string{node1}); err != nil {
		t.Fatal(err)
	}
	head, err := p.store.lineHead(chatID, "")
	if err != nil || head == nil || head.HeadNodeID != node1 {
		t.Fatalf("single-result head should be the turn node: %+v err=%v", head, err)
	}

	// Batch 2: two roles — the head advances to a join node over both.
	batch2, input2, _, err := p.intakeBatch(chatID, "", "d2")
	if err != nil {
		t.Fatal(err)
	}
	if input2 != node1 {
		t.Fatalf("batch2 must snapshot batch1's head %s, got %s", node1, input2)
	}
	nodeIDs := []string{}
	for _, id := range []string{"t2", "t3"} {
		turn := &Turn{ID: id, ChatID: chatID, RoleID: "role-x", DispatchID: "d2", BatchID: batch2, StartedAt: 200}
		if err := p.store.beginTurn(turn); err != nil {
			t.Fatal(err)
		}
		nodeID, err := p.publishTurnNode(chatID, "", end(turn), input2)
		if err != nil {
			t.Fatal(err)
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	if err := p.advanceBatch(chatID, "", batch2, nodeIDs); err != nil {
		t.Fatal(err)
	}
	head, err = p.store.lineHead(chatID, "")
	if err != nil || head == nil || head.HeadNodeID != joinNodeID(batch2, "") {
		t.Fatalf("multi-result head should be the join node: %+v err=%v", head, err)
	}
	snapshot, err := p.store.snapshotTurns(chatID, head.HeadNodeID)
	if err != nil {
		t.Fatal(err)
	}
	turnSet := map[string]bool{}
	for _, turn := range snapshot.Turns {
		turnSet[turn.ID] = true
	}
	requireMembers(t, turnSet, "t1", "t2", "t3")

	// Crash recovery: an interrupted turn (no ended_at) finalizes and
	// publishes off its batch snapshot; an orphan input becomes an input
	// node. Both are idempotent across repeated recovery runs.
	batch3, input3, _, err := p.intakeBatch(chatID, "", "d3")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.store.beginTurn(&Turn{ID: "t4", ChatID: chatID, RoleID: "role-x", DispatchID: "d3", BatchID: batch3, StartedAt: 300}); err != nil {
		t.Fatal(err)
	}
	_ = input3
	if err := p.store.addMessage(&Message{ID: "u-orphan", ChatID: chatID, Role: "user", TurnID: "d-orphan", Text: "lost", CreatedAt: 350}); err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 2; run++ {
		if err := p.recoverHistory(); err != nil {
			t.Fatalf("recovery run %d: %v", run, err)
		}
	}
	t4, err := p.store.turn("t4")
	if err != nil || t4.EndedAt == nil || t4.StopReason == nil || *t4.StopReason != "interrupted" {
		t.Fatalf("t4 should be finalized as interrupted: %+v err=%v", t4, err)
	}
	head, err = p.store.lineHead(chatID, "")
	if err != nil || head == nil || head.HeadNodeID != inputNodeID("u-orphan") {
		t.Fatalf("head should end at the orphan input node: %+v err=%v", head, err)
	}
	snapshot, err = p.store.snapshotTurns(chatID, head.HeadNodeID)
	if err != nil {
		t.Fatal(err)
	}
	turnSet = map[string]bool{}
	for _, turn := range snapshot.Turns {
		turnSet[turn.ID] = true
	}
	requireMembers(t, turnSet, "t1", "t2", "t3", "t4")
	requireAbsent(t, turnSet, "nope")
	inputFound := false
	for _, id := range snapshot.InputMessageIDs {
		if id == "u-orphan" {
			inputFound = true
		}
	}
	if !inputFound {
		t.Fatalf("orphan input missing after recovery: %v", snapshot.InputMessageIDs)
	}
}
