package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestBuildCandidatesRequestedModelSortsFirst(t *testing.T) {
	active := Config{Endpoint: "http://a/v1", Model: "m1"}
	profiles := []storedProfile{
		{Name: "p1", Endpoint: "http://b/v1", Model: "m2"},
		{Name: "p2", Endpoint: "http://c/v1", Model: "m3"},
		{Name: "p3", Endpoint: "http://d/v1", Model: "m2"},
	}
	chain := buildCandidates(active, profiles, "m2")
	var order []string
	for _, entry := range chain {
		order = append(order, entry.name)
	}
	// The requested model's configs keep their list order and lead; the rest
	// follows in default order (active, then profiles top-down).
	want := []string{"p1", "p3", "active", "p2"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", order, want)
	}
	// An unknown model leaves the default order untouched.
	chain = buildCandidates(active, profiles, "nope")
	if chain[0].name != "active" || chain[1].name != "p1" {
		t.Fatalf("unknown model must keep default order: %v", chain)
	}
}

func TestRouteNotFoundFallsBack(t *testing.T) {
	missing := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte("model not found"))
	}))
	defer missing.Close()
	good := okServer(t, "served")
	chain := buildCandidates(
		Config{Endpoint: missing.URL, Model: "m"},
		[]storedProfile{{Name: "good", Endpoint: good.URL, Model: "m"}},
		"",
	)
	result, err := newRouter().route(context.Background(), chain, routeAttempt([]map[string]string{{"role": "user", "content": "hi"}}))
	if err != nil || result.Content != "served" {
		t.Fatalf("result = %#v err = %v", result, err)
	}
}

func TestBuildCandidates(t *testing.T) {
	chain := buildCandidates(
		Config{Endpoint: "http://a/v1", Model: "m1"},
		[]storedProfile{
			{Name: "dup-of-active", Endpoint: "http://a/v1/chat/completions", Model: "m1"},
			{Name: "", Endpoint: "", Model: "m2"},                     // skipped: no endpoint
			{Name: "cloud", Endpoint: "http://b/v1", Model: "m1"},     // kept: same model, other host
			{Name: "", Endpoint: "http://c/v1", Model: "m3"},          // kept: name falls back to model
			{Name: "same-server", Endpoint: "http://a/", Model: "m2"}, // kept: same host, other model
		},
		"",
	)
	if len(chain) != 4 {
		t.Fatalf("chain = %#v", chain)
	}
	if chain[0].name != "active" || chain[1].name != "cloud" || chain[2].name != "m3" || chain[3].name != "same-server" {
		t.Fatalf("chain order = %#v", chain)
	}
}

func okServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		encoded, _ := json.Marshal(map[string]any{
			"model":   "fake",
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
		})
		_, _ = writer.Write(encoded)
	}))
	t.Cleanup(server.Close)
	return server
}

func routeAttempt(messages []map[string]string) func(context.Context, Config) (CompletionResult, error) {
	return func(ctx context.Context, config Config) (CompletionResult, error) {
		return complete(ctx, http.DefaultClient, config, messages, false, nil)
	}
}

func TestRouteFallsBackOnRetryableStatusAndCoolsDown(t *testing.T) {
	var badCalls int32
	bad := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&badCalls, 1)
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer bad.Close()
	good := okServer(t, "from-cloud")
	chain := buildCandidates(
		Config{Endpoint: bad.URL, Model: "m"},
		[]storedProfile{{Name: "cloud", Endpoint: good.URL, Model: "m"}},
		"",
	)
	messages := []map[string]string{{"role": "user", "content": "hi"}}
	router := newRouter()
	now := time.Now()
	router.now = func() time.Time { return now }

	result, err := router.route(context.Background(), chain, routeAttempt(messages))
	if err != nil || result.Content != "from-cloud" {
		t.Fatalf("result = %#v err = %v", result, err)
	}
	if atomic.LoadInt32(&badCalls) != 1 {
		t.Fatalf("bad calls = %d, want 1", badCalls)
	}
	// Within the cooldown the bad endpoint is skipped entirely.
	if _, err := router.route(context.Background(), chain, routeAttempt(messages)); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&badCalls) != 1 {
		t.Fatalf("bad calls during cooldown = %d, want 1", badCalls)
	}
	// After the cooldown it gets one retry (and cools down again on failure).
	now = now.Add(breakerCooldown + time.Second)
	if _, err := router.route(context.Background(), chain, routeAttempt(messages)); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&badCalls) != 2 {
		t.Fatalf("bad calls after cooldown = %d, want 2", badCalls)
	}
}

func TestRouteFallsBackOnTransportError(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	good := okServer(t, "backup")
	chain := buildCandidates(
		Config{Endpoint: deadURL, Model: "m"},
		[]storedProfile{{Name: "backup", Endpoint: good.URL, Model: "m"}},
		"",
	)
	result, err := newRouter().route(context.Background(), chain, routeAttempt([]map[string]string{{"role": "user", "content": "hi"}}))
	if err != nil || result.Content != "backup" {
		t.Fatalf("result = %#v err = %v", result, err)
	}
}

func TestRouteNonRetryableStatusAbortsChain(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte("bad request"))
	}))
	defer bad.Close()
	var goodCalls int32
	good := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&goodCalls, 1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer good.Close()
	chain := buildCandidates(
		Config{Endpoint: bad.URL, Model: "m"},
		[]storedProfile{{Name: "good", Endpoint: good.URL, Model: "m"}},
		"",
	)
	_, err := newRouter().route(context.Background(), chain, routeAttempt([]map[string]string{{"role": "user", "content": "hi"}}))
	var statusErr *statusError
	if !errors.As(err, &statusErr) || statusErr.status != http.StatusBadRequest {
		t.Fatalf("err = %v, want the 400 statusError", err)
	}
	if atomic.LoadInt32(&goodCalls) != 0 {
		t.Fatalf("good endpoint must not be tried after a 400, calls = %d", goodCalls)
	}
}

func TestFallbackAcrossProfilesRPCAndHTTPFacade(t *testing.T) {
	var badCalls int32
	bad := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&badCalls, 1)
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer bad.Close()
	good := okServer(t, "from-cloud")
	good2 := okServer(t, "from-m2-provider")

	kernelConfig := kernel.DefaultConfig()
	kernelConfig.Host, kernelConfig.Port = "127.0.0.1", 0
	kernelServer := kernel.New(kernelConfig)
	if err := kernelServer.Start(); err != nil {
		t.Fatal(err)
	}
	defer kernelServer.Shutdown(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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

	caller := busclient.New(kernelWS, busclient.Manifest{ID: "llm-fallback-test", Version: "0.1.0", Slots: map[string]any{}, Emits: map[string]any{}})
	if err := caller.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer caller.Close()
	if _, err := caller.Request(ctx, "config:_:set", map[string]any{
		"plugin": configNamespace, "key": "active",
		"value": Config{Endpoint: bad.URL, Model: "m"},
	}, rpcBudget); err != nil {
		t.Fatal(err)
	}
	if _, err := caller.Request(ctx, "config:_:set", map[string]any{
		"plugin": configNamespace, "key": "profiles",
		"value": []storedProfile{
			{Name: "cloud", Endpoint: good.URL, Model: "m"},
			{Name: "cloud-m2", Endpoint: good2.URL, Model: "m2"},
		},
	}, rpcBudget); err != nil {
		t.Fatal(err)
	}

	plugin := New()
	if err := plugin.Start(ctx, kernelWS, false); err != nil {
		t.Fatal(err)
	}
	defer plugin.Close(context.Background())

	// HTTP facade path shares the breaker: the bad endpoint stays skipped.
	// (Configured before the calls: writing the http config resets the
	// breaker like any plugins.llm edit.)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	if _, err := caller.Request(ctx, "llm:_:http:configure", map[string]any{"enabled": true, "port": port}, rpcBudget); err != nil {
		t.Fatal(err)
	}

	// RPC path: active (502) falls back to the cloud profile.
	for call := 1; call <= 2; call++ {
		value, err := caller.Request(ctx, "llm:_:complete", map[string]any{
			"messages": []map[string]string{{"role": "user", "content": "hi"}},
		}, rpcBudget)
		if err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
		if value.(map[string]any)["content"] != "from-cloud" {
			t.Fatalf("call %d: value = %#v", call, value)
		}
	}
	if atomic.LoadInt32(&badCalls) != 1 {
		t.Fatalf("bad endpoint must cool down after one failure, calls = %d", badCalls)
	}

	response, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port), "application/json", strings.NewReader(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("facade status = %d", response.StatusCode)
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Choices[0].Message.Content != "from-cloud" {
		t.Fatalf("facade envelope = %#v", envelope)
	}
	if atomic.LoadInt32(&badCalls) != 1 {
		t.Fatalf("bad endpoint must stay cooled across the facade, calls = %d", badCalls)
	}

	// A requested model sorts its configs first, on the RPC and the facade.
	value, err := caller.Request(ctx, "llm:_:complete", map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
		"model":    "m2",
	}, rpcBudget)
	if err != nil {
		t.Fatal(err)
	}
	if value.(map[string]any)["content"] != "from-m2-provider" {
		t.Fatalf("requested-model RPC value = %#v", value)
	}
	response2, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port), "application/json", strings.NewReader(`{"model":"m2","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response2.Body.Close()
	var envelope2 struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(response2.Body).Decode(&envelope2); err != nil {
		t.Fatal(err)
	}
	if envelope2.Choices[0].Message.Content != "from-m2-provider" {
		t.Fatalf("requested-model facade envelope = %#v", envelope2)
	}
	if atomic.LoadInt32(&badCalls) != 1 {
		t.Fatalf("requested model must bypass the cooled default chain head, bad calls = %d", badCalls)
	}

	// An unknown requested model rolls down the default chain to a result.
	response3, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port), "application/json", strings.NewReader(`{"model":"unknown-model","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response3.Body.Close()
	if response3.StatusCode != http.StatusOK {
		t.Fatalf("unknown-model facade status = %d", response3.StatusCode)
	}

	// GET /v1/models lists the union of servable models.
	modelsResponse, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/models", port))
	if err != nil {
		t.Fatal(err)
	}
	defer modelsResponse.Body.Close()
	var modelsList struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(modelsResponse.Body).Decode(&modelsList); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, item := range modelsList.Data {
		ids = append(ids, item.ID)
	}
	if strings.Join(ids, ",") != "m,m2" {
		t.Fatalf("models = %v, want [m m2]", ids)
	}

	// A config edit clears the breaker: the bad endpoint is retried once. The
	// mailbox reset is asynchronous, so poll until it lands.
	if _, err := caller.Request(ctx, "config:_:set", map[string]any{
		"plugin": configNamespace, "key": "active",
		"value": Config{Endpoint: bad.URL, Model: "m"},
	}, rpcBudget); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := caller.Request(ctx, "llm:_:complete", map[string]any{
			"messages": []map[string]string{{"role": "user", "content": "hi"}},
		}, rpcBudget); err != nil {
			t.Fatal(err)
		}
		if atomic.LoadInt32(&badCalls) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("config edit must reset the breaker, bad calls = %d, want 2", badCalls)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestProbeEndpoint(t *testing.T) {
	good := okServer(t, "ok")
	bad := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()

	kernelConfig := kernel.DefaultConfig()
	kernelConfig.Host, kernelConfig.Port = "127.0.0.1", 0
	kernelServer := kernel.New(kernelConfig)
	if err := kernelServer.Start(); err != nil {
		t.Fatal(err)
	}
	defer kernelServer.Shutdown(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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

	caller := busclient.New(kernelWS, busclient.Manifest{ID: "llm-test-probe", Version: "0.1.0", Slots: map[string]any{}, Emits: map[string]any{}})
	if err := caller.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer caller.Close()

	plugin := New()
	if err := plugin.Start(ctx, kernelWS, false); err != nil {
		t.Fatal(err)
	}
	defer plugin.Close(context.Background())

	value, err := caller.Request(ctx, "llm:_:test", map[string]any{"endpoint": good.URL, "model": "m"}, rpcBudget)
	if err != nil {
		t.Fatal(err)
	}
	probe := value.(map[string]any)
	if probe["ok"] != true || probe["model"] != "fake" {
		t.Fatalf("good probe = %#v", probe)
	}
	if latency, isNumber := probe["latency_ms"].(float64); !isNumber || latency < 0 {
		t.Fatalf("latency_ms = %#v", probe["latency_ms"])
	}

	value, err = caller.Request(ctx, "llm:_:test", map[string]any{"endpoint": bad.URL, "model": "m"}, rpcBudget)
	if err != nil {
		t.Fatal(err)
	}
	probe = value.(map[string]any)
	if probe["ok"] != false || !strings.Contains(probe["error"].(string), "500") {
		t.Fatalf("bad probe = %#v", probe)
	}
	// A failed probe must not touch the breaker state.
	if plugin.router.cooled(candidate{config: Config{Endpoint: bad.URL, Model: "m"}}.key()) {
		t.Fatal("probe must not cool the endpoint down")
	}

	if _, err := caller.Request(ctx, "llm:_:test", map[string]any{"endpoint": good.URL}, rpcBudget); err == nil {
		t.Fatal("missing model must be a bad_request")
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
