package agenthermes

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestDiscoverCatalogViaACP drives the real ACP discovery path against the
// mock agent: session/new returns SessionModelState whose modelIds use the
// "provider:model" syntax, and the catalog must group them per provider.
func TestDiscoverCatalogViaACP(t *testing.T) {
	mock, err := filepath.Abs("../../../scripts/mock_acp_agent.py")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIEWER_HERMES_COMMAND", mock)
	plugin := New()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	catalog, err := plugin.discoverCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Agent != "hermes" || len(catalog.Providers) != 2 {
		t.Fatalf("catalog=%#v", catalog)
	}
	if catalog.Providers[0].Provider != "mockprov" {
		t.Fatalf("providers=%#v", catalog.Providers)
	}
	if len(catalog.Providers[0].Models) != 2 || catalog.Providers[0].Models[0] != "mock-model-a" || catalog.Providers[0].Models[1] != "mock-model-b" {
		t.Fatalf("models=%#v", catalog.Providers[0].Models)
	}
	if catalog.Providers[1].Provider != "otherprov" || catalog.Providers[1].Models[0] != "other-model" {
		t.Fatalf("providers=%#v", catalog.Providers)
	}
}

// TestDiscoverCatalogFailsWithoutModels covers agents whose session/new
// carries no SessionModelState: discovery must fail so the CatalogCache keeps
// the previous (fallback or older discovered) catalog.
func TestDiscoverCatalogFailsWithoutModels(t *testing.T) {
	mock, err := filepath.Abs("../../../scripts/mock_acp_agent.py")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIEWER_HERMES_COMMAND", mock)
	t.Setenv("MOCK_ACP_PLAIN_SESSION_NEW", "1")
	plugin := New()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := plugin.discoverCatalog(ctx); err == nil {
		t.Fatal("expected error when session/new carries no models")
	}
}
