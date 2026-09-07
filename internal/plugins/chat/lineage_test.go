package chat

import (
	"testing"
)

// Lineage collector (framework v0.64): fork ancestry cut off at the fork
// turn, merged branches unioned into their target line by time.
func TestLineageTurnsForkAndMerge(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	chatID := "c"
	mkTurn := func(id, branchID string, started int64) {
		turn := &Turn{ID: id, ChatID: chatID, BranchID: branchID, DispatchID: "d-" + id, StartedAt: started}
		if err := p.store.beginTurn(turn); err != nil {
			t.Fatal(err)
		}
	}
	// Mainline: m1(10) m2(20) — fork point — m3(50).
	mkTurn("m1", "", 10)
	mkTurn("m2", "", 20)
	mkTurn("m3", "", 50)
	// Branch B forks at m2: b1(30) b2(40).
	branchB := &Branch{ID: "B", ChatID: chatID, Name: "B", ForkTurnID: "m2", ParentBranchID: "", CreatedAt: 25, UpdatedAt: 25}
	if err := p.store.createBranch(branchB); err != nil {
		t.Fatal(err)
	}
	mkTurn("b1", "B", 30)
	mkTurn("b2", "B", 40)
	// Branch C forks from B at b1: c1(35). Multi-level ancestry.
	branchC := &Branch{ID: "C", ChatID: chatID, Name: "C", ForkTurnID: "b1", ParentBranchID: "B", CreatedAt: 32, UpdatedAt: 32}
	if err := p.store.createBranch(branchC); err != nil {
		t.Fatal(err)
	}
	mkTurn("c1", "C", 35)

	ids := func(turns []Turn, err error) []string {
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(turns))
		for _, turn := range turns {
			out = append(out, turn.ID)
		}
		return out
	}
	assert := func(label string, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v want %v", label, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: got %v want %v", label, got, want)
			}
		}
	}

	// The branch sees shared mainline history up to the fork turn plus its
	// own turns — not m3 (mainline moved on after the fork).
	assert("branch B", ids(p.lineageTurns(chatID, "B", 100)), []string{"m1", "m2", "b1", "b2"})
	// The mainline does not see unmerged branch work.
	assert("mainline pre-merge", ids(p.lineageTurns(chatID, "", 100)), []string{"m1", "m2", "m3"})
	// Multi-level: C inherits B up to b1 (not b2) plus the mainline to m2.
	assert("branch C", ids(p.lineageTurns(chatID, "C", 100)), []string{"m1", "m2", "b1", "c1"})

	// Merge B into the mainline: B's turns join the mainline record by time.
	now := int64(60)
	branchB.ArchivedAt = &now
	branchB.MergedIntoBranchID = ""
	if err := p.store.saveBranch(branchB); err != nil {
		t.Fatal(err)
	}
	assert("mainline post-merge", ids(p.lineageTurns(chatID, "", 100)), []string{"m1", "m2", "b1", "b2", "m3"})

	// A branch merged into a branch joins THAT branch's lineage (and the
	// mainline's transitively once the target itself merges).
	branchB2 := &Branch{ID: "B2", ChatID: chatID, Name: "B2", ForkTurnID: "m1", ParentBranchID: "", CreatedAt: 12, UpdatedAt: 12}
	if err := p.store.createBranch(branchB2); err != nil {
		t.Fatal(err)
	}
	mkTurn("d1", "B2", 15)
	branchC2 := &Branch{ID: "C2", ChatID: chatID, Name: "C2", ForkTurnID: "d1", ParentBranchID: "B2", CreatedAt: 16, UpdatedAt: 16}
	if err := p.store.createBranch(branchC2); err != nil {
		t.Fatal(err)
	}
	mkTurn("e1", "C2", 17)
	branchC2.ArchivedAt = &now
	branchC2.MergedIntoBranchID = "B2"
	if err := p.store.saveBranch(branchC2); err != nil {
		t.Fatal(err)
	}
	assert("branch B2 absorbs C2", ids(p.lineageTurns(chatID, "B2", 100)), []string{"m1", "d1", "e1"})
	assert("mainline does not see B2 yet", ids(p.lineageTurns(chatID, "", 100)), []string{"m1", "m2", "b1", "b2", "m3"})
	branchB2.ArchivedAt = &now
	branchB2.MergedIntoBranchID = ""
	if err := p.store.saveBranch(branchB2); err != nil {
		t.Fatal(err)
	}
	assert("mainline absorbs B2+C2", ids(p.lineageTurns(chatID, "", 100)), []string{"m1", "d1", "e1", "m2", "b1", "b2", "m3"})
}

// Prev-turn stamping (framework v0.64): branch first turn points at the fork
// turn, continuations at the branch's latest turn, mainline at its own head.
func TestPrevTurnFor(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	chatID := "c"
	mkTurn := func(id, branchID, sessionID string, started int64) {
		turn := &Turn{ID: id, ChatID: chatID, BranchID: branchID, SessionID: sessionID, StartedAt: started}
		if err := p.store.beginTurn(turn); err != nil {
			t.Fatal(err)
		}
	}
	if got := p.prevTurnFor(chatID, relayTarget{}); got != "" {
		t.Fatalf("empty mainline prev=%q", got)
	}
	mkTurn("m1", "", "s-main", 10)
	mkTurn("m2", "", "s-main", 20)
	branch := &Branch{ID: "B", ChatID: chatID, Name: "B", ForkTurnID: "m2", CreatedAt: 25, UpdatedAt: 25}
	if err := p.store.createBranch(branch); err != nil {
		t.Fatal(err)
	}
	if got := p.prevTurnFor(chatID, relayTarget{branch: "B"}); got != "m2" {
		t.Fatalf("branch first turn prev=%q want m2", got)
	}
	mkTurn("b1", "B", "s-branch", 30)
	if got := p.prevTurnFor(chatID, relayTarget{branch: "B"}); got != "b1" {
		t.Fatalf("branch continuation prev=%q want b1", got)
	}
	if got := p.prevTurnFor(chatID, relayTarget{resume: "s-main"}); got != "m2" {
		t.Fatalf("lane continuation prev=%q want m2", got)
	}
	if got := p.prevTurnFor(chatID, relayTarget{}); got != "m2" {
		t.Fatalf("mainline prev=%q want m2", got)
	}
}
