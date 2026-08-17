package agentdriver

import (
	"context"
	"errors"
	"testing"
)

func TestRefreshSuccessReplacesFallback(t *testing.T) {
	fallback := Catalog{Agent: "a", Providers: []ProviderCatalog{}}
	discovered := Catalog{Agent: "a", Providers: []ProviderCatalog{{Provider: "p", Models: []string{"m"}}}}
	cache := NewCatalogCache(fallback, func(context.Context) (Catalog, error) { return discovered, nil })
	if len(cache.Current().Providers) != 0 {
		t.Fatalf("fallback must be served before the first refresh: %#v", cache.Current())
	}
	got, err := cache.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Providers[0].Provider != "p" || cache.Current().Providers[0].Provider != "p" {
		t.Fatalf("refresh must replace the cache: %#v", cache.Current())
	}
}

func TestRefreshFailureKeepsPrevious(t *testing.T) {
	calls := 0
	cache := NewCatalogCache(Catalog{Agent: "a", Providers: []ProviderCatalog{{Provider: "boot", Models: []string{}}}}, func(context.Context) (Catalog, error) {
		calls++
		if calls == 1 {
			return Catalog{Agent: "a", Providers: []ProviderCatalog{{Provider: "p", Models: []string{"m"}}}}, nil
		}
		return Catalog{}, errors.New("boom")
	})
	if _, err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := cache.Refresh(context.Background())
	if err == nil {
		t.Fatal("expected discovery error")
	}
	if got.Providers[0].Provider != "p" || cache.Current().Providers[0].Provider != "p" {
		t.Fatalf("failed refresh must keep the previous catalog: %#v", got)
	}
}

func TestStartOnceRefreshesImmediately(t *testing.T) {
	cache := NewCatalogCache(Catalog{Agent: "a"}, func(context.Context) (Catalog, error) {
		return Catalog{Agent: "a", Providers: []ProviderCatalog{{Provider: "p"}}}, nil
	})
	updated := []Catalog{}
	cache.StartOnce(context.Background(), func(c Catalog) { updated = append(updated, c) })
	if len(updated) != 1 || len(updated[0].Providers) != 1 || updated[0].Providers[0].Provider != "p" {
		t.Fatalf("StartOnce must refresh exactly once, immediately: %#v", updated)
	}
	if cache.Current().Providers[0].Provider != "p" {
		t.Fatalf("cache=%#v", cache.Current())
	}
}

func TestStartOnceSurvivesFailingFetch(t *testing.T) {
	cache := NewCatalogCache(Catalog{Agent: "a"}, func(context.Context) (Catalog, error) {
		return Catalog{}, errors.New("boom")
	})
	updated := false
	cache.StartOnce(context.Background(), func(Catalog) { updated = true })
	if updated {
		t.Fatal("onUpdate must not fire for a failed refresh")
	}
	if cache.Current().Agent != "a" {
		t.Fatalf("cache=%#v", cache.Current())
	}
}

func TestGroupModelIDs(t *testing.T) {
	entries := GroupModelIDs([]string{"b:2", "a:1", "b:1", "b:2", "", "bare"}, ":")
	if len(entries) != 3 {
		t.Fatalf("entries=%#v", entries)
	}
	if entries[0].Provider != "b" || len(entries[0].Models) != 2 || entries[0].Models[0] != "2" || entries[0].Models[1] != "1" {
		t.Fatalf("first-seen order and de-dup broken: %#v", entries[0])
	}
	if entries[1].Provider != "a" || entries[2].Provider != "default" || entries[2].Models[0] != "bare" {
		t.Fatalf("entries=%#v", entries)
	}
	slash := GroupModelIDs([]string{"opencode/big-pickle"}, "/")
	if len(slash) != 1 || slash[0].Provider != "opencode" || slash[0].Models[0] != "big-pickle" {
		t.Fatalf("slash=%#v", slash)
	}
	if got := GroupModelIDs(nil, ":"); len(got) != 0 {
		t.Fatalf("empty input must yield empty entries: %#v", got)
	}
}
