package agentopencode

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"viewer/internal/agentdriver"
)

// TestDiscoverCatalogViaACP drives the ACP discovery path against the mock
// opencode agent: session/new carries configOptions whose select-typed "model"
// option lists "provider/model" values, grouped per provider in the catalog.
func TestDiscoverCatalogViaACP(t *testing.T) {
	mock, err := filepath.Abs("../../../scripts/mock_opencode_agent.py")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIEWER_OPENCODE_COMMAND", mock)
	plugin := New()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	catalog, err := plugin.discoverCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Agent != "opencode" || len(catalog.Providers) != 2 {
		t.Fatalf("catalog=%#v", catalog)
	}
	if catalog.Providers[0].Provider != "mockzen" {
		t.Fatalf("providers=%#v", catalog.Providers)
	}
	if len(catalog.Providers[0].Models) != 2 || catalog.Providers[0].Models[0] != "model-a" || catalog.Providers[0].Models[1] != "model-b" {
		t.Fatalf("models=%#v", catalog.Providers[0].Models)
	}
	if catalog.Providers[1].Provider != "other" || catalog.Providers[1].Models[0] != "model-c" {
		t.Fatalf("providers=%#v", catalog.Providers)
	}
}

// TestDiscoverCatalogFailsWithoutModelOption covers agents whose session/new
// carries no model config option: discovery must fail so the CatalogCache
// keeps the previous catalog.
func TestDiscoverCatalogFailsWithoutModelOption(t *testing.T) {
	mock, err := filepath.Abs("../../../scripts/mock_opencode_agent.py")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIEWER_OPENCODE_COMMAND", mock)
	t.Setenv("MOCK_OPENCODE_PLAIN_SESSION_NEW", "1")
	plugin := New()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := plugin.discoverCatalog(ctx); err == nil {
		t.Fatal("expected error when session/new carries no model config option")
	}
}

// TestOpencodeModelValue pins the routing-target → "provider/model" encoding
// used for session/set_config_option, including the fail-open cases where no
// enforcement should happen at all.
func TestOpencodeModelValue(t *testing.T) {
	cases := []struct {
		name   string
		target agentdriver.Target
		want   string
	}{
		{"provider and model", agentdriver.Target{Provider: "mockzen", Model: "model-a"}, "mockzen/model-a"},
		{"default provider means no enforcement", agentdriver.Target{Provider: "default", Model: "model-a"}, ""},
		{"empty provider means no enforcement", agentdriver.Target{Provider: "", Model: "model-a"}, ""},
		{"empty model", agentdriver.Target{Provider: "mockzen", Model: ""}, ""},
		{"whitespace trimmed", agentdriver.Target{Provider: " mockzen ", Model: " model-b "}, "mockzen/model-b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := opencodeModelValue(tc.target); got != tc.want {
				t.Fatalf("opencodeModelValue(%#v)=%q, want %q", tc.target, got, tc.want)
			}
		})
	}
}
