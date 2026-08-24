package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"viewer/internal/kernel"
	"viewer/internal/plugins/configstore"
	"viewer/sdk/go/busclient"
)

func TestCompleteBaseURLAndJSONMode(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&calls, 1)
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", request.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		if body["response_format"] == nil {
			t.Errorf("json_mode should set response_format, body = %v", body)
		}
		if request.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("missing bearer key")
		}
		writer.Header().Set("Content-Type", "application/json")
		encoded, _ := json.Marshal(map[string]any{
			"model":   "fake",
			"choices": []map[string]any{{"message": map[string]string{"content": "  done  "}}},
		})
		_, _ = writer.Write(encoded)
	}))
	defer server.Close()
	result, err := complete(context.Background(), server.Client(), Config{Endpoint: server.URL + "/v1", APIKey: "k", Model: "m"}, []map[string]string{{"role": "user", "content": "hi"}}, true, nil)
	if err != nil || result.Content != "done" || result.Model != "fake" {
		t.Fatalf("result = %#v err = %v", result, err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestCompleteHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte("upstream down"))
	}))
	defer server.Close()
	_, err := complete(context.Background(), server.Client(), Config{Endpoint: server.URL, Model: "m"}, []map[string]string{{"role": "user", "content": "hi"}}, false, nil)
	if err == nil {
		t.Fatal("expected HTTP error")
	}
}

func TestCompleteExtraBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		kwargs, ok := body["chat_template_kwargs"].(map[string]any)
		if !ok || kwargs["enable_thinking"] != false {
			t.Errorf("extra_body not merged, body = %v", body)
		}
		if body["model"] != "m" {
			t.Errorf("extra_body must not drop the model field, body = %v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		encoded, _ := json.Marshal(map[string]any{
			"model":   "fake",
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
		_, _ = writer.Write(encoded)
	}))
	defer server.Close()
	extra := map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}}
	result, err := complete(context.Background(), server.Client(), Config{Endpoint: server.URL, Model: "m"}, []map[string]string{{"role": "user", "content": "hi"}}, false, extra)
	if err != nil || result.Content != "ok" {
		t.Fatalf("result = %#v err = %v", result, err)
	}
}

func TestCompleteConfigExtraBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		// Config default applied; the per-call extra_body wins per key.
		if body["reasoning_effort"] != "low" {
			t.Errorf("caller extra_body must override the config default per key, body = %v", body)
		}
		if body["seed"] != float64(7) {
			t.Errorf("config-only default missing, body = %v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		encoded, _ := json.Marshal(map[string]any{
			"model":   "fake",
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
		_, _ = writer.Write(encoded)
	}))
	defer server.Close()
	config := Config{
		Endpoint:  server.URL,
		Model:     "m",
		ExtraBody: map[string]any{"reasoning_effort": "medium", "seed": 7},
	}
	extra := map[string]any{"reasoning_effort": "low"}
	result, err := complete(context.Background(), server.Client(), config, []map[string]string{{"role": "user", "content": "hi"}}, false, extra)
	if err != nil || result.Content != "ok" {
		t.Fatalf("result = %#v err = %v", result, err)
	}
}

func TestMigrateLegacy(t *testing.T) {
	stored := map[string]json.RawMessage{}
	get := func(namespace, key string) (json.RawMessage, bool, error) {
		value, ok := stored[namespace+"/"+key]
		return value, ok, nil
	}
	set := func(namespace, key string, value json.RawMessage) error {
		stored[namespace+"/"+key] = value
		return nil
	}
	stored[legacyNamespace+"/llm"] = json.RawMessage(`{"endpoint":"http://x/v1","model":"m"}`)
	stored[legacyNamespace+"/llm_profiles"] = json.RawMessage(`[{"id":"p1"}]`)
	if err := migrateLegacy(get, set); err != nil {
		t.Fatal(err)
	}
	if _, ok := stored[configNamespace+"/active"]; !ok {
		t.Fatalf("active not migrated: %v", stored)
	}
	if _, ok := stored[configNamespace+"/profiles"]; !ok {
		t.Fatalf("profiles not migrated: %v", stored)
	}
	// Second run must not overwrite user edits in the new namespace.
	stored[configNamespace+"/active"] = json.RawMessage(`{"endpoint":"http://y/v1","model":"edited"}`)
	if err := migrateLegacy(get, set); err != nil {
		t.Fatal(err)
	}
	if string(stored[configNamespace+"/active"]) != `{"endpoint":"http://y/v1","model":"edited"}` {
		t.Fatalf("migration overwrote existing config: %s", stored[configNamespace+"/active"])
	}
}

func TestMigrateLegacyNoLegacyKeys(t *testing.T) {
	stored := map[string]json.RawMessage{}
	get := func(namespace, key string) (json.RawMessage, bool, error) {
		value, ok := stored[namespace+"/"+key]
		return value, ok, nil
	}
	set := func(namespace, key string, value json.RawMessage) error {
		stored[namespace+"/"+key] = value
		return nil
	}
	if err := migrateLegacy(get, set); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 0 {
		t.Fatalf("nothing should be written without legacy keys: %v", stored)
	}
}

func TestHTTPFacadeUsesActiveModelAndCanBeDisabled(t *testing.T) {
	var logOutput bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logOutput, nil)))
	defer slog.SetDefault(previousLogger)
	var upstreamModel atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		upstreamModel.Store(body["model"])
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"model":"central-model","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()

	kernelConfig := kernel.DefaultConfig()
	kernelConfig.Host, kernelConfig.Port = "127.0.0.1", 0
	kernelServer := kernel.New(kernelConfig)
	if err := kernelServer.Start(); err != nil {
		t.Fatal(err)
	}
	defer kernelServer.Shutdown(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	kernelWS := fmt.Sprintf("ws://127.0.0.1:%d/ws", kernelServer.Port())

	store, err := configstore.New(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Start(ctx, kernelWS, false); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	caller := busclient.New(kernelWS, busclient.Manifest{ID: "llm-http-test", Version: "0.1.0", Slots: map[string]any{}, Emits: map[string]any{}})
	if err := caller.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer caller.Close()
	_, err = caller.Request(ctx, "config:_:set", map[string]any{
		"plugin": configNamespace, "key": "active",
		"value": Config{Endpoint: upstream.URL, Model: "central-model"},
	}, rpcBudget)
	if err != nil {
		t.Fatal(err)
	}

	plugin := New()
	if err := plugin.Start(ctx, kernelWS, false); err != nil {
		t.Fatal(err)
	}
	defer plugin.Close(context.Background())

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	statusValue, err := caller.Request(ctx, "llm:_:http:configure", map[string]any{"enabled": true, "port": port, "expose": true}, rpcBudget)
	if err != nil {
		t.Fatal(err)
	}
	status := statusValue.(map[string]any)
	if status["running"] != true {
		t.Fatalf("status = %#v", status)
	}
	if status["host"] != allInterfacesHost || status["expose"] != true {
		t.Fatalf("exposed status = %#v", status)
	}

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	response, err := http.Post(baseURL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"caller-model","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("completion status = %d", response.StatusCode)
	}
	if got := upstreamModel.Load(); got != "central-model" {
		t.Fatalf("upstream model = %v, want central-model", got)
	}
	logged := logOutput.String()
	for _, expected := range []string{"llm HTTP completion", "request_body", "caller-model", "upstream_body", "central-model", "response_body", "choices", "request_id"} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("HTTP trace log missing %q: %s", expected, logged)
		}
	}

	modelsResponse, err := http.Get(baseURL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer modelsResponse.Body.Close()
	var models map[string]any
	if err := json.NewDecoder(modelsResponse.Body).Decode(&models); err != nil {
		t.Fatal(err)
	}
	data := models["data"].([]any)
	if data[0].(map[string]any)["id"] != "central-model" {
		t.Fatalf("models = %#v", models)
	}

	if _, err := caller.Request(ctx, "llm:_:http:configure", map[string]any{"enabled": false, "port": port}, rpcBudget); err != nil {
		t.Fatal(err)
	}
	if plugin.currentHTTPStatus().Running {
		t.Fatal("HTTP facade still running after disable")
	}
}
