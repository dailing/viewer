package chat

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type dispatchTestResult struct {
	reply map[string]any
	user  *Message
	err   error
}

func newDispatchTestPlugin(t *testing.T, busy bool) (*Plugin, *Chat, *Task, Workspace) {
	t.Helper()
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.close() })
	now := nowMillis()
	chat := &Chat{ID: "chat-a", Name: "Project", Type: "project", MemberRoleIDsJSON: `["role-a"]`, CreatedAt: now, UpdatedAt: now}
	if err := s.saveChat(chat); err != nil {
		t.Fatal(err)
	}
	task := &Task{ID: "task-a", ChatID: chat.ID, Title: "Task", TitleSource: "manual", CreatedAt: now, UpdatedAt: now}
	if err := s.createTask(task); err != nil {
		t.Fatal(err)
	}
	role := SuperRole{ID: "role-a", Name: "utility", Description: " miscellaneous work "}
	workspace := Workspace{Roles: []SuperRole{role}}
	key := lineKey(chat.ID, task.ID)
	p := &Plugin{store: s, ctx: context.Background(), busy: map[string]bool{}, queues: map[string][]queuedMessage{},
		routeQueues: map[string][]pendingRoute{}, routeRunning: map[string]bool{}, lineRaw: map[string]bool{}}
	p.busy[key] = busy
	return p, chat, task, workspace
}

func dispatchTestRequest(taskID, id, message string) dispatchRequest {
	return dispatchRequest{TaskID: taskID, Message: message, IdempotencyKey: id, DispatchID: id, SourcePlugin: "gateway"}
}

func waitDispatchTest(t *testing.T, results <-chan dispatchTestResult) dispatchTestResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch did not acknowledge while Auto Route was blocked")
		return dispatchTestResult{}
	}
}

func waitPluginWork(t *testing.T, p *Plugin) {
	t.Helper()
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("background dispatch workers did not finish")
	}
}

func TestAutoRouteAcknowledgesBeforeLLMAndPreservesTaskOrder(t *testing.T) {
	p, chat, task, workspace := newDispatchTestPlugin(t, true)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	routeMessages := make(chan string, 2)
	var routeMu sync.Mutex
	calls := 0
	p.routeLLMFn = func(_ context.Context, messages []map[string]string, _ bool, _ int, _ string, _ map[string]any) (completionResult, error) {
		routeMu.Lock()
		calls++
		call := calls
		routeMu.Unlock()
		prompt := messages[len(messages)-1]["content"]
		label := "first"
		if strings.Contains(prompt, "second message") {
			label = "second"
		}
		routeMessages <- label
		if call == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		return completionResult{Content: `{"role_ids":["role-a"],"rationale":"test"}`}, nil
	}

	firstResult := make(chan dispatchTestResult, 1)
	go func() {
		reply, user, err := p.dispatchMessage(chat, workspace, dispatchTestRequest(task.ID, "dispatch-first", "first message"), map[string]any{})
		firstResult <- dispatchTestResult{reply: reply, user: user, err: err}
	}()
	first := waitDispatchTest(t, firstResult)
	if first.err != nil || first.reply["status"] != "routing" || first.user == nil {
		t.Fatalf("first admission = %#v user=%#v err=%v", first.reply, first.user, first.err)
	}
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first Auto Route did not start")
	}
	storedFirst, err := p.store.message(first.user.ID)
	if err != nil || storedFirst == nil {
		t.Fatalf("first user message was not durable before routing: %#v err=%v", storedFirst, err)
	}

	secondResult := make(chan dispatchTestResult, 1)
	go func() {
		reply, user, err := p.dispatchMessage(chat, workspace, dispatchTestRequest(task.ID, "dispatch-second", "second message"), map[string]any{})
		secondResult <- dispatchTestResult{reply: reply, user: user, err: err}
	}()
	second := waitDispatchTest(t, secondResult)
	if second.err != nil || second.reply["status"] != "routing" || second.user == nil {
		t.Fatalf("second admission = %#v user=%#v err=%v", second.reply, second.user, second.err)
	}
	close(releaseFirst)
	waitPluginWork(t, p)

	for _, want := range []string{"first", "second"} {
		select {
		case got := <-routeMessages:
			if got != want {
				t.Fatalf("route order = %s, want %s", got, want)
			}
		default:
			t.Fatalf("missing %s route call", want)
		}
	}
	key := lineKey(chat.ID, task.ID)
	p.mu.Lock()
	queued := append([]queuedMessage(nil), p.queues[key]...)
	p.mu.Unlock()
	if len(queued) != 2 || queued[0].message != "first message" || queued[1].message != "second message" {
		t.Fatalf("Task queue order = %#v", queued)
	}
}

func TestDecisionResponseUsesAsyncAutoRoute(t *testing.T) {
	p, chat, task, workspace := newDispatchTestPlugin(t, true)
	workspace.Roles = append(workspace.Roles, SuperRole{ID: "role-b", Name: "researcher", Description: "research work"})
	chat.MemberRoleIDsJSON = `["role-a","role-b"]`
	now := nowMillis()
	decision := &Decision{ID: "decision-route", ChatID: chat.ID, TaskID: task.ID,
		SourceTurnID: "missing-source-turn", RoleID: "role-a", Version: 1, Kind: "choice",
		OptionsJSON: "[]", Status: "pending", CreatedAt: now, UpdatedAt: now}
	if err := p.store.db.Create(decision).Error; err != nil {
		t.Fatal(err)
	}
	routeStarted := make(chan struct{})
	releaseRoute := make(chan struct{})
	p.routeLLMFn = func(_ context.Context, messages []map[string]string, _ bool, _ int, _ string, _ map[string]any) (completionResult, error) {
		if prompt := messages[len(messages)-1]["content"]; !strings.Contains(prompt, "switch roles") {
			t.Fatalf("Decision response missing from router prompt: %s", prompt)
		}
		close(routeStarted)
		<-releaseRoute
		return completionResult{Content: `{"role_ids":["role-b"],"rationale":"router selected a new role"}`}, nil
	}
	request := dispatchTestRequest(task.ID, "decision:decision-route", "switch roles")
	request.DecisionID = decision.ID
	request.SourcePlugin = "chat"
	resultChannel := make(chan dispatchTestResult, 1)
	go func() {
		reply, user, err := p.dispatchMessage(chat, workspace, request, map[string]any{"decision_id": decision.ID})
		resultChannel <- dispatchTestResult{reply: reply, user: user, err: err}
	}()
	result := waitDispatchTest(t, resultChannel)
	if result.err != nil || result.reply["status"] != "routing" || len(result.reply["role_ids"].([]string)) != 0 {
		t.Fatalf("Decision admission = %#v err=%v", result.reply, result.err)
	}
	select {
	case <-routeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Decision response did not enter Auto Route")
	}
	resolved, err := p.decision(decision.ID)
	if err != nil || resolved.Status != "resolved" || resolved.ResolvedDispatchID != request.DispatchID {
		t.Fatalf("Decision lifecycle = %#v err=%v", resolved, err)
	}
	close(releaseRoute)
	waitPluginWork(t, p)
	key := lineKey(chat.ID, task.ID)
	p.mu.Lock()
	queued := append([]queuedMessage(nil), p.queues[key]...)
	p.mu.Unlock()
	if len(queued) != 1 || len(queued[0].targets) != 1 || queued[0].targets[0].role.ID != "role-b" {
		t.Fatalf("routed Decision queue = %#v", queued)
	}
}

func TestAutoRouteFailureIsPersistentAndVisible(t *testing.T) {
	p, chat, task, workspace := newDispatchTestPlugin(t, true)
	p.routeLLMFn = func(context.Context, []map[string]string, bool, int, string, map[string]any) (completionResult, error) {
		return completionResult{}, errors.New("router unavailable")
	}
	request := dispatchTestRequest(task.ID, "dispatch-failure", "message needing a route")
	if err := p.startOperation(request.SourcePlugin, "dispatch", request.IdempotencyKey, request, chat.ID, task.ID, request.DispatchID); err != nil {
		t.Fatal(err)
	}
	reply, user, err := p.dispatchMessage(chat, workspace, request, map[string]any{})
	if err != nil || reply["status"] != "routing" {
		t.Fatalf("admission reply=%#v err=%v", reply, err)
	}
	waitPluginWork(t, p)
	var receipt OperationReceipt
	if err := p.store.db.Where("result_id = ?", request.DispatchID).First(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "failed" || !strings.Contains(receipt.Failure, "router unavailable") {
		t.Fatalf("route receipt = %#v", receipt)
	}
	var block MessageBlock
	if err := p.store.db.Where("turn_id = ? AND kind = ?", request.DispatchID, "error").First(&block).Error; err != nil {
		t.Fatal(err)
	}
	if block.TaskID != task.ID || block.TurnID != user.TurnID || !strings.Contains(block.Text, "router unavailable") {
		t.Fatalf("route error block = %#v", block)
	}
}

func TestRecoverInterruptedAutoRouteMarksReceiptAndTimelineFailed(t *testing.T) {
	p, chat, task, _ := newDispatchTestPlugin(t, false)
	user := &Message{ID: "message-a", ChatID: chat.ID, TaskID: task.ID, TurnID: "dispatch-a", Role: "user", Text: "kept", CreatedAt: nowMillis()}
	if err := p.store.addMessage(user); err != nil {
		t.Fatal(err)
	}
	receipt := &OperationReceipt{CallerPlugin: "gateway", Operation: "dispatch", IdempotencyKey: "input:1", ResultID: user.TurnID,
		ResultJSON: fmt.Sprintf(`{"status":"routing","dispatch_id":%q,"message_id":%q,"task_id":%q}`, user.TurnID, user.ID, task.ID),
		ChatID:     chat.ID, TaskID: task.ID, Status: "routing", CreatedAt: nowMillis()}
	if err := p.store.db.Create(receipt).Error; err != nil {
		t.Fatal(err)
	}
	if err := p.recoverTaskWork(); err != nil {
		t.Fatal(err)
	}
	var stored OperationReceipt
	if err := p.store.db.Where("result_id = ?", user.TurnID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" || !strings.Contains(stored.ResultJSON, `"failed"`) {
		t.Fatalf("recovered receipt = %#v", stored)
	}
	var block MessageBlock
	if err := p.store.db.Where("turn_id = ? AND kind = ?", user.TurnID, "error").First(&block).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block.Text, "interrupted by chat restart") {
		t.Fatalf("recovery block = %#v", block)
	}
}

func TestAutoRouteUsesFastKimiWithoutReasoning(t *testing.T) {
	called := false
	complete := func(_ context.Context, _ []map[string]string, jsonMode bool, timeout int, model string, extra map[string]any) (completionResult, error) {
		called = true
		if !jsonMode || timeout != 15 || model != "k2.8" {
			t.Fatalf("route options json=%v timeout=%d model=%q", jsonMode, timeout, model)
		}
		if extra["reasoning_effort"] != "none" || extra["temperature"] != 0.6 {
			t.Fatalf("route extra_body = %#v", extra)
		}
		return completionResult{Content: `{"role_ids":["role-a"],"rationale":"fast"}`}, nil
	}
	roles := []SuperRole{{ID: "role-a", Name: "utility", Description: "miscellaneous"}}
	ids, _, err := routeWithLLM(context.Background(), complete, "route this", roles, "", defaultDispatchRouterConfig())
	if err != nil || !called || len(ids) != 1 || ids[0] != "role-a" {
		t.Fatalf("route ids=%v called=%v err=%v", ids, called, err)
	}
}
