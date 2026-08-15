package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"
)

type pipePeer struct {
	client *Client
	read   *bufio.Reader
	write  io.WriteCloser
}

func newPipePeer(t *testing.T) *pipePeer {
	t.Helper()
	clientRead, agentWrite := io.Pipe()
	agentRead, clientWrite := io.Pipe()
	return &pipePeer{client: NewStream(clientRead, clientWrite), read: bufio.NewReader(agentRead), write: agentWrite}
}

func (p *pipePeer) request(t *testing.T) map[string]any {
	t.Helper()
	line, err := p.read.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(line, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func (p *pipePeer) send(t *testing.T, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.write.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeNDJSONAndIDCorrelationOutOfOrder(t *testing.T) {
	p := newPipePeer(t)
	defer p.client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	results := make(chan string, 2)
	go func() {
		id, err := p.client.NewSession(ctx, "/one")
		if err != nil {
			results <- err.Error()
		} else {
			results <- id
		}
	}()
	go func() {
		id, err := p.client.NewSession(ctx, "/two")
		if err != nil {
			results <- err.Error()
		} else {
			results <- id
		}
	}()
	first, second := p.request(t), p.request(t)
	p.send(t, map[string]any{"jsonrpc": "2.0", "id": second["id"], "result": map[string]any{"sessionId": "second"}})
	p.send(t, map[string]any{"jsonrpc": "2.0", "id": first["id"], "result": map[string]any{"sessionId": "first"}})
	seen := map[string]bool{<-results: true, <-results: true}
	if !seen["first"] || !seen["second"] {
		t.Fatalf("unexpected results: %#v", seen)
	}
}

func TestNotificationDispatchAndMalformedTolerance(t *testing.T) {
	p := newPipePeer(t)
	defer p.client.Close()
	updates := make(chan Update, 1)
	p.client.OnUpdate(func(update Update) { updates <- update })
	if _, err := p.write.Write([]byte("not-json\n{\"jsonrpc\":\"2.0\",broken}\n")); err != nil {
		t.Fatal(err)
	}
	p.send(t, map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
		"sessionId": "s1", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "hello"}},
	}})
	select {
	case update := <-updates:
		if update.SessionID != "s1" || update.Value["sessionUpdate"] != "agent_message_chunk" {
			t.Fatalf("bad update: %#v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("notification not dispatched")
	}
}

func TestProcessExitFailsPendingRequest(t *testing.T) {
	p := newPipePeer(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := p.client.NewSession(ctx, "/tmp"); done <- err }()
	_ = p.request(t)
	_ = p.write.Close()
	if err := <-done; err == nil {
		t.Fatal("expected stream exit error")
	}
}

func TestConcurrentWritesRemainWholeFrames(t *testing.T) {
	p := newPipePeer(t)
	defer p.client.Close()
	var wg sync.WaitGroup
	for index := 0; index < 4; index++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = p.client.Cancel(context.Background(), "s") }()
	}
	for index := 0; index < 4; index++ {
		line, err := p.read.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if json.Unmarshal(line, &value) != nil || value["method"] != "session/cancel" {
			t.Fatalf("bad frame: %q", line)
		}
	}
	wg.Wait()
}
