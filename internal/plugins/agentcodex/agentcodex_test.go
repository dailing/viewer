package agentcodex_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"viewer/internal/agentdriver"
	"viewer/internal/kernel"
	"viewer/internal/plugins/agentcodex"
	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

func decode(value, target any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func TestCodexBusContractWithMockServer(t *testing.T) {
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

	configClient := busclient.New(url, busclient.Manifest{ID: "agentcodex-test-config", Version: "0.1.0", Slots: map[string]any{"config:_:get": map[string]any{}}, Emits: map[string]any{}})
	_, _ = configClient.Subscribe("config:_:get", func(frame busclient.Frame) { _ = pluginrpc.Respond(configClient, frame, nil) })
	if err := configClient.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer configClient.Close()

	_, file, _, _ := runtime.Caller(0)
	mock := filepath.Join(filepath.Dir(file), "..", "..", "..", "scripts", "mock_codex_server.py")
	t.Setenv("VIEWER_CODEX_APP_SERVER_COMMAND", mock)
	plugin := agentcodex.New()
	if err := plugin.Start(ctx, url, false); err != nil {
		t.Fatal(err)
	}
	defer plugin.Close()

	caller := busclient.New(url, busclient.Manifest{ID: "agentcodex-test-caller", Version: "0.1.0", Slots: map[string]any{}, Emits: map[string]any{}})
	events := make(chan agentdriver.EventFrame, 16)
	ended := make(chan agentdriver.TurnEndedFrame, 4)
	catalogs := make(chan agentdriver.Catalog, 1)
	_, _ = caller.Subscribe(agentcodex.PluginID+":_:event", func(frame busclient.Frame) {
		var value agentdriver.EventFrame
		if decode(frame.Value, &value) == nil {
			events <- value
		}
	})
	_, _ = caller.Subscribe(agentcodex.PluginID+":_:turn-ended", func(frame busclient.Frame) {
		var value agentdriver.TurnEndedFrame
		if decode(frame.Value, &value) == nil {
			ended <- value
		}
	})
	_, _ = caller.Subscribe(agentcodex.PluginID+":_:catalog", func(frame busclient.Frame) {
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
		if catalog.Agent != "codex" || len(catalog.Providers) != 1 || len(catalog.Providers[0].Models) != 2 {
			t.Fatalf("catalog=%#v", catalog)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	startedValue, err := caller.Request(ctx, agentcodex.PluginID+":_:start", map[string]any{"cwd": t.TempDir(), "target": agentdriver.Target{Agent: "codex-app-server", Provider: "openai-subscription", Model: "gpt-test", Parameters: map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	if err := decode(startedValue, &started); err != nil || started.SessionID == "" {
		t.Fatalf("started=%#v err=%v", started, err)
	}
	if _, err := caller.Request(ctx, agentcodex.PluginID+":_:prompt", map[string]any{"session_id": started.SessionID, "turn_id": "turn-1", "text": "hello"}); err != nil {
		t.Fatal(err)
	}
	foundText := false
	for !foundText {
		select {
		case event := <-events:
			if event.SessionID != started.SessionID || event.TurnID != "turn-1" || event.RawJSON == "" || event.Block.Kind == "" {
				t.Fatalf("event=%#v", event)
			}
			foundText = event.Block.Kind == agentdriver.KindAgentText && event.Block.Text == "mock answer"
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	select {
	case result := <-ended:
		if result.TurnID != "turn-1" || result.StopReason != "end_turn" {
			t.Fatalf("ended=%#v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
