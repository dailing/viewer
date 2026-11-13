package chat

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestHistoryPagesUseTimestampAndIDCursor(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if err := s.createTask(&Task{ID: "task-a", ChatID: "chat-a", Title: "history", TitleSource: "manual", CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	for _, message := range []Message{
		{ID: "m-a", ChatID: "chat-a", TaskID: "task-a", Role: "assistant", CreatedAt: 100},
		{ID: "m-b", ChatID: "chat-a", TaskID: "task-a", Role: "assistant", CreatedAt: 100},
		{ID: "m-c", ChatID: "chat-a", TaskID: "task-a", Role: "assistant", CreatedAt: 100},
		{ID: "m-d", ChatID: "chat-a", TaskID: "task-a", Role: "assistant", CreatedAt: 200},
		{ID: "m-e", ChatID: "chat-a", TaskID: "task-a", Role: "assistant", CreatedAt: 300},
		{ID: "m-f", ChatID: "chat-a", TaskID: "task-a", Role: "assistant", CreatedAt: 400},
	} {
		if err := s.addMessage(&message); err != nil {
			t.Fatal(err)
		}
	}

	assertPage := func(name string, got []Message, hasMore bool, wantIDs []string, wantMore bool) {
		t.Helper()
		ids := make([]string, 0, len(got))
		for _, message := range got {
			ids = append(ids, message.ID)
		}
		if !reflect.DeepEqual(ids, wantIDs) || hasMore != wantMore {
			t.Fatalf("%s ids=%v hasMore=%v, want ids=%v hasMore=%v", name, ids, hasMore, wantIDs, wantMore)
		}
	}

	latest, more, err := s.historyPage("task-a", 0, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPage("latest", latest, more, []string{"m-e", "m-f"}, true)

	older, more, err := s.historyPage("task-a", 400, "m-f", 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPage("older", older, more, []string{"m-d", "m-e"}, true)

	older, more, err = s.historyPage("task-a", 200, "m-d", 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPage("same timestamp", older, more, []string{"m-b", "m-c"}, true)

	older, more, err = s.historyPage("task-a", 100, "m-b", 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPage("oldest", older, more, []string{"m-a"}, false)

	newer, moreAfter, err := s.historyPageAfter("task-a", 100, "m-b", 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPage("newer with same timestamp", newer, moreAfter, []string{"m-c", "m-d"}, true)

	newer, moreAfter, err = s.historyPageAfter("task-a", 200, "m-d", 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPage("newest", newer, moreAfter, []string{"m-e", "m-f"}, false)
}

func TestHistoryAroundUsesAnchorAndReportsBothDirections(t *testing.T) {
	s, err := openStorePath(filepath.Join(t.TempDir(), "chat.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if err := s.createTask(&Task{ID: "task-a", ChatID: "chat-a", Title: "history", TitleSource: "manual", CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	for _, message := range []Message{
		{ID: "m-a", ChatID: "chat-a", TaskID: "task-a", Role: "assistant", CreatedAt: 100},
		{ID: "m-b", ChatID: "chat-a", TaskID: "task-a", Role: "assistant", CreatedAt: 100},
		{ID: "m-c", ChatID: "chat-a", TaskID: "task-a", Role: "assistant", CreatedAt: 100},
		{ID: "m-d", ChatID: "chat-a", TaskID: "task-a", Role: "assistant", CreatedAt: 200},
		{ID: "m-e", ChatID: "chat-a", TaskID: "task-a", Role: "assistant", CreatedAt: 300},
	} {
		if err := s.addMessage(&message); err != nil {
			t.Fatal(err)
		}
	}

	messages, hasOlder, hasNewer, err := s.historyAround("task-a", 100, "m-b", 4)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	if want := []string{"m-a", "m-b", "m-c", "m-d"}; !reflect.DeepEqual(ids, want) || hasOlder || !hasNewer {
		t.Fatalf("around ids=%v hasOlder=%v hasNewer=%v, want ids=%v hasOlder=false hasNewer=true", ids, hasOlder, hasNewer, want)
	}
}
