package main

import "testing"

func TestSnapshotElementFrameBinding(t *testing.T) {
	tab := &chromeTab{id: "tab_one", frames: map[string]*frameContext{
		"main":  {FrameID: "main", ContextID: 1},
		"child": {FrameID: "child", ParentFrameID: "main", ContextID: 2},
	}}
	page := &chromePage{tabs: map[string]*chromeTab{"tab_one": tab}, activeTabID: "tab_one"}
	page.currentSnapshot = &pageSnapshot{ID: "snap_current", TabID: "tab_one", Elements: map[string]string{"e1": "child"}, Contexts: map[string]int64{"child": 2}}
	payload := map[string]any{"snapshot_id": "snap_current", "element_id": "e1"}
	if _, frameID, err := page.snapshotElement(payload); err != nil || frameID != "child" {
		t.Fatalf("valid binding = %q, %v", frameID, err)
	}
	for _, test := range []struct {
		name     string
		mutate   func()
		restore  func()
		expected string
	}{
		{"wrong snapshot", func() { payload["snapshot_id"] = "snap_old" }, func() { payload["snapshot_id"] = "snap_current" }, "stale_snapshot"},
		{"unknown element", func() { payload["element_id"] = "e99" }, func() { payload["element_id"] = "e1" }, "stale_element"},
		{"navigated context", func() { tab.frames["child"].ContextID = 3 }, func() { tab.frames["child"].ContextID = 2 }, "stale_frame"},
		{"closed frame", func() { tab.frames["child"].ContextID = 0 }, func() { tab.frames["child"].ContextID = 2 }, "stale_frame"},
		{"changed tab", func() { page.activeTabID = "tab_other" }, func() { page.activeTabID = "tab_one" }, "active_tab_changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.mutate()
			defer test.restore()
			_, _, err := page.snapshotElement(payload)
			if err == nil {
				t.Fatal("invalid binding unexpectedly succeeded")
			}
			requireErrorCode(t, err, test.expected)
		})
	}
}

func TestFrameByIDNeverFallsBackToMain(t *testing.T) {
	tab := &chromeTab{frames: map[string]*frameContext{"main": {FrameID: "main", ContextID: 1}}}
	page := &chromePage{}
	if frame, err := page.frameByID(tab, ""); err != nil || frame.FrameID != "main" {
		t.Fatalf("main frame = %#v, %v", frame, err)
	}
	if _, err := page.frameByID(tab, "missing"); err == nil {
		t.Fatal("missing child frame silently used main frame")
	} else {
		requireErrorCode(t, err, "stale_frame")
	}
}
