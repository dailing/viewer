package chat

import (
	"strings"
	"testing"
)

func automationFixture(t *testing.T) (*Plugin, dispatchRequest) {
	t.Helper()
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	lease := AutomationLease{ID: "scheduler", Owner: "owner", Fence: 1, ExpiresAt: nowMillis() + 30000}
	gate := AutomationGate{Key: gateKey("chat", "branch"), ChatID: "chat", BranchID: "branch", AutomationID: "loop", Revision: 1}
	if err := p.store.db.Create(&lease).Error; err != nil {
		t.Fatal(err)
	}
	if err := p.store.db.Create(&gate).Error; err != nil {
		t.Fatal(err)
	}
	return p, dispatchRequest{ChatID: "chat", BranchID: "branch", RoleIDs: []string{"role"}, Message: "execute", AutomationID: "loop", IdempotencyKey: "loop/1", Owner: "owner", Fence: 1}
}
func TestAutomationDeduplicatesAndRejectsChangedPayload(t *testing.T) {
	p, r := automationFixture(t)
	if err := p.prepareDispatch(r, "dispatch"); err != nil {
		t.Fatal(err)
	}
	reply, found, err := p.existingDispatch(r)
	if err != nil || !found || reply["dispatch_id"] != "dispatch" {
		t.Fatalf("reply=%v found=%v err=%v", reply, found, err)
	}
	r.Message = "different"
	if _, _, err = p.existingDispatch(r); err == nil {
		t.Fatal("accepted different prompt for same key")
	}
	var count int64
	p.store.db.Model(&DispatchReceipt{}).Count(&count)
	if count != 1 {
		t.Fatal(count)
	}
}
func TestHumanDispatchClosesGateBeforeNextAutomaticDispatch(t *testing.T) {
	p, r := automationFixture(t)
	if err := p.prepareDispatch(dispatchRequest{ChatID: r.ChatID, BranchID: r.BranchID, Message: "change direction"}, "human"); err != nil {
		t.Fatal(err)
	}
	if err := p.prepareDispatch(r, "automatic"); err == nil {
		t.Fatal("automatic dispatch passed paused gate")
	}
	var gate AutomationGate
	p.store.db.First(&gate, "key = ?", gateKey(r.ChatID, r.BranchID))
	if !gate.Paused || gate.Revision != 2 || gate.Feedback != "change direction" {
		t.Fatalf("gate=%+v", gate)
	}
}
func TestAutomationFencesOldOwner(t *testing.T) {
	p, r := automationFixture(t)
	p.store.db.Model(&AutomationLease{}).Where("id = ?", "scheduler").Updates(map[string]any{"owner": "new", "fence": 2})
	if err := p.prepareDispatch(r, "stale"); err == nil {
		t.Fatal("old owner dispatched")
	}
	r.Owner = "new"
	r.Fence = 2
	if err := p.prepareDispatch(r, "new"); err != nil {
		t.Fatal(err)
	}
}
func TestDispatchRecoveryUsesPersistedTurnAndBoundsOutput(t *testing.T) {
	p, r := automationFixture(t)
	if err := p.prepareDispatch(r, "d"); err != nil {
		t.Fatal(err)
	}
	p.generation = "replacement"
	st, err := p.dispatchStatus(r.IdempotencyKey)
	if err != nil || st["status"] != "unknown" {
		t.Fatalf("%v %v", st, err)
	}
	end := nowMillis()
	reason := "end_turn"
	turn := Turn{ID: "t", ChatID: r.ChatID, DispatchID: "d", EndedAt: &end, StopReason: &reason}
	if err = p.store.db.Create(&turn).Error; err != nil {
		t.Fatal(err)
	}
	if err = p.store.db.Create(&Message{ID: "m", ChatID: r.ChatID, TurnID: "t", Role: "assistant", Text: strings.Repeat("中", 30000) + "LATEST"}).Error; err != nil {
		t.Fatal(err)
	}
	st, err = p.dispatchStatus(r.IdempotencyKey)
	if err != nil || st["status"] != "completed" {
		t.Fatalf("%v %v", st, err)
	}
	output := st["output"].(string)
	if len(output) > 24100 || !strings.HasSuffix(output, "LATEST") {
		t.Fatalf("output bytes=%d", len(output))
	}
}

func TestHumanSendRetractsQueuedAutomation(t *testing.T) {
	p, r := automationFixture(t)
	if err := p.prepareDispatch(r, "automatic"); err != nil {
		t.Fatal(err)
	}
	p.queues["chat\x00role\x00branch\x00branch"] = []queuedMessage{{chatID: "chat", dispatchID: "automatic", messageID: "queued-message"}}
	if err := p.prepareDispatch(dispatchRequest{ChatID: "chat", BranchID: "branch", Message: "take over"}, "human"); err != nil {
		t.Fatal(err)
	}
	st, err := p.dispatchStatus(r.IdempotencyKey)
	if err != nil || st["status"] != "cancelled" {
		t.Fatalf("status=%v error=%v", st, err)
	}
	if len(p.queues) != 0 {
		t.Fatal("automatic message remains queued")
	}
}
