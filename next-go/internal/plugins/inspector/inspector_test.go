package inspector

import (
	"strings"
	"testing"
	"time"

	"viewer/internal/busclient"
)

func testPlugin(t *testing.T, ringSize, threshold int) *Plugin {
	t.Helper()
	plugin, err := New(Config{KernelWS: "ws://127.0.0.1:29399/ws", RingSize: ringSize, EmitThreshold: threshold})
	if err != nil {
		t.Fatal(err)
	}
	return plugin
}

func TestCaptureRingFilterAndSelfEcho(t *testing.T) {
	plugin := testPlugin(t, 2, 200)
	plugin.filter = map[string]string{"channel": "traffic:*:event", "origin": "producer", "text": "needle"}
	plugin.capture(busclient.Frame{Type: "publish", Channel: "traffic:one:event", Value: map[string]any{"text": "needle"}, Origin: &busclient.Origin{Plugin: "producer", Instance: "_"}})
	plugin.capture(busclient.Frame{Type: "publish", Channel: "traffic:two:event", Value: "miss", Origin: &busclient.Origin{Plugin: "producer", Instance: "_"}})
	plugin.capture(busclient.Frame{Type: "set", Channel: StatsChannel, Value: map[string]any{}, Origin: &busclient.Origin{Plugin: Manifest.ID, Instance: "_"}})
	plugin.capture(busclient.Frame{Type: "publish", Channel: "traffic:three:event", Value: map[string]any{"text": "needle"}, Origin: &busclient.Origin{Plugin: "producer", Instance: "_"}})
	if plugin.captured != 3 || len(plugin.ring) != 2 || plugin.ring[0].Channel != "traffic:two:event" {
		t.Fatalf("captured=%d ring=%#v", plugin.captured, plugin.ring)
	}
	if plugin.emitted != 2 || plugin.dropped != 0 {
		t.Fatalf("emitted=%d dropped=%d", plugin.emitted, plugin.dropped)
	}
}

func TestEmitThresholdAndSnapshotBudget(t *testing.T) {
	plugin := testPlugin(t, 10, 2)
	plugin.emitWindowStart = time.Now()
	for index := 0; index < 3; index++ {
		plugin.capture(busclient.Frame{Type: "publish", Channel: "burst:_:event", Value: index, Origin: &busclient.Origin{Plugin: "producer", Instance: "_"}})
	}
	if plugin.emitted != 2 || plugin.dropped != 1 {
		t.Fatalf("emitted=%d dropped=%d", plugin.emitted, plugin.dropped)
	}
	ring := []Entry{
		{Seq: 1, Value: strings.Repeat("a", 450_000)},
		{Seq: 2, Value: strings.Repeat("b", 450_000)},
		{Seq: 3, Value: "latest"},
	}
	page := snapshotEntries(ring, 10, 0)
	if len(page) != 2 || page[0].Seq != 3 || page[1].Seq != 2 {
		t.Fatalf("budgeted page = %#v", page)
	}
	older := snapshotEntries(ring, 1, 3)
	if len(older) != 1 || older[0].Seq != 2 {
		t.Fatalf("cursor page = %#v", older)
	}
}
