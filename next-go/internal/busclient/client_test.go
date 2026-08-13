package busclient

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"viewer/internal/kernel"
)

type blockingTransport struct{ wrote chan struct{} }

func (t *blockingTransport) ReadFrame(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (t *blockingTransport) WriteFrame(context.Context, []byte) error {
	select {
	case <-t.wrote:
	default:
		close(t.wrote)
	}
	return nil
}

func (*blockingTransport) Close() error { return nil }

func testManifest(id string) Manifest {
	return Manifest{ID: id, Version: "1.0.0", Slots: map[string]any{}, Emits: map[string]any{}}
}

func startTestKernel(t *testing.T, port int) *kernel.Server {
	t.Helper()
	config := kernel.DefaultConfig()
	config.Port = port
	config.PingInterval = time.Second
	server := kernel.New(config)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	return server
}

func stopTestKernel(t *testing.T, server *kernel.Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func connectTestClient(t *testing.T, server *kernel.Server, id string, opts ...Option) *Client {
	t.Helper()
	client := New("ws://"+server.Addr()+"/ws", testManifest(id), opts...)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func waitUntil(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}

func TestRequestCorrelationGenerationAndMatching(t *testing.T) {
	server := startTestKernel(t, 0)
	defer stopTestKernel(t, server)
	responder := connectTestClient(t, server, "corr-responder")
	caller := connectTestClient(t, server, "corr-caller")

	seen := make(chan map[string]any, 1)
	_, err := responder.Subscribe("rpc:_:corr", func(frame Frame) {
		value, _ := frame.Value.(map[string]any)
		seen <- value
		_ = responder.Publish(context.Background(), value["_reply_to"].(string), map[string]any{
			"_corr": value["_corr"], "ok": true, "result": "matched",
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := caller.Request(context.Background(), "rpc:_:corr", map[string]any{"input": 1}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result != "matched" {
		t.Fatalf("result = %#v", result)
	}
	request := <-seen
	corr, _ := request["_corr"].(string)
	replyTo, _ := request["_reply_to"].(string)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(corr) {
		t.Fatalf("corr = %q", corr)
	}
	if replyTo != "_inbox:"+caller.Conn()+":"+corr {
		t.Fatalf("reply_to = %q", replyTo)
	}
}

func TestRequestTimeoutPublishesCancel(t *testing.T) {
	server := startTestKernel(t, 0)
	defer stopTestKernel(t, server)
	callee := connectTestClient(t, server, "timeout-callee")
	caller := connectTestClient(t, server, "timeout-caller")

	cancelled := make(chan string, 1)
	_, err := callee.Subscribe("rpc:_:slow", func(frame Frame) {
		value, _ := frame.Value.(map[string]any)
		if value["_cancel"] == true {
			cancelled <- value["_corr"].(string)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = caller.Request(context.Background(), "rpc:_:slow", nil, 60*time.Millisecond)
	if !errors.Is(err, ErrRequestTimeout) {
		t.Fatalf("error = %v", err)
	}
	var timeoutError *RequestTimeoutError
	if !errors.As(err, &timeoutError) {
		t.Fatalf("error type = %T", err)
	}
	select {
	case corr := <-cancelled:
		if corr != timeoutError.Corr {
			t.Fatalf("cancel corr = %s, request corr = %s", corr, timeoutError.Corr)
		}
	case <-time.After(time.Second):
		t.Fatal("callee did not receive _cancel")
	}
}

func TestRPCErrorResponseMapping(t *testing.T) {
	server := startTestKernel(t, 0)
	defer stopTestKernel(t, server)
	responder := connectTestClient(t, server, "error-responder")
	caller := connectTestClient(t, server, "error-caller")
	_, err := responder.Subscribe("rpc:_:error", func(frame Frame) {
		value := frame.Value.(map[string]any)
		_ = responder.Publish(context.Background(), value["_reply_to"].(string), map[string]any{
			"_corr": value["_corr"], "ok": false,
			"error": map[string]any{"code": "not_found", "message": "missing item"},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = caller.Request(context.Background(), "rpc:_:error", nil, time.Second)
	var rpcError *RPCError
	if !errors.As(err, &rpcError) || rpcError.Code != "not_found" || rpcError.Message != "missing item" {
		t.Fatalf("error = %#v", err)
	}
}

func TestReconnectRestoresSubscriptionAndRetainedReplay(t *testing.T) {
	server := startTestKernel(t, 0)
	port := server.Port()
	url := "ws://" + server.Addr() + "/ws"
	subscriber := New(url, testManifest("reconnect-subscriber"), WithBackoff(200*time.Millisecond, 200*time.Millisecond))
	defer subscriber.Close()
	values := make(chan string, 4)
	_, err := subscriber.Subscribe("state:_:reconnect", func(frame Frame) {
		value := frame.Value.(map[string]any)
		values <- value["state"].(string)
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := subscriber.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	firstConn := subscriber.Conn()
	states := make(chan ConnectionState, 8)
	subscriber.OnStateChange(func(state ConnectionState) { states <- state })

	stopTestKernel(t, server)
	waitUntil(t, 2*time.Second, func() bool { return !subscriber.Connected() })
	restarted := startTestKernel(t, port)
	defer stopTestKernel(t, restarted)
	publisher := connectTestClient(t, restarted, "reconnect-publisher")
	if err := publisher.Set(context.Background(), "state:_:reconnect", map[string]any{"state": "retained"}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 4*time.Second, func() bool {
		return subscriber.Connected() && subscriber.Conn() != firstConn
	})
	select {
	case value := <-values:
		if value != "retained" {
			t.Fatalf("replayed value = %q", value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restored subscription did not receive retained replay")
	}

	var mu sync.Mutex
	observed := []ConnectionState{}
	drain := true
	for drain {
		select {
		case state := <-states:
			mu.Lock()
			observed = append(observed, state)
			mu.Unlock()
		default:
			drain = false
		}
	}
	if len(observed) == 0 {
		t.Fatal("no connection state callbacks observed")
	}
}

func TestInvalidHelloMapsNamedError(t *testing.T) {
	server := startTestKernel(t, 0)
	defer stopTestKernel(t, server)
	client := New("ws://"+server.Addr()+"/ws", Manifest{ID: "bad", Version: "1", Slots: nil, Emits: nil}, WithReconnect(false))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := client.Connect(ctx)
	if !errors.Is(err, ErrInvalidHello) {
		t.Fatalf("error = %v", err)
	}
	_ = client.Close()
}

func TestCloseConcurrentWithInitialConnect(t *testing.T) {
	fake := &blockingTransport{wrote: make(chan struct{})}
	client := New("ws://unused", testManifest("concurrent-close"))
	client.dial = func(context.Context, string) (transport, error) { return fake, nil }
	connectResult := make(chan error, 1)
	go func() { connectResult <- client.Connect(context.Background()) }()
	select {
	case <-fake.wrote:
	case <-time.After(time.Second):
		t.Fatal("Connect did not begin the hello handshake")
	}
	closed := make(chan struct{})
	go func() { _ = client.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close deadlocked with initial Connect")
	}
	if err := <-connectResult; !errors.Is(err, ErrClosed) {
		t.Fatalf("Connect error = %v", err)
	}
}
