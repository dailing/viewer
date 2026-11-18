package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeBus struct{}

func (b *fakeBus) Publish(context.Context, string, any) error { return nil }
func (b *fakeBus) Set(context.Context, string, any) error     { return nil }

type fakeDriver struct {
	mu       sync.Mutex
	url      string
	title    string
	viewport Viewport
	inputs   []map[string]any
	actions  []string
	closed   bool
	human    map[string]any
	tabs     []map[string]any
}

func (d *fakeDriver) tabResultLocked(action, tabID string) map[string]any {
	public := make([]map[string]any, 0, len(d.tabs))
	stored := make([]storedTab, 0, len(d.tabs))
	active := 0
	for index, tab := range d.tabs {
		item := map[string]any{"tab_id": tab["tab_id"], "url": tab["url"], "title": tab["title"], "active": tab["active"]}
		public = append(public, item)
		stored = append(stored, storedTab{URL: stringField(tab, "url"), Title: stringField(tab, "title")})
		if tab["active"] == true {
			active = index
		}
	}
	return map[string]any{"action": action, "tab_id": tabID, "tabs": public, "tab_state": map[string]any{"tabs": stored, "active_tab": active}}
}

func (d *fakeDriver) executeTabLocked(action string, payload map[string]any) (map[string]any, error) {
	if d.tabs == nil {
		d.tabs = []map[string]any{{"tab_id": "tab_1", "url": d.url, "title": d.title, "active": true}}
	}
	activeIndex := 0
	for index, tab := range d.tabs {
		if tab["active"] == true {
			activeIndex = index
		}
	}
	switch action {
	case "list_tabs":
		return d.tabResultLocked("listed", stringField(d.tabs[activeIndex], "tab_id")), nil
	case "new_tab":
		for _, tab := range d.tabs {
			tab["active"] = false
		}
		rawURL := stringField(payload, "url")
		if rawURL == "" {
			rawURL = "about:blank"
		}
		tabID := fmt.Sprintf("tab_%d", len(d.tabs)+1)
		d.tabs = append(d.tabs, map[string]any{"tab_id": tabID, "url": rawURL, "title": "Fake " + tabID, "active": true})
		d.url, d.title = rawURL, "Fake "+tabID
		return d.tabResultLocked("created", tabID), nil
	case "switch_tab":
		tabID := stringField(payload, "tab_id")
		for index, tab := range d.tabs {
			tab["active"] = tab["tab_id"] == tabID
			if tab["active"] == true {
				activeIndex = index
			}
		}
		if d.tabs[activeIndex]["tab_id"] != tabID {
			return nil, errorf("tab_not_found", "unknown fake tab")
		}
		d.url, d.title = stringField(d.tabs[activeIndex], "url"), stringField(d.tabs[activeIndex], "title")
		return d.tabResultLocked("switched", tabID), nil
	case "close_tab":
		if len(d.tabs) == 1 {
			return nil, errorf("last_tab", "close the Page explicitly")
		}
		tabID := stringField(payload, "tab_id")
		if tabID == "" {
			tabID = stringField(d.tabs[activeIndex], "tab_id")
		}
		closeIndex := -1
		for index, tab := range d.tabs {
			if tab["tab_id"] == tabID {
				closeIndex = index
			}
		}
		if closeIndex < 0 {
			return nil, errorf("tab_not_found", "unknown fake tab")
		}
		wasActive := d.tabs[closeIndex]["active"] == true
		d.tabs = append(d.tabs[:closeIndex], d.tabs[closeIndex+1:]...)
		if wasActive {
			activeIndex = min(closeIndex, len(d.tabs)-1)
			d.tabs[activeIndex]["active"] = true
		}
		for index, tab := range d.tabs {
			if tab["active"] == true {
				activeIndex = index
			}
		}
		d.url, d.title = stringField(d.tabs[activeIndex], "url"), stringField(d.tabs[activeIndex], "title")
		return d.tabResultLocked("closed", tabID), nil
	}
	return nil, errorf("invalid_request", "unknown fake tab action")
}

func (d *fakeDriver) Execute(_ context.Context, action string, payload map[string]any) (any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.human != nil {
		return nil, errorf("human_required", "%s", stringField(d.human, "detail"))
	}
	d.actions = append(d.actions, action)
	switch action {
	case "list_tabs", "new_tab", "switch_tab", "close_tab":
		return d.executeTabLocked(action, payload)
	}
	if action == "navigate" {
		d.url = stringField(payload, "url")
		return map[string]any{"navigated": true, "url": d.url, "title": d.title}, nil
	}
	if action == "screenshot" {
		return map[string]any{"data": "aGVsbG8=", "mime_type": "image/png", "path": "/tmp/screenshot.png", "width": 1, "height": 1}, nil
	}
	return map[string]any{"snapshot_id": "snap_test", "url": d.url, "title": d.title, "elements": []any{}}, nil
}
func (d *fakeDriver) HumanRequired() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.human
}
func (d *fakeDriver) Input(_ context.Context, event map[string]any) error {
	d.mu.Lock()
	d.inputs = append(d.inputs, event)
	d.mu.Unlock()
	return nil
}
func (d *fakeDriver) SetViewport(_ context.Context, viewport Viewport) error {
	d.viewport = viewport
	return nil
}
func (d *fakeDriver) Navigate(_ context.Context, rawURL string) error  { d.url = rawURL; return nil }
func (d *fakeDriver) Metadata(context.Context) (string, string, error) { return d.url, d.title, nil }
func (d *fakeDriver) StreamTarget() (string, uint32)                   { return ":0", 123 }
func (d *fakeDriver) Close(context.Context) error                      { d.closed = true; return nil }

type fakeBackend struct{ drivers map[string]*fakeDriver }

func (b *fakeBackend) OpenPage(_ context.Context, pageID, rawURL string, viewport Viewport) (PageDriver, error) {
	if rawURL == "" {
		rawURL = "about:blank"
	}
	driver := &fakeDriver{url: rawURL, title: "Fake " + pageID, viewport: viewport}
	if b.drivers == nil {
		b.drivers = make(map[string]*fakeDriver)
	}
	b.drivers[pageID] = driver
	return driver, nil
}

type fakeStream struct {
	viewport Viewport
	video    VideoSettings
	ensures  int
	stops    int
}

func (s *fakeStream) Ensure(_ context.Context, _ string, _ uint32, viewport Viewport, video VideoSettings) error {
	s.viewport, s.video = viewport, video
	s.ensures++
	return nil
}
func (s *fakeStream) Ticket(context.Context) (map[string]any, error) {
	return map[string]any{"stream_id": "st_fake", "ticket": "ticket_fake"}, nil
}
func (s *fakeStream) Stop() { s.stops++ }

func newTestService(t *testing.T) (*Service, *fakeBackend, map[string]*fakeStream, string) {
	t.Helper()
	return newTestServiceAt(t, filepath.Join(t.TempDir(), "pages.json"))
}

func newTestServiceAt(t *testing.T, stateFile string) (*Service, *fakeBackend, map[string]*fakeStream, string) {
	t.Helper()
	backend := &fakeBackend{}
	streams := make(map[string]*fakeStream)
	service, err := NewService(&fakeBus{}, stateFile, backend, func(pageID string) StreamController {
		stream := &fakeStream{}
		streams[pageID] = stream
		return stream
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.CreatePage(context.Background(), "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	return service, backend, streams, page["page_id"].(string)
}

func requireErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	browserErr, ok := err.(*Error)
	if !ok || browserErr.Code != code {
		t.Fatalf("error = %#v, want code %s", err, code)
	}
}

func watchTestPage(t *testing.T, service *Service, pageID, viewerID string, value map[string]any) map[string]any {
	t.Helper()
	if value == nil {
		value = map[string]any{"pane_width": 1200, "pane_height": 800, "page_zoom": 1.0, "render_scale": 1.0, "fps": 15, "bitrate_kbps": 4000}
	}
	value["page_id"], value["viewer_id"] = pageID, viewerID
	result, err := service.Watch(context.Background(), pageID, viewerID, value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestHumanRequiredStatusAndPromptFailure(t *testing.T) {
	service, backend, _, pageID := newTestService(t)
	lease, err := service.Require(pageID, "")
	if err != nil {
		t.Fatal(err)
	}
	driver := backend.drivers[pageID]
	driver.mu.Lock()
	driver.human = map[string]any{"kind": "dialog", "detail": "Fixture prompt"}
	driver.mu.Unlock()
	status, err := service.Status(pageID)
	if err != nil {
		t.Fatal(err)
	}
	required, _ := status["human_required"].(map[string]any)
	if stringField(required, "kind") != "dialog" || stringField(required, "detail") != "Fixture prompt" {
		t.Fatalf("human-required status = %#v", status)
	}
	started := time.Now()
	if _, err := service.Execute(context.Background(), pageID, lease["lease_id"].(string), "snapshot", nil, 35*time.Second); err == nil {
		t.Fatal("dialog did not block browser action")
	} else {
		requireErrorCode(t, err, "human_required")
	}
	if time.Since(started) > time.Second {
		t.Fatal("human-required action waited for the action timeout")
	}
	driver.mu.Lock()
	driver.human = nil
	driver.mu.Unlock()
	status, err = service.Status(pageID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := status["human_required"]; exists {
		t.Fatalf("cleared dialog remained in status: %#v", status)
	}
}

func TestPageIsolationAndExclusiveLease(t *testing.T) {
	service, backend, _, idA := newTestService(t)
	pageB, err := service.CreatePage(context.Background(), "https://example.org")
	if err != nil {
		t.Fatal(err)
	}
	idB := pageB["page_id"].(string)
	if idA == idB || len(idA) != 29 {
		t.Fatalf("invalid page ids %q %q", idA, idB)
	}
	leaseA, err := service.Require(idA, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Require(idA, ""); err == nil {
		t.Fatal("second require acquired the same page")
	} else {
		requireErrorCode(t, err, "page_busy")
	}
	if _, err := service.Require(idB, ""); err != nil {
		t.Fatalf("another page should be independent: %v", err)
	}
	if _, err := service.Execute(context.Background(), idA, "wrong", "snapshot", nil, time.Second); err == nil {
		t.Fatal("wrong lease executed")
	} else {
		requireErrorCode(t, err, "invalid_lease")
	}
	if len(backend.drivers[idA].actions) != 0 {
		t.Fatal("invalid lease reached Chrome")
	}
	leaseID := leaseA["lease_id"].(string)
	if err := service.Release(idA, leaseID); err != nil {
		t.Fatal(err)
	}
	newLease, err := service.Require(idA, "")
	if err != nil || newLease["lease_id"] == leaseID {
		t.Fatalf("new lease = %v, %v", newLease, err)
	}
	if err := service.ForceRelease(idA); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(context.Background(), idA, newLease["lease_id"].(string), "snapshot", nil, time.Second); err == nil {
		t.Fatal("fenced lease executed")
	} else {
		requireErrorCode(t, err, "invalid_lease")
	}
}

func TestVideoControlsAreIndependent(t *testing.T) {
	service, backend, streams, pageID := newTestService(t)
	watchTestPage(t, service, pageID, "viewer", map[string]any{
		"pane_width": 1200, "pane_height": 800, "page_zoom": 1.25,
		"render_scale": 1.5, "fps": 30, "bitrate_kbps": 8000,
	})
	stream := streams[pageID]
	if stream.viewport.CSSWidth != 960 || stream.viewport.CSSHeight != 640 || stream.viewport.PixelWidth != 1440 || stream.viewport.PixelHeight != 960 {
		t.Fatalf("unexpected viewport: %+v", stream.viewport)
	}
	if stream.video.FPS != 30 || stream.video.BitrateKbps != 8000 {
		t.Fatalf("unexpected video: %+v", stream.video)
	}
	watchTestPage(t, service, pageID, "viewer", map[string]any{
		"pane_width": 1200, "pane_height": 800, "page_zoom": 1.25,
		"render_scale": 1.5, "fps": 30, "bitrate_kbps": 2000,
	})
	if stream.video.FPS != 30 || stream.video.BitrateKbps != 2000 || stream.viewport.PixelWidth != 1440 {
		t.Fatalf("bitrate changed another parameter: %+v %+v", stream.video, stream.viewport)
	}
	watchTestPage(t, service, pageID, "viewer", map[string]any{
		"pane_width": 1200, "pane_height": 800, "page_zoom": 1.25,
		"render_scale": 0.75, "fps": 5, "bitrate_kbps": 2000,
	})
	if stream.viewport.CSSWidth != 960 || stream.viewport.PixelWidth != 720 || stream.video.FPS != 5 || stream.video.BitrateKbps != 2000 {
		t.Fatalf("scale/fps controls are not independent: %+v %+v", stream.viewport, stream.video)
	}
	if backend.drivers[pageID].viewport.PixelWidth != 720 {
		t.Fatal("viewport did not reach Chrome")
	}
}

func TestClosePaneKeepsPageAndAgentLease(t *testing.T) {
	service, _, streams, pageID := newTestService(t)
	lease, err := service.Require(pageID, "")
	if err != nil {
		t.Fatal(err)
	}
	watchTestPage(t, service, pageID, "viewer", nil)
	service.Unwatch(pageID, "viewer")
	status, err := service.Status(pageID)
	if err != nil || status["online"] != true || status["control_owner"] != "agent" {
		t.Fatalf("page changed after pane close: %+v, %v", status, err)
	}
	if streams[pageID].stops != 1 {
		t.Fatalf("stream stops = %d", streams[pageID].stops)
	}
	if _, err := service.Execute(context.Background(), pageID, lease["lease_id"].(string), "snapshot", nil, time.Second); err != nil {
		t.Fatalf("agent lease did not survive pane close: %v", err)
	}
}

func TestHumanTakeoverFencesAgent(t *testing.T) {
	service, backend, _, pageID := newTestService(t)
	agent, err := service.Require(pageID, "")
	if err != nil {
		t.Fatal(err)
	}
	watchTestPage(t, service, pageID, "human", nil)
	if _, err := service.HumanControl(pageID, "human", false); err == nil {
		t.Fatal("human acquired page without takeover")
	} else {
		requireErrorCode(t, err, "page_busy")
	}
	if _, err := service.HumanControl(pageID, "human", true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(context.Background(), pageID, agent["lease_id"].(string), "snapshot", nil, time.Second); err == nil {
		t.Fatal("old agent lease survived takeover")
	} else {
		requireErrorCode(t, err, "invalid_lease")
	}
	if _, err := service.Require(pageID, ""); err == nil {
		t.Fatal("agent acquired human-controlled page")
	} else {
		requireErrorCode(t, err, "page_busy")
	}
	if err := service.Input(context.Background(), pageID, "human", map[string]any{"type": "insertText", "text": "hello"}); err != nil {
		t.Fatal(err)
	}
	if len(backend.drivers[pageID].inputs) != 1 {
		t.Fatal("human input did not reach Chrome")
	}
	service.Unwatch(pageID, "human")
	if _, err := service.Require(pageID, ""); err != nil {
		t.Fatalf("agent could not acquire after human pane closed: %v", err)
	}
}

func TestHumanTabLifecycleAndPersistence(t *testing.T) {
	service, _, _, pageID := newTestService(t)
	watchTestPage(t, service, pageID, "human", nil)
	listed, err := service.ListTabs(context.Background(), pageID)
	if err != nil || len(listed["tabs"].([]map[string]any)) != 1 {
		t.Fatalf("initial tabs = %#v, %v", listed, err)
	}
	if _, err := service.HumanTab(context.Background(), pageID, "human", "new_tab", map[string]any{}); err == nil {
		t.Fatal("tab action succeeded without human control")
	} else {
		requireErrorCode(t, err, "invalid_lease")
	}
	if _, err := service.HumanControl(pageID, "human", false); err != nil {
		t.Fatal(err)
	}
	created, err := service.HumanTab(context.Background(), pageID, "human", "new_tab", map[string]any{"url": "https://example.org/two"})
	if err != nil || len(created["tabs"].([]map[string]any)) != 2 {
		t.Fatalf("created tabs = %#v, %v", created, err)
	}
	page := service.getPage(pageID)
	if page.URL != "https://example.org/two" || page.ActiveTab != 1 || len(page.Tabs) != 2 {
		t.Fatalf("active tab metadata = %#v", page)
	}
	persisted, err := NewService(&fakeBus{}, service.stateFile, &fakeBackend{}, func(string) StreamController { return &fakeStream{} })
	if err != nil {
		t.Fatal(err)
	}
	stored := persisted.getPage(pageID)
	if len(stored.Tabs) != 2 || stored.ActiveTab != 1 || stored.Tabs[1].URL != "https://example.org/two" {
		t.Fatalf("persisted tabs = %#v", stored)
	}
	if _, err := service.HumanTab(context.Background(), pageID, "human", "switch_tab", map[string]any{"tab_id": "tab_1"}); err != nil {
		t.Fatal(err)
	}
	if page.URL != "https://example.com" || page.ActiveTab != 0 {
		t.Fatalf("switched tab metadata = %#v", page)
	}
	closed, err := service.HumanTab(context.Background(), pageID, "human", "close_tab", map[string]any{"tab_id": "tab_2"})
	if err != nil || len(closed["tabs"].([]map[string]any)) != 1 {
		t.Fatalf("closed tabs = %#v, %v", closed, err)
	}
	if _, err := service.HumanTab(context.Background(), pageID, "human", "close_tab", map[string]any{}); err == nil {
		t.Fatal("last tab unexpectedly closed")
	} else {
		requireErrorCode(t, err, "last_tab")
	}
}

func TestCloseOfflinePageWithoutDriver(t *testing.T) {
	service, _, _, pageID := newTestService(t)
	page := service.getPage(pageID)
	page.Online = false
	page.driver = nil
	if err := service.ClosePage(context.Background(), pageID); err != nil {
		t.Fatal(err)
	}
	if service.getPage(pageID) != nil {
		t.Fatal("offline page was not unregistered")
	}
}

func TestRestoreKeepsRandomPageID(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "pages.json")
	service, _, _, pageID := newTestServiceAt(t, stateFile)
	lease, err := service.Require(pageID, "")
	if err != nil {
		t.Fatal(err)
	}
	if lease["lease_id"] == "" {
		t.Fatal("missing lease")
	}
	restored, err := NewService(&fakeBus{}, stateFile, &fakeBackend{}, func(string) StreamController { return &fakeStream{} })
	if err != nil {
		t.Fatal(err)
	}
	restored.Restore(context.Background())
	status, err := restored.Status(pageID)
	if err != nil || status["online"] != true || status["busy"] != false {
		t.Fatalf("restored status = %+v, %v", status, err)
	}
}
