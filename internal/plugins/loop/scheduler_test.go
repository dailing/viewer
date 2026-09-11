package loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeChat struct {
	mu                 sync.Mutex
	gate               gate
	states             map[string]dispatchStatus
	sends              int
	cancels            int
	timeoutAfterAccept bool
	judge              string
	judgeCalls         int
}

func (f *fakeChat) call(ctx context.Context, ch string, v any, _ time.Duration) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := v.(map[string]any)
	switch ch {
	case "chat:_:automation":
		switch m["op"] {
		case "get", "claim":
			return f.gate, nil
		case "pause":
			f.gate.Paused = true
			f.gate.Revision++
			return f.gate, nil
		case "resume":
			if m["revision"] != f.gate.Revision {
				return nil, errors.New("revision mismatch")
			}
			f.gate.Paused = false
			return f.gate, nil
		case "release":
			f.gate.AutomationID = ""
			return f.gate, nil
		case "describe":
			return map[string]any{"root": "/work"}, nil
		}
	case "chat:_:dispatch-status":
		key := m["idempotency_key"].(string)
		st, ok := f.states[key]
		if !ok {
			return dispatchStatus{Status: "missing"}, nil
		}
		if m["op"] == "cancel" {
			f.cancels++
		} // cancellation is NOT completion
		return st, nil
	case "chat:_:dispatch":
		key := m["idempotency_key"].(string)
		if _, ok := f.states[key]; !ok {
			f.sends++
			f.states[key] = dispatchStatus{Status: "running", DispatchID: "d", TurnID: "t", StartedAt: now()}
		}
		if f.timeoutAfterAccept {
			return nil, context.DeadlineExceeded
		}
		return map[string]any{"dispatch_id": "d"}, nil
	case "llm:_:complete":
		f.judgeCalls++
		if f.judge == "" {
			return nil, errors.New("judge down")
		}
		return map[string]any{"content": f.judge}, nil
	}
	return nil, errors.New("unexpected fake RPC: " + ch)
}
func fixture(t *testing.T) (*Plugin, *fakeChat, *Loop) {
	t.Helper()
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.fence = 1
	p.leaseUntil = now() + 60000
	f := &fakeChat{gate: gate{AutomationID: "run", Revision: 1}, states: map[string]dispatchStatus{}}
	p.call = f.call
	l := &Loop{ID: "run", ChatID: "chat", RoleID: "role", BranchID: "branch", Root: "/work", Goal: "goal", Criteria: "test passes", State: "running", Deadline: now() + 60000, MaxIterations: 20, TurnTimeoutSeconds: 900, JudgeEvery: 3, ProgressPath: filepath.Join(t.TempDir(), "progress.md")}
	if err = p.db.Create(l).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.cancel(); p.wg.Wait(); db, _ := p.db.DB(); db.Close() })
	return p, f, l
}
func advance(t *testing.T, p *Plugin, l *Loop) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.db.First(l, "id = ?", l.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := p.advance(l); err != nil {
		t.Fatal(err)
	}
}
func TestTimeoutAfterAcceptanceNeverResendsLogicalIteration(t *testing.T) {
	p, f, l := fixture(t)
	f.timeoutAfterAccept = true
	advance(t, p, l)
	advance(t, p, l)
	advance(t, p, l)
	if f.sends != 1 || l.Iteration != 1 {
		t.Fatalf("sends=%d iteration=%d", f.sends, l.Iteration)
	}
}
func TestStopWaitsForCancellationAndDoesNotTouchOtherTurns(t *testing.T) {
	p, f, l := fixture(t)
	advance(t, p, l)
	l.State = "stopping"
	p.db.Save(l)
	advance(t, p, l)
	if l.State != "stopping" || f.cancels != 1 {
		t.Fatalf("state=%s cancels=%d", l.State, f.cancels)
	}
	key := iterationKey(l.ID, 1)
	f.states[key] = dispatchStatus{Status: "completed", StopReason: "cancelled", EndedAt: now()}
	advance(t, p, l)
	advance(t, p, l)
	if l.State != "stopped" || f.sends != 1 {
		t.Fatalf("state=%s sends=%d", l.State, f.sends)
	}
}
func TestHumanGatePausesNextRound(t *testing.T) {
	p, f, l := fixture(t)
	advance(t, p, l)
	f.gate.Paused = true
	f.gate.Feedback = "new direction"
	f.states[iterationKey(l.ID, 1)] = dispatchStatus{Status: "completed", StopReason: "end_turn", Output: `<loop-result>{"status":"continue","summary":"step one","next_step":"step two"}</loop-result>`, EndedAt: now()}
	advance(t, p, l)
	advance(t, p, l)
	if l.State != "paused" || f.sends != 1 || l.UserFeedback != "new direction" {
		t.Fatalf("loop=%+v sends=%d", l, f.sends)
	}
}
func TestDeadlineCancelsAndLimitsRatherThanCompletes(t *testing.T) {
	p, f, l := fixture(t)
	advance(t, p, l)
	l.Deadline = now() - 1
	p.db.Save(l)
	advance(t, p, l)
	if l.State != "limiting" || f.cancels != 1 {
		t.Fatalf("%s %d", l.State, f.cancels)
	}
	f.states[iterationKey(l.ID, 1)] = dispatchStatus{Status: "cancelled"}
	advance(t, p, l)
	advance(t, p, l)
	if l.State != "limited" {
		t.Fatal(l.State)
	}
}
func TestCompletionRequiresJudgeAndSurvivesDuplicateEvents(t *testing.T) {
	p, f, l := fixture(t)
	f.judge = `{"status":"complete","reason":"test evidence verified","feedback":"","progress":true}`
	advance(t, p, l)
	f.states[iterationKey(l.ID, 1)] = dispatchStatus{Status: "completed", StopReason: "end_turn", Output: `<loop-result>{"status":"complete","summary":"done","evidence":["tests passed"]}</loop-result>`, EndedAt: now()}
	advance(t, p, l)
	p.wg.Wait()
	advance(t, p, l)
	advance(t, p, l)
	if l.State != "completed" || f.sends != 1 || f.judgeCalls != 1 {
		t.Fatalf("state=%s sends=%d judges=%d", l.State, f.sends, f.judgeCalls)
	}
}
func TestUnknownOldTurnPausesWithoutRetry(t *testing.T) {
	p, f, l := fixture(t)
	advance(t, p, l)
	f.states[iterationKey(l.ID, 1)] = dispatchStatus{Status: "unknown"}
	advance(t, p, l)
	advance(t, p, l)
	if l.State != "paused" || f.sends != 1 || !strings.Contains(l.Reason, "状态不明") {
		t.Fatalf("%+v sends=%d", l, f.sends)
	}
}
func TestJudgeFailuresDoNotExecuteAgentAgain(t *testing.T) {
	p, f, l := fixture(t)
	advance(t, p, l)
	f.states[iterationKey(l.ID, 1)] = dispatchStatus{Status: "completed", StopReason: "end_turn", Output: "missing result"}
	for n := 0; n < 4; n++ {
		advance(t, p, l)
		p.wg.Wait()
	}
	p.db.First(l, "id = ?", l.ID)
	if l.State != "paused" || f.sends != 1 || f.judgeCalls != 3 {
		t.Fatalf("state=%s sends=%d judges=%d", l.State, f.sends, f.judgeCalls)
	}
}
func TestResultParserAndBoundedCheckpoint(t *testing.T) {
	for _, s := range []string{"tool output [GOAL-COMPLETE]", `<loop-result>{"status":"complete"}</loop-result> followed by text`, `<loop-result>{"status":"nonsense"}</loop-result>`} {
		if _, err := parseResult(s); err == nil {
			t.Fatal("accepted", s)
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "progress.md")
	if err := os.WriteFile(path, []byte("iteration: 2\nverified work"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCheckpoint(path, 2); err != "" {
		t.Fatal(err)
	}
	if _, err := readCheckpoint(path, 3); err == "" {
		t.Fatal("accepted stale checkpoint")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 10000)), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := readCheckpoint(path, 2)
	if err == "" || len(s) > 8192 {
		t.Fatalf("len=%d error=%s", len(s), err)
	}
}

func TestPauseDuringJudgeCannotApplyStaleCompletion(t *testing.T) {
	p, f, l := fixture(t)
	advance(t, p, l)
	var i Iteration
	p.db.First(&i, "id = ?", iterationKey(l.ID, 1))
	f.gate.Paused = true
	f.gate.Feedback = "changed acceptance criteria"
	p.applyVerdict(l, &i, verdict{Status: "complete", Reason: "old criteria passed"})
	if l.State != "paused" || i.Verdict == "complete" || l.UserFeedback != f.gate.Feedback {
		t.Fatalf("loop=%+v iteration=%+v", l, i)
	}
}

func TestPersistedPendingIterationSurvivesSchedulerReplacement(t *testing.T) {
	p, f, l := fixture(t)
	f.timeoutAfterAccept = true
	advance(t, p, l)
	replacement := &Plugin{db: p.db, ctx: p.ctx, call: f.call, owner: "new", fence: 2, leaseUntil: now() + 60000, judging: map[string]bool{}, wake: make(chan struct{}, 1)}
	advance(t, replacement, l)
	if f.sends != 1 || l.Iteration != 1 {
		t.Fatalf("replayed: sends=%d iteration=%d", f.sends, l.Iteration)
	}
}

func TestResumedGateDoesNotValidateOlderJudgeInput(t *testing.T) {
	p, f, l := fixture(t)
	advance(t, p, l)
	var i Iteration
	p.db.First(&i, "id = ?", iterationKey(l.ID, 1))
	i.GateRevision = 1
	f.gate.Revision = 3
	f.gate.Paused = false
	f.gate.Feedback = "new instructions after resume"
	p.applyVerdict(l, &i, verdict{Status: "complete", Reason: "old input"})
	if l.State != "paused" || i.Verdict == "complete" {
		t.Fatalf("stale judge accepted: %+v", l)
	}
}

func TestMinIntervalDelaysNextIteration(t *testing.T) {
	p, f, l := fixture(t)
	l.MinIntervalSeconds = 600
	p.db.Save(l)
	advance(t, p, l)
	f.states[iterationKey(l.ID, 1)] = dispatchStatus{Status: "completed", StopReason: "end_turn", Output: `<loop-result>{"status":"continue","summary":"step","next_step":"next"}</loop-result>`, EndedAt: now()}
	advance(t, p, l)
	advance(t, p, l)
	if l.Iteration != 1 || f.sends != 1 {
		t.Fatalf("dispatched inside min interval: iteration=%d sends=%d", l.Iteration, f.sends)
	}
	p.db.Model(&Iteration{}).Where("id = ?", iterationKey(l.ID, 1)).Update("created_at", now()-601000)
	advance(t, p, l)
	if l.Iteration != 2 || f.sends != 2 {
		t.Fatalf("interval elapsed but not dispatched: iteration=%d sends=%d", l.Iteration, f.sends)
	}
}

func TestManualQueueCancellationPausesRatherThanRequeues(t *testing.T) {
	p, f, l := fixture(t)
	advance(t, p, l)
	f.states[iterationKey(l.ID, 1)] = dispatchStatus{Status: "cancelled"}
	advance(t, p, l)
	advance(t, p, l)
	if l.State != "paused" || f.sends != 1 {
		t.Fatalf("state=%s sends=%d", l.State, f.sends)
	}
}
