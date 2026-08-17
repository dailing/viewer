package broker

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"viewer/sdk/go/protocol"
)

func delivery(kind, channel, value string, ts int64) protocol.Delivery {
	return protocol.Delivery{
		Type: kind, Channel: channel, Value: json.RawMessage(value), TS: ts, Depth: 0,
		Origin: protocol.Origin{Plugin: "test", Instance: "_"},
	}
}

func next(t *testing.T, connection *Connection) protocol.Delivery {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frame, err := connection.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func TestMailboxReplaceReplayInSetOrder(t *testing.T) {
	b := New(10)
	b.Publish(delivery("set", "demo:_:first", `1`, 1), "")
	b.Publish(delivery("set", "demo:_:second", `2`, 2), "")
	b.Publish(delivery("set", "demo:_:first", `3`, 3), "")
	b.Publish(delivery("publish", "demo:_:event", `"past"`, 4), "")
	connection, err := b.AddConnection("subscriber")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe("subscriber", "demo:_"); err != nil {
		t.Fatal(err)
	}
	first, second := next(t, connection), next(t, connection)
	if first.Channel != "demo:_:first" || string(first.Value) != "3" {
		t.Fatalf("first replay = %#v", first)
	}
	if second.Channel != "demo:_:second" || string(second.Value) != "2" {
		t.Fatalf("second replay = %#v", second)
	}
	select {
	case extra := <-connection.queue:
		t.Fatalf("unexpected event replay: %#v", extra)
	default:
	}
}

func TestAtomicSubscribeHandoff(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		b := New(10)
		connection, _ := b.AddConnection("subscriber")
		b.Publish(delivery("set", "race:_:state", `0`, 1), "")
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_ = b.Subscribe("subscriber", "race:_:state")
		}()
		go func() {
			defer wg.Done()
			<-start
			b.Publish(delivery("set", "race:_:state", `1`, 2), "")
		}()
		close(start)
		wg.Wait()
		got := []string{string(next(t, connection).Value)}
		select {
		case frame := <-connection.queue:
			got = append(got, string(frame.Value))
		default:
		}
		if len(got) == 1 && got[0] != "1" || len(got) == 2 && (got[0] != "0" || got[1] != "1") {
			t.Fatalf("handoff sequence = %v", got)
		}
	}
}

func TestBackpressureDropsNewFrameAndReportsError(t *testing.T) {
	b := New(1)
	connection, _ := b.AddConnection("slow")
	if err := b.Subscribe("slow", ">"); err != nil {
		t.Fatal(err)
	}
	b.Publish(delivery("publish", "demo:_:event", `1`, 1), "producer")
	b.Publish(delivery("publish", "demo:_:event", `2`, 2), "producer")
	if connection.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", connection.Dropped())
	}
	errorFrame := next(t, connection)
	if errorFrame.Channel != "_conn:slow:error" {
		t.Fatalf("priority frame = %s", errorFrame.Channel)
	}
	var value map[string]any
	if err := json.Unmarshal(errorFrame.Value, &value); err != nil {
		t.Fatal(err)
	}
	if value["code"] != "slow_consumer" {
		t.Fatalf("error = %#v", value)
	}
	queued := next(t, connection)
	if string(queued.Value) != "1" {
		t.Fatalf("queued value = %s; new frame was not dropped", queued.Value)
	}
}

func TestNoRouteFastFailsRPCRequest(t *testing.T) {
	b := New(10)
	caller, _ := b.AddConnection("caller")
	if err := b.Subscribe("caller", "_inbox:caller:abc"); err != nil {
		t.Fatal(err)
	}
	request := delivery("publish", "chat:_:blocks:list",
		`{"chat_id":"x","_reply_to":"_inbox:caller:abc","_corr":"abc"}`, 1)
	b.Publish(request, "caller")
	response := next(t, caller)
	if response.Channel != "_inbox:caller:abc" {
		t.Fatalf("response channel = %s", response.Channel)
	}
	var value map[string]any
	if err := json.Unmarshal(response.Value, &value); err != nil {
		t.Fatal(err)
	}
	if value["ok"] != false || value["_corr"] != "abc" {
		t.Fatalf("response = %#v", value)
	}
	errValue, ok := value["error"].(map[string]any)
	if !ok || errValue["code"] != "no_route" {
		t.Fatalf("error = %#v", value["error"])
	}
}

func TestNoRouteSilentForPlainEvents(t *testing.T) {
	b := New(10)
	listener, _ := b.AddConnection("listener")
	if err := b.Subscribe("listener", "other:_"); err != nil {
		t.Fatal(err)
	}
	// An unrouted plain event must vanish without a no_route response.
	b.Publish(delivery("publish", "demo:_:nobody-listens", `{"n":1}`, 1), "producer")
	select {
	case frame := <-listener.queue:
		t.Fatalf("unexpected frame: %#v", frame)
	default:
	}
}

func TestNoRouteSkippedWhenSubscriberExists(t *testing.T) {
	b := New(10)
	handler, _ := b.AddConnection("handler")
	if err := b.Subscribe("handler", "chat:_"); err != nil {
		t.Fatal(err)
	}
	caller, _ := b.AddConnection("caller")
	if err := b.Subscribe("caller", "_inbox:caller:abc"); err != nil {
		t.Fatal(err)
	}
	request := delivery("publish", "chat:_:blocks:list",
		`{"chat_id":"x","_reply_to":"_inbox:caller:abc","_corr":"abc"}`, 1)
	b.Publish(request, "caller")
	if got := next(t, handler); got.Channel != "chat:_:blocks:list" {
		t.Fatalf("handler frame = %s", got.Channel)
	}
	select {
	case frame := <-caller.queue:
		t.Fatalf("caller got spurious no_route response: %#v", frame)
	default:
	}
}

func TestErrorMailboxRemovedWithConnection(t *testing.T) {
	b := New(10)
	b.AddConnection("gone")
	b.ReportError("gone", "bad", "bad frame", nil)
	if _, ok := b.GetRetained("_conn:gone:error"); !ok {
		t.Fatal("error mailbox missing")
	}
	b.RemoveConnection("gone")
	if _, ok := b.GetRetained("_conn:gone:error"); ok {
		t.Fatal("error mailbox leaked")
	}
}

func TestRegistryAddRemoveAndLifecycle(t *testing.T) {
	b := New(20)
	observer, _ := b.AddConnection("observer")
	_ = b.Subscribe("observer", "plugins")
	r := NewRegistry(b)
	instance := "job-1"
	r.Register(protocol.Hello{
		Conn: "worker-conn", Managed: true, InstanceID: &instance,
		Manifest: protocol.Manifest{ID: "worker", Version: "1", Slots: map[string]any{}, Emits: map[string]any{}},
	})
	listing := next(t, observer)
	if listing.Channel != "plugins:_:list" {
		t.Fatalf("first registry frame = %s", listing.Channel)
	}
	var entries []RegistryEntry
	if err := json.Unmarshal(listing.Value, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Conn != "worker-conn" || entries[0].InstanceID == nil || *entries[0].InstanceID != instance {
		t.Fatalf("entries = %#v", entries)
	}
	if lifecycle := next(t, observer); lifecycle.Channel != "plugins:worker:lifecycle" {
		t.Fatalf("activation = %s", lifecycle.Channel)
	}
	r.Deregister("worker-conn")
	listing = next(t, observer)
	if err := json.Unmarshal(listing.Value, &entries); err != nil || len(entries) != 0 {
		t.Fatalf("entries after remove = %#v, err=%v", entries, err)
	}
	var lifecycleValue map[string]any
	deactivated := next(t, observer)
	_ = json.Unmarshal(deactivated.Value, &lifecycleValue)
	if lifecycleValue["state"] != "deactivated" {
		t.Fatalf("deactivation = %#v", lifecycleValue)
	}
}
