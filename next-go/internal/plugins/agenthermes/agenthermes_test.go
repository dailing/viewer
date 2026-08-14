package agenthermes_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"viewer/internal/agentdriver"
	"viewer/internal/busclient"
	"viewer/internal/kernel"
	"viewer/internal/plugins/agenthermes"
	"viewer/internal/plugins/pluginrpc"
)

func decode(value, target any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func TestHermesBusContractWithMockACP(t *testing.T) {
	config := kernel.DefaultConfig()
	config.Host, config.Port = "127.0.0.1", 0
	server := kernel.New(config)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	defer server.Shutdown(context.Background())
	url := fmt.Sprintf("ws://127.0.0.1:%d/ws", server.Port())

	configClient := busclient.New(url, busclient.Manifest{ID: "agenthermes-test-config", Version: "0.1.0", Slots: map[string]any{"config:_:get": map[string]any{}}, Emits: map[string]any{}})
	if _, err := configClient.Subscribe("config:_:get", func(frame busclient.Frame) { _ = pluginrpc.Respond(configClient, frame, nil) }); err != nil {
		t.Fatal(err)
	}
	if err := configClient.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer configClient.Close()

	mock, err := filepath.Abs("../../../scripts/mock_acp_agent.py")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIEWER_HERMES_COMMAND", mock)
	plugin := agenthermes.New()
	if err := plugin.Start(ctx, url, false); err != nil {
		t.Fatal(err)
	}
	defer plugin.Close()

	caller := busclient.New(url, busclient.Manifest{ID: "agenthermes-test-caller", Version: "0.1.0", Slots: map[string]any{}, Emits: map[string]any{}})
	events := make(chan agentdriver.EventFrame, 16)
	ended := make(chan string, 4)
	catalogs := make(chan agentdriver.Catalog, 1)
	_, _ = caller.Subscribe(agenthermes.PluginID+":_:event", func(frame busclient.Frame) {
		var value agentdriver.EventFrame
		if decode(frame.Value, &value) == nil {
			events <- value
		}
	})
	_, _ = caller.Subscribe(agenthermes.PluginID+":_:turn-ended", func(frame busclient.Frame) {
		var value struct {
			StopReason string `json:"stop_reason"`
		}
		if decode(frame.Value, &value) == nil {
			ended <- value.StopReason
		}
	})
	_, _ = caller.Subscribe(agenthermes.PluginID+":_:catalog", func(frame busclient.Frame) {
		var value agentdriver.Catalog
		if decode(frame.Value, &value) == nil {
			catalogs <- value
		}
	})
	if err := caller.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer caller.Close()
	select {
	case catalog := <-catalogs:
		if catalog.Agent != "hermes" {
			t.Fatalf("catalog=%#v", catalog)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	startedValue, err := caller.Request(ctx, agenthermes.PluginID+":_:start", map[string]any{"cwd": t.TempDir(), "target": agentdriver.Target{Agent: "hermes", Provider: "default", Parameters: map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	if err := decode(startedValue, &started); err != nil || started.SessionID == "" {
		t.Fatalf("started=%#v err=%v", started, err)
	}
	if _, err := caller.Request(ctx, agenthermes.PluginID+":_:prompt", map[string]any{"session_id": started.SessionID, "text": "hello"}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.SessionID != started.SessionID || event.Seq != 0 || event.RawJSON == "" || event.Block.Kind == "" {
			t.Fatalf("event=%#v", event)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case reason := <-ended:
		if reason != "end_turn" {
			t.Fatalf("reason=%q", reason)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	if _, err := caller.Request(ctx, agenthermes.PluginID+":_:prompt", map[string]any{"session_id": started.SessionID, "text": "long"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-events:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancelled, err := caller.Request(ctx, agenthermes.PluginID+":_:cancel", map[string]any{"session_id": started.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	var stopped struct {
		Stopped bool `json:"stopped"`
	}
	_ = decode(cancelled, &stopped)
	if !stopped.Stopped {
		t.Fatalf("cancel=%#v", cancelled)
	}
	select {
	case reason := <-ended:
		if reason != "cancelled" {
			t.Fatalf("reason=%q", reason)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
