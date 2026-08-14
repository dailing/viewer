package agentopencode_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"viewer/internal/agentdriver"
	"viewer/internal/busclient"
	"viewer/internal/kernel"
	"viewer/internal/plugins/agentopencode"
	"viewer/internal/plugins/pluginrpc"
)

func decode(value, target any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func TestOpenCodeBusContractWithMockACP(t *testing.T) {
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

	configClient := busclient.New(url, busclient.Manifest{ID: "agentopencode-test-config", Version: "0.1.0", Slots: map[string]any{"config:_:get": map[string]any{}}, Emits: map[string]any{}})
	if _, err := configClient.Subscribe("config:_:get", func(frame busclient.Frame) {
		_ = pluginrpc.Respond(configClient, frame, agentdriver.Catalog{Agent: "opencode", Providers: []agentdriver.ProviderCatalog{{Provider: "configured", Models: []string{"model-a"}}}})
	}); err != nil {
		t.Fatal(err)
	}
	if err := configClient.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer configClient.Close()

	mock, err := filepath.Abs("../../../scripts/mock_opencode_agent.py")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIEWER_OPENCODE_COMMAND", mock)
	t.Setenv("VIEWER_OPENCODE_ARGS", "acp --mock")
	plugin := agentopencode.New()
	if err := plugin.Start(ctx, url, false); err != nil {
		t.Fatal(err)
	}
	defer plugin.Close()

	caller := busclient.New(url, busclient.Manifest{ID: "agentopencode-test-caller", Version: "0.1.0", Slots: map[string]any{}, Emits: map[string]any{}})
	events := make(chan agentdriver.EventFrame, 16)
	ended := make(chan agentdriver.TurnEndedFrame, 4)
	catalogs := make(chan agentdriver.Catalog, 1)
	_, _ = caller.Subscribe(agentopencode.PluginID+":_:event", func(frame busclient.Frame) {
		var value agentdriver.EventFrame
		if decode(frame.Value, &value) == nil {
			events <- value
		}
	})
	_, _ = caller.Subscribe(agentopencode.PluginID+":_:turn-ended", func(frame busclient.Frame) {
		var value agentdriver.TurnEndedFrame
		if decode(frame.Value, &value) == nil {
			ended <- value
		}
	})
	_, _ = caller.Subscribe(agentopencode.PluginID+":_:catalog", func(frame busclient.Frame) {
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
		if catalog.Agent != "opencode" || len(catalog.Providers) != 1 || catalog.Providers[0].Provider != "configured" || len(catalog.Providers[0].Models) != 1 {
			t.Fatalf("catalog=%#v", catalog)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	startedValue, err := caller.Request(ctx, agentopencode.PluginID+":_:start", map[string]any{"cwd": t.TempDir(), "target": agentdriver.Target{Agent: "opencode", Provider: "default", Parameters: map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	if err := decode(startedValue, &started); err != nil || started.SessionID == "" {
		t.Fatalf("started=%#v err=%v", started, err)
	}
	if _, err := caller.Request(ctx, agentopencode.PluginID+":_:prompt", map[string]any{"session_id": started.SessionID, "turn_id": "turn-1", "text": "hello"}); err != nil {
		t.Fatal(err)
	}
	var dialect, text agentdriver.EventFrame
	select {
	case dialect = <-events:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case text = <-events:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if dialect.TurnID != "turn-1" || dialect.Seq != 0 || dialect.Kind != "opencode_step" || dialect.Block.Kind != agentdriver.KindOther || !strings.Contains(dialect.Block.Payload, `"session_update":"opencode_step"`) {
		t.Fatalf("dialect event=%#v", dialect)
	}
	if text.TurnID != "turn-1" || text.Seq != 1 || text.Block.Kind != agentdriver.KindAgentText || text.RawJSON == "" {
		t.Fatalf("text event=%#v", text)
	}
	select {
	case result := <-ended:
		if result.TurnID != "turn-1" || result.StopReason != "end_turn" {
			t.Fatalf("ended=%#v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	if _, err := caller.Request(ctx, agentopencode.PluginID+":_:prompt", map[string]any{"session_id": started.SessionID, "turn_id": "turn-2", "text": "long"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-events:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancelled, err := caller.Request(ctx, agentopencode.PluginID+":_:cancel", map[string]any{"session_id": started.SessionID})
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
	case result := <-ended:
		if result.TurnID != "turn-2" || result.StopReason != "cancelled" {
			t.Fatalf("ended=%#v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
