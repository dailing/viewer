package chat

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAssociateDecisionOrdinaryReplies(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	p := &Plugin{store: s}
	now := nowMillis()
	task := &Task{ID: "task-a", ChatID: "chat-a", Title: "Task", CreatedAt: now, UpdatedAt: now}
	if err := s.createTask(task); err != nil {
		t.Fatal(err)
	}
	decision := &Decision{ID: "decision-a", ChatID: "chat-a", TaskID: task.ID, SourceTurnID: "turn-a", SourceMessageID: "message-a", RoleID: "role-a", Version: 1, Kind: "choice", OptionsJSON: "[]", Status: "pending", CreatedAt: now, UpdatedAt: now}
	if err := s.db.Create(decision).Error; err != nil {
		t.Fatal(err)
	}
	request := dispatchRequest{Message: "ordinary reply", SourcePlugin: "gateway", ForceNewSession: true}
	claimed, err := p.associateDecision(task.ID, &request)
	if err != nil || !claimed {
		t.Fatalf("associate claimed=%v err=%v", claimed, err)
	}
	if request.DecisionID != decision.ID || request.ContinueTurnID != "" || len(request.RoleIDs) != 0 || !request.ForceNewSession {
		t.Fatalf("Decision association changed dispatch routing: %#v", request)
	}
	if err := p.finishOrdinaryDecision(decision.ID, "dispatch-a", claimed, nil); err != nil {
		t.Fatal(err)
	}
	resolved, err := p.decision(decision.ID)
	if err != nil || resolved.Status != "resolved" || resolved.Response != "ordinary reply" || resolved.ResolvedDispatchID != "dispatch-a" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}

	for _, item := range []Decision{
		{ID: "decision-b", ChatID: "chat-a", TaskID: task.ID, SourceTurnID: "turn-b", SourceMessageID: "message-b", RoleID: "role-a", Version: 1, Kind: "choice", OptionsJSON: "[]", Status: "pending", CreatedAt: now + 1, UpdatedAt: now + 1},
		{ID: "decision-c", ChatID: "chat-a", TaskID: task.ID, SourceTurnID: "turn-c", SourceMessageID: "message-c", RoleID: "role-a", Version: 1, Kind: "choice", OptionsJSON: "[]", Status: "pending", CreatedAt: now + 2, UpdatedAt: now + 2},
	} {
		item := item
		if err := s.db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}
	ambiguous := dispatchRequest{Message: "do not guess", SourcePlugin: "gateway"}
	claimed, err = p.associateDecision(task.ID, &ambiguous)
	if err != nil || claimed || ambiguous.DecisionID != "" {
		t.Fatalf("ambiguous reply was associated: %#v claimed=%v err=%v", ambiguous, claimed, err)
	}
	explicit := dispatchRequest{DecisionID: "decision-b", Message: "explicit", SourcePlugin: "gateway"}
	claimed, err = p.associateDecision(task.ID, &explicit)
	if err != nil || !claimed {
		t.Fatalf("explicit claimed=%v err=%v", claimed, err)
	}
	if err := p.finishOrdinaryDecision("decision-b", "", claimed, errors.New("admission failed")); err != nil {
		t.Fatal(err)
	}
	pending, err := p.decision("decision-b")
	if err != nil || pending.Status != "pending" {
		t.Fatalf("failed admission did not roll back: %#v err=%v", pending, err)
	}

	inFlight, err := p.decision("decision-c")
	if err != nil {
		t.Fatal(err)
	}
	inFlight.Status = "dispatching"
	if err := s.db.Save(inFlight).Error; err != nil {
		t.Fatal(err)
	}
	external := dispatchRequest{DecisionID: "decision-c", Message: "duplicate", SourcePlugin: "gateway"}
	if _, err := p.associateDecision(task.ID, &external); err == nil {
		t.Fatal("external dispatch entered an already claimed Decision")
	}
	internal := dispatchRequest{DecisionID: "decision-c", Message: "dedicated", SourcePlugin: "chat"}
	claimed, err = p.associateDecision(task.ID, &internal)
	if err != nil || claimed {
		t.Fatalf("dedicated dispatch claimed=%v err=%v", claimed, err)
	}
}

func TestDecisionAlwaysIncludesEndCurrentTaskOption(t *testing.T) {
	marker := parseDecisionMarker("<viewer-decision>\nversion: 1\nkind: choice\nquestion: Continue?\noption: First path\noption: Second path\n</viewer-decision>")
	if marker == nil {
		t.Fatal("ordinary Decision marker was rejected")
	}
	ensureDecisionEndTaskOption(marker)
	options, err := parseDecisionOptions(marker.OptionsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 3 || options[2].ID != decisionEndTaskOptionID || options[2].Label != decisionEndTaskOptionLabel || options[2].Action != "end_current_task" {
		t.Fatalf("built-in Task-ending option mismatch: %#v", options)
	}
	ensureDecisionEndTaskOption(marker)
	options, _ = parseDecisionOptions(marker.OptionsJSON)
	if len(options) != 3 {
		t.Fatalf("built-in Task-ending option was duplicated: %#v", options)
	}

	agentEnded := parseDecisionMarker("<viewer-decision>\nversion: 1\nkind: choice\noption: Continue\noption: End current task\noption_action: end_current_task\n</viewer-decision>")
	ensureDecisionEndTaskOption(agentEnded)
	options, _ = parseDecisionOptions(agentEnded.OptionsJSON)
	if len(options) != 2 || options[1].ID != "option-2" {
		t.Fatalf("Agent-provided Task-ending option was replaced or duplicated: %#v", options)
	}

	legacy := decisionPayload(&Decision{OptionsJSON: `[{"id":"option-1","label":"Continue"}]`, Status: "pending"})
	legacyOptions, ok := legacy["options"].([]decisionOption)
	if !ok || len(legacyOptions) != 2 || legacyOptions[1].ID != decisionEndTaskOptionID {
		t.Fatalf("existing pending Decision did not gain the built-in option: %#v", legacy["options"])
	}
}

func TestEndCurrentTaskDecisionResolvesWithoutDispatch(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	now := nowMillis()
	task := &Task{ID: "task-end", ChatID: "chat-a", Title: "Task", CreatedAt: now, UpdatedAt: now}
	if err := s.createTask(task); err != nil {
		t.Fatal(err)
	}
	marker := parseDecisionMarker("<viewer-decision>\nversion: 1\nkind: choice\noption: End current task\noption_action: end_current_task\n</viewer-decision>")
	if marker == nil {
		t.Fatal("Task-ending marker was rejected")
	}
	decision := &Decision{ID: "decision-end", ChatID: task.ChatID, TaskID: task.ID, SourceTurnID: "turn-end", RoleID: "role-a", Version: 1, Kind: "choice", OptionsJSON: marker.OptionsJSON, Status: "pending", CreatedAt: now, UpdatedAt: now}
	other := &Decision{ID: "decision-other", ChatID: task.ChatID, TaskID: task.ID, SourceTurnID: "turn-other", RoleID: "role-a", Version: 1, Kind: "choice", OptionsJSON: "[]", Status: "pending", CreatedAt: now, UpdatedAt: now}
	if err := s.db.Create(decision).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(other).Error; err != nil {
		t.Fatal(err)
	}
	options, err := parseDecisionOptions(decision.OptionsJSON)
	if err != nil || len(options) != 1 {
		t.Fatalf("options=%#v err=%v", options, err)
	}
	p := &Plugin{store: s}
	cancelled, err := p.finishTaskEnd(task, decision, &options[0])
	if err != nil || len(cancelled) != 1 || cancelled[0].ID != other.ID {
		t.Fatalf("cancelled=%#v err=%v", cancelled, err)
	}
	storedTask, err := s.task(task.ID)
	if err != nil || storedTask == nil || !storedTask.ended() {
		t.Fatalf("Task was not ended: %#v err=%v", storedTask, err)
	}
	stored, err := p.decision(decision.ID)
	if err != nil || stored.Status != "resolved" || stored.Response != "End current task" || stored.ResolvedDispatchID != "" || stored.Version != 2 {
		t.Fatalf("resolving Decision=%#v err=%v", stored, err)
	}
	storedOther, err := p.decision(other.ID)
	if err != nil || storedOther.Status != "cancelled" {
		t.Fatalf("other Decision=%#v err=%v", storedOther, err)
	}
	var summary TaskSummary
	if err := s.db.Limit(1).Find(&summary, "task_id = ?", task.ID).Error; err != nil || !summary.Final || summary.Status != "pending" {
		t.Fatalf("final summary=%#v err=%v", summary, err)
	}
}
