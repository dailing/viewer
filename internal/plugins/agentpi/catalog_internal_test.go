package agentpi

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"viewer/internal/agentdriver"
)

func TestDiscoverCatalogViaNativeRPC(t *testing.T) {
	mock, err := filepath.Abs("../../../scripts/mock_pi_rpc.py")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIEWER_PI_COMMAND", "python3")
	t.Setenv("VIEWER_PI_ARGS", mock)
	plugin := New()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	catalog, err := plugin.discoverCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Agent != "pi" || len(catalog.Providers) != 2 {
		t.Fatalf("catalog=%#v", catalog)
	}
	if catalog.Providers[0].Provider != "mockprov" || len(catalog.Providers[0].Models) != 2 || catalog.Providers[0].Models[1] != "model-b" {
		t.Fatalf("providers=%#v", catalog.Providers)
	}
	if catalog.Providers[1].Provider != "otherprov" || catalog.Providers[1].Models[0] != "other-model" {
		t.Fatalf("providers=%#v", catalog.Providers)
	}
	if catalog.Providers[0].ParameterSchema["thinking_level"] == nil {
		t.Fatalf("thinking schema missing: %#v", catalog.Providers[0])
	}
}

func TestTargetParameterMapping(t *testing.T) {
	provider, model := piModelSelection(agentdriver.Target{Provider: "mockprov", Model: "model-a"})
	if provider != "mockprov" || model != "model-a" {
		t.Fatalf("selection=%q/%q", provider, model)
	}
	provider, model = piModelSelection(agentdriver.Target{Provider: "default", Model: "model-a"})
	if provider != "" || model != "" {
		t.Fatalf("default provider must preserve Pi selection: %q/%q", provider, model)
	}
	if level := piThinkingLevel(agentdriver.Target{Parameters: map[string]any{"thinking_level": " high "}}); level != "high" {
		t.Fatalf("thinking level=%q", level)
	}
}
