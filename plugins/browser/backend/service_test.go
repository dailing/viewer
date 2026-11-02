package main

import (
	"context"
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
}

func (d *fakeDriver) Execute(_ context.Context, action string, payload map[string]any) (any, error) {
	d.mu.Lock()
	d.actions = append(d.actions, action)
	d.mu.Unlock()
	if action == "navigate" {
		d.url = stringField(payload, "url")
		return map[string]any{"navigated": true, "url": d.url, "title": d.title}, nil
	}
	return map[string]any{"snapshot_id": "snap_test", "url": d.url, "title": d.title, "elements": []any{}}, nil
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
