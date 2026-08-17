package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
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
		info, err := p.client.NewSession(ctx, "/one")
		if err != nil {
			results <- err.Error()
		} else {
			results <- info.ID
		}
	}()
	go func() {
		info, err := p.client.NewSession(ctx, "/two")
		if err != nil {
			results <- err.Error()
		} else {
			results <- info.ID
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

func TestNewSessionParsesModelsAndConfigOptions(t *testing.T) {
	p := newPipePeer(t)
	defer p.client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	results := make(chan SessionInfo, 1)
	go func() {
		info, err := p.client.NewSession(ctx, "/tmp")
		if err != nil {
			t.Errorf("NewSession: %v", err)
		}
		results <- info
	}()
	req := p.request(t)
	if req["method"] != "session/new" {
		t.Fatalf("method=%v", req["method"])
	}
	params, _ := req["params"].(map[string]any)
	if _, ok := params["mcpServers"]; !ok {
		t.Fatal("session/new params must carry mcpServers (hermes v0.20 requires the field)")
	}
	p.send(t, map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{
		"sessionId": "s1",
		"models": map[string]any{
			"availableModels": []any{
				map[string]any{"modelId": "prov-a:model-1", "name": "ProvA · model-1"},
				map[string]any{"modelId": "prov-b:model-2"},
			},
			"currentModelId": "prov-a:model-1",
		},
		"configOptions": []any{map[string]any{
			"id": "model", "category": "model", "type": "select", "currentValue": "prov-a/model-1",
			"options": []any{map[string]any{"name": "model-1", "value": "prov-a/model-1"}},
		}},
	}})
	info := <-results
	if info.ID != "s1" || info.CurrentModel != "prov-a:model-1" {
		t.Fatalf("info=%#v", info)
	}
	if len(info.Models) != 2 || info.Models[0].ID != "prov-a:model-1" || info.Models[1].ID != "prov-b:model-2" {
		t.Fatalf("models=%#v", info.Models)
	}
	if len(info.ConfigOptions) != 1 || info.ConfigOptions[0].ID != "model" || len(info.ConfigOptions[0].Options) != 1 || info.ConfigOptions[0].Options[0].Value != "prov-a/model-1" {
		t.Fatalf("configOptions=%#v", info.ConfigOptions)
	}
}

func TestNewSessionWithoutModelsTolerated(t *testing.T) {
	p := newPipePeer(t)
	defer p.client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	results := make(chan SessionInfo, 1)
	go func() {
		info, err := p.client.NewSession(ctx, "/tmp")
		if err != nil {
			t.Errorf("NewSession: %v", err)
		}
		results <- info
	}()
	req := p.request(t)
	p.send(t, map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{"sessionId": "s2"}})
	info := <-results
	if info.ID != "s2" || info.Models != nil || info.ConfigOptions != nil || info.CurrentModel != "" {
		t.Fatalf("agents predating the models extension must parse to an empty SessionInfo: %#v", info)
	}
}

func TestSetConfigOptionSendsRPC(t *testing.T) {
	p := newPipePeer(t)
	defer p.client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.client.SetConfigOption(ctx, "s1", "model", "prov/model-2") }()
	req := p.request(t)
	params, _ := req["params"].(map[string]any)
	if req["method"] != "session/set_config_option" || params["sessionId"] != "s1" || params["configId"] != "model" || params["value"] != "prov/model-2" {
		t.Fatalf("req=%#v", req)
	}
	p.send(t, map[string]any{"jsonrpc": "2.0", "id": req["id"], "error": map[string]any{"code": -32602, "message": "model not found"}})
	if err := <-done; err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("server-side validation errors must surface: err=%v", err)
	}
}

func TestPermissionRequestAutoApproved(t *testing.T) {
	p := newPipePeer(t)
	defer p.client.Close()
	p.send(t, map[string]any{"jsonrpc": "2.0", "id": 41, "method": "session/request_permission", "params": map[string]any{
		"sessionId": "s1",
		"options": []any{
			map[string]any{"optionId": "allow_once", "kind": "allow_once", "name": "Allow once"},
			map[string]any{"optionId": "allow_always", "kind": "allow_always", "name": "Allow always"},
			map[string]any{"optionId": "deny", "kind": "reject_once", "name": "Deny"},
		},
	}})
	reply := p.request(t)
	if reply["id"] != float64(41) {
		t.Fatalf("reply id=%v", reply["id"])
	}
	result, _ := reply["result"].(map[string]any)
	outcome, _ := result["outcome"].(map[string]any)
	if outcome["outcome"] != "selected" || outcome["optionId"] != "allow_always" {
		t.Fatalf("permission must be auto-approved with the strongest allow option: %#v", reply)
	}
}

func TestPermissionRequestFallbackOption(t *testing.T) {
	p := newPipePeer(t)
	defer p.client.Close()
	p.send(t, map[string]any{"jsonrpc": "2.0", "id": "perm-7", "method": "session/request_permission", "params": map[string]any{
		"options": []any{
			map[string]any{"optionId": "allow_once", "kind": "allow_once"},
			map[string]any{"optionId": "deny", "kind": "reject_once"},
		},
	}})
	reply := p.request(t)
	result, _ := reply["result"].(map[string]any)
	outcome, _ := result["outcome"].(map[string]any)
	if reply["id"] != "perm-7" || outcome["optionId"] != "allow_once" {
		t.Fatalf("string id must round-trip and the first allow-kind option wins: %#v", reply)
	}
}

func TestUnknownAgentRequestGetsMethodNotFound(t *testing.T) {
	p := newPipePeer(t)
	defer p.client.Close()
	p.send(t, map[string]any{"jsonrpc": "2.0", "id": 9, "method": "fs/read_text_file", "params": map[string]any{}})
	reply := p.request(t)
	errObj, _ := reply["error"].(map[string]any)
	if errObj["code"] != float64(-32601) {
		t.Fatalf("unknown agent requests must fail fast, not stall: %#v", reply)
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
