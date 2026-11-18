package chat

import "testing"

func TestBackgroundTurnSnapshotAndReleaseIdentity(t *testing.T) {
	keyA := "chat-a\x00role-a\x00task-a"
	keyOther := "chat-a\x00role-b\x00task-b"
	blockA := &backgroundBlock{
		chatID: "chat-a", taskID: "task-a", turnID: "turn-a",
		role: SuperRole{ID: "role-a", Name: "Utility"}, sessionID: "session-a", since: 123,
		processes: []backgroundProcess{{SessionID: "process-a", Command: "sleep 30", PID: 42}},
	}
	p := &Plugin{background: map[string]*backgroundBlock{
		keyA:     blockA,
		keyOther: {chatID: "chat-a", taskID: "task-b", turnID: "turn-b", role: SuperRole{ID: "role-b"}},
	}}

	snapshot := p.backgroundTurns("task-a")
	if len(snapshot) != 1 {
		t.Fatalf("snapshot length=%d, want 1", len(snapshot))
	}
	value := snapshot[0]
	if value["turn_id"] != "turn-a" || value["role_name"] != "Utility" || value["active"] != true {
		t.Fatalf("snapshot identity=%#v", value)
	}
	processes, ok := value["processes"].([]map[string]any)
	if !ok || len(processes) != 1 || processes[0]["command"] != "sleep 30" || processes[0]["pid"] != 42 {
		t.Fatalf("snapshot processes=%#v", value["processes"])
	}

	inactive := backgroundPayload(keyA, blockA, false)
	if inactive["active"] != false || inactive["turn_id"] != "turn-a" || inactive["role_id"] != "role-a" {
		t.Fatalf("inactive payload lost source identity=%#v", inactive)
	}
}

func TestBackgroundProcessesChanged(t *testing.T) {
	first := []backgroundProcess{{SessionID: "process-a", Command: "sleep 30", PID: 42}}
	same := []backgroundProcess{{SessionID: "process-a", Command: "sleep 30", PID: 42}}
	if backgroundProcessesChanged(first, same) {
		t.Fatal("identical process snapshots must not publish an update")
	}
	changed := []backgroundProcess{{SessionID: "process-a", Command: "sleep 31", PID: 42}}
	if !backgroundProcessesChanged(first, changed) {
		t.Fatal("changed process details must publish an update")
	}
	if !backgroundProcessesChanged(first, nil) {
		t.Fatal("completion must be detected as a process-list change")
	}
}
