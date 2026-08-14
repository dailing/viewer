package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"viewer/internal/busclient"
	"viewer/internal/kernel"
	"viewer/internal/plugins/pluginrpc"
)

func TestVoiceRelayAndCancel(t *testing.T) {
	starts := make(chan map[string]any, 2)
	chunks := make(chan []byte, 1)
	stops := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	var connections atomic.Int32
	service := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := websocket.Accept(writer, request, nil)
		if err != nil {
			t.Errorf("accept service websocket: %v", err)
			return
		}
		defer conn.CloseNow()
		connection := connections.Add(1)
		kind, data, err := conn.Read(request.Context())
		if err != nil || kind != websocket.MessageText {
			t.Errorf("read start: kind=%v err=%v", kind, err)
			return
		}
		var start map[string]any
		if err := json.Unmarshal(data, &start); err != nil {
			t.Errorf("decode start: %v", err)
			return
		}
		starts <- start
		if connection == 2 {
			if _, _, err := conn.Read(request.Context()); err == nil {
				t.Error("cancelled connection remained open")
			}
			cancelled <- struct{}{}
			return
		}
		kind, data, err = conn.Read(request.Context())
		if err != nil || kind != websocket.MessageBinary {
			t.Errorf("read chunk: kind=%v err=%v", kind, err)
			return
		}
		chunks <- data
		kind, data, err = conn.Read(request.Context())
		if err != nil || kind != websocket.MessageText || string(data) != `{"type":"stop"}` {
			t.Errorf("read stop: kind=%v data=%s err=%v", kind, data, err)
			return
		}
		stops <- struct{}{}
		_ = conn.Write(request.Context(), websocket.MessageText, []byte(`{"type":"processing"}`))
		_ = conn.Write(request.Context(), websocket.MessageText, []byte(`{"type":"final","text":"ok"}`))
	}))
	defer service.Close()

	kernelConfig := kernel.DefaultConfig()
	kernelConfig.Host, kernelConfig.Port = "127.0.0.1", 0
	server := kernel.New(kernelConfig)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Shutdown(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	busURL := fmt.Sprintf("ws://127.0.0.1:%d/ws", server.Port())

	settings := map[string]any{
		"service_ws": "ws" + strings.TrimPrefix(service.URL, "http"),
		"model":      "test-model",
		"language":   "zh",
	}
	configClient := busclient.New(busURL, busclient.Manifest{ID: "voice-test-config", Version: "0.1.0", Slots: map[string]any{"config:_:get": map[string]any{}}, Emits: map[string]any{}})
	_, _ = configClient.Subscribe("config:_:get", func(frame busclient.Frame) {
		value, _ := pluginrpc.Object(frame)
		_ = pluginrpc.Respond(configClient, frame, settings[value["key"].(string)])
	})
	if err := configClient.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer configClient.Close()

	plugin, err := New(Config{KernelWS: busURL})
	if err != nil {
		t.Fatal(err)
	}
	if err := plugin.StartWithManaged(ctx, false); err != nil {
		t.Fatal(err)
	}
	defer plugin.Close()

	observer := busclient.New(busURL, busclient.Manifest{ID: "voice-test-observer", Version: "0.1.0", Slots: map[string]any{}, Emits: map[string]any{}})
	events := make(chan map[string]any, 8)
	_, _ = observer.Subscribe("voice:*:event", func(frame busclient.Frame) {
		if value, ok := frame.Value.(map[string]any); ok {
			events <- value
		}
	})
	if err := observer.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer observer.Close()

	result, err := observer.Request(ctx, "voice:_:start", map[string]any{"mime_type": "audio/webm", "llm_refine": true}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	recID := result.(map[string]any)["rec_id"].(string)
	if len(recID) != 16 {
		t.Fatalf("rec_id=%q", recID)
	}
	start := receive(t, starts)
	for key, want := range map[string]any{"type": "start", "mimeType": "audio/webm", "llm_refine": true, "model": "test-model", "language": "zh"} {
		if start[key] != want {
			t.Fatalf("start[%s]=%#v want %#v; payload=%#v", key, start[key], want, start)
		}
	}
	audio := []byte{0, 1, 2, 128, 255}
	if err := observer.Publish(ctx, "voice:"+recID+":chunk", map[string]any{"data": base64.StdEncoding.EncodeToString(audio)}); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, chunks); string(got) != string(audio) {
		t.Fatalf("chunk=%v want %v", got, audio)
	}
	if err := observer.Publish(ctx, "voice:"+recID+":stop", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	receive(t, stops)
	gotTypes := []string{}
	for len(gotTypes) < 3 {
		gotTypes = append(gotTypes, receive(t, events)["type"].(string))
	}
	if fmt.Sprint(gotTypes) != "[ready processing final]" {
		t.Fatalf("events=%v", gotTypes)
	}
	waitSessions(t, plugin, 0)

	result, err = observer.Request(ctx, "voice:_:start", map[string]any{"mime_type": "audio/mp4", "llm_refine": false}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	recID = result.(map[string]any)["rec_id"].(string)
	receive(t, starts)
	if _, err := observer.Request(ctx, "voice:_:cancel", map[string]any{"rec_id": recID}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	receive(t, cancelled)
	waitSessions(t, plugin, 0)
}

func receive[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(3 * time.Second):
		var zero T
		t.Fatal("timed out waiting for test event")
		return zero
	}
}

func waitSessions(t *testing.T, plugin *Plugin, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		plugin.mu.RLock()
		count := len(plugin.sessions)
		plugin.mu.RUnlock()
		if count == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session count did not reach %d", want)
}
