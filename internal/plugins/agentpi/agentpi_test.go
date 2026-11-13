package agentpi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"viewer/internal/agentdriver"
	"viewer/internal/kernel"
	"viewer/internal/plugins/agentpi"
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

func TestPiBusContractAndNormalizedEvents(t *testing.T) {
	config := kernel.DefaultConfig()
	config.Host, config.Port = "127.0.0.1", 0
	server := kernel.New(config)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	defer server.Shutdown(context.Background())
	url := fmt.Sprintf("ws://127.0.0.1:%d/ws", server.Port())

	configClient := busclient.New(url, busclient.Manifest{ID: "agentpi-test-config", Version: "0.1.0", Slots: map[string]any{"config:_:get": map[string]any{}}, Emits: map[string]any{}})
	if _, err := configClient.Subscribe("config:_:get", func(frame busclient.Frame) {
		_ = pluginrpc.Respond(configClient, frame, agentdriver.Catalog{Agent: "pi", Providers: []agentdriver.ProviderCatalog{{Provider: "configured", Models: []string{"model-a"}}}})
	}); err != nil {
		t.Fatal(err)
	}
	if err := configClient.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer configClient.Close()

	mock, err := filepath.Abs("../../../scripts/mock_pi_rpc.py")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIEWER_PI_COMMAND", "python3")
	t.Setenv("VIEWER_PI_ARGS", mock)
	plugin := agentpi.New()
	if err := plugin.Start(ctx, url, false); err != nil {
		t.Fatal(err)
	}
	defer plugin.Close()

	caller := busclient.New(url, busclient.Manifest{ID: "agentpi-test-caller", Version: "0.1.0", Slots: map[string]any{}, Emits: map[string]any{}})
	events := make(chan agentdriver.EventFrame, 128)
	ended := make(chan agentdriver.TurnEndedFrame, 8)
	catalogs := make(chan agentdriver.Catalog, 1)
	_, _ = caller.Subscribe(agentpi.PluginID+":_:event", func(frame busclient.Frame) {
		var value agentdriver.EventFrame
		if decode(frame.Value, &value) == nil {
			events <- value
		}
	})
	_, _ = caller.Subscribe(agentpi.PluginID+":_:turn-ended", func(frame busclient.Frame) {
		var value agentdriver.TurnEndedFrame
		if decode(frame.Value, &value) == nil {
			ended <- value
		}
	})
	_, _ = caller.Subscribe(agentpi.PluginID+":_:catalog", func(frame busclient.Frame) {
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
		if catalog.Agent != "pi" || len(catalog.Providers) != 1 || catalog.Providers[0].Provider != "configured" {
			t.Fatalf("catalog=%#v", catalog)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	startedValue, err := caller.Request(ctx, agentpi.PluginID+":_:start", map[string]any{
		"cwd":    t.TempDir(),
		"target": agentdriver.Target{Agent: "pi", Provider: "mockprov", Model: "model-b", Parameters: map[string]any{"thinking_level": "high"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		SessionID   string `json:"session_id"`
		PiSessionID string `json:"pi_session_id"`
		Resumed     bool   `json:"resumed"`
	}
	if err := decode(startedValue, &started); err != nil || !strings.HasSuffix(started.SessionID, ".jsonl") || started.PiSessionID == "" || started.Resumed {
		t.Fatalf("started=%#v err=%v", started, err)
	}
	resumedValue, err := caller.Request(ctx, agentpi.PluginID+":_:start", map[string]any{
		"cwd": t.TempDir(), "session_id": started.SessionID,
		"target": agentdriver.Target{Agent: "pi", Provider: "mockprov", Model: "model-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var resumed struct {
		Resumed bool `json:"resumed"`
	}
	_ = decode(resumedValue, &resumed)
	if !resumed.Resumed {
		t.Fatalf("in-memory resume=%#v", resumedValue)
	}

	if _, err := caller.Request(ctx, agentpi.PluginID+":_:prompt", map[string]any{"session_id": started.SessionID, "turn_id": "turn-1", "text": "hello"}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-ended:
		if result.TurnID != "turn-1" || result.StopReason != "end_turn" || result.Error != "" {
			t.Fatalf("ended=%#v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var first []agentdriver.EventFrame
	for {
		select {
		case event := <-events:
			first = append(first, event)
			if event.Kind == "get_session_stats" {
				goto statsReceived
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
statsReceived:
	for len(events) > 0 {
		first = append(first, <-events)
	}
	assertNormalizedPiEvents(t, first, "turn-1")

	if _, err := caller.Request(ctx, agentpi.PluginID+":_:prompt", map[string]any{"session_id": started.SessionID, "turn_id": "turn-2", "text": "long"}); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case event := <-events:
			if event.TurnID == "turn-2" && event.Kind == "agent_start" {
				goto cancel
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
cancel:
	cancelValue, err := caller.Request(ctx, agentpi.PluginID+":_:cancel", map[string]any{"session_id": started.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	var stopped struct {
		Stopped bool `json:"stopped"`
	}
	_ = decode(cancelValue, &stopped)
	if !stopped.Stopped {
		t.Fatalf("cancel=%#v", cancelValue)
	}
	select {
	case result := <-ended:
		if result.TurnID != "turn-2" || result.StopReason != "cancelled" {
			t.Fatalf("cancel ended=%#v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for len(events) > 0 {
		<-events
	}

	if _, err := caller.Request(ctx, agentpi.PluginID+":_:prompt", map[string]any{"session_id": started.SessionID, "turn_id": "turn-3", "text": "error"}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-ended:
		if result.TurnID != "turn-3" || result.StopReason != "error" || !strings.Contains(result.Error, "provider unavailable") {
			t.Fatalf("error ended=%#v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	foundError := false
	for len(events) > 0 {
		event := <-events
		if event.TurnID == "turn-3" && event.Block.Kind == agentdriver.KindError && strings.Contains(event.Block.Text, "provider unavailable") {
			foundError = true
		}
	}
	if !foundError {
		t.Fatal("terminal Pi provider error was not normalized as an error block")
	}

	if _, err := caller.Request(ctx, agentpi.PluginID+":_:start", map[string]any{
		"cwd": t.TempDir(), "target": agentdriver.Target{Agent: "pi", Provider: "mockprov", Model: "missing"},
	}); err == nil || !strings.Contains(err.Error(), "Model not found") {
		t.Fatalf("invalid model must fail start: %v", err)
	}
}

func assertNormalizedPiEvents(t *testing.T, events []agentdriver.EventFrame, turnID string) {
	t.Helper()
	if len(events) < 10 {
		t.Fatalf("too few events: %d", len(events))
	}
	kinds := map[string]int{}
	blockKinds := map[string]int{}
	var text strings.Builder
	lastSeq := -1
	for _, event := range events {
		if event.TurnID != turnID || event.SessionID == "" || event.RawJSON == "" {
			t.Errorf("bad correlation/raw event=%#v", event)
		}
		if event.Seq != lastSeq+1 {
			t.Errorf("non-contiguous seq: previous=%d event=%#v", lastSeq, event)
		}
		lastSeq = event.Seq
		kinds[event.Kind]++
		blockKinds[event.Block.Kind]++
		if event.Block.Kind == agentdriver.KindAgentText {
			text.WriteString(event.Block.Text)
		}
	}
	if text.String() != "pi: hello" {
		t.Errorf("assistant text duplicated or missing: %q", text.String())
	}
	for kind, minimum := range map[string]int{agentdriver.KindThinking: 1, agentdriver.KindToolCall: 2, agentdriver.KindToolResult: 2, agentdriver.KindCommand: 3, agentdriver.KindTokenUsage: 1} {
		if blockKinds[kind] < minimum {
			t.Errorf("block kind %s count=%d, want >=%d; all=%v", kind, blockKinds[kind], minimum, blockKinds)
		}
	}
	if kinds["extension_ui_request"] != 1 || kinds["get_session_stats"] != 1 {
		t.Errorf("missing extension dialog raw event or final stats: %v", kinds)
	}
	last := events[len(events)-1]
	if last.Kind != "get_session_stats" || last.Block.Kind != agentdriver.KindTokenUsage {
		t.Errorf("context stats must precede turn-ended: %#v", last)
	}
}
