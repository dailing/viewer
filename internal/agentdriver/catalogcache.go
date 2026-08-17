package agentdriver

import (
	"context"
	"strings"
	"sync"
)

// CatalogFetcher discovers the catalog from the agent over its wire protocol
// (ACP session/new models / configOptions, codex app-server model/list, ...).
// It returns an error when discovery fails or yields nothing usable; the
// cache then keeps its previous value.
type CatalogFetcher func(ctx context.Context) (Catalog, error)

// CatalogCache holds the most recently discovered catalog. It starts at a
// static fallback so the plugin can publish something immediately at boot,
// then a one-shot background refresh (StartOnce) or a manual RPC (Refresh)
// replaces it with protocol-discovered data. A failed refresh never erases
// good data. There is deliberately no periodic loop: further discovery is
// triggered on demand when the chat manager opens (chat fans out
// chat:_:agent-catalog-refresh to every agent plugin's catalog-refresh RPC).
type CatalogCache struct {
	mu      sync.Mutex
	catalog Catalog
	fetch   CatalogFetcher
}

func NewCatalogCache(fallback Catalog, fetch CatalogFetcher) *CatalogCache {
	return &CatalogCache{catalog: fallback, fetch: fetch}
}

// Current returns the cached catalog (the fallback until the first successful
// refresh).
func (c *CatalogCache) Current() Catalog {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.catalog
}

// Refresh runs one discovery round. On success the cache is updated and the
// discovered catalog returned; on failure the previous value is kept.
func (c *CatalogCache) Refresh(ctx context.Context) (Catalog, error) {
	catalog, err := c.fetch(ctx)
	if err != nil {
		return c.Current(), err
	}
	c.mu.Lock()
	c.catalog = catalog
	c.mu.Unlock()
	return catalog, nil
}

// GroupModelIDs splits "provider<sep>model" identifiers into provider-grouped
// catalog entries, preserving first-seen order and de-duplicating models. IDs
// without the separator land in the "default" provider bucket. hermes uses
// ":" ("kimi-coding:kimi-k3"), opencode uses "/" ("opencode/big-pickle").
func GroupModelIDs(ids []string, sep string) []ProviderCatalog {
	order := []string{}
	byProvider := map[string][]string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" {
			continue
		}
		provider, model, ok := strings.Cut(id, sep)
		if !ok || provider == "" || model == "" {
			provider, model = "default", id
		}
		if _, exists := byProvider[provider]; !exists {
			order = append(order, provider)
		}
		key := provider + "\x00" + model
		if seen[key] {
			continue
		}
		seen[key] = true
		byProvider[provider] = append(byProvider[provider], model)
	}
	entries := make([]ProviderCatalog, 0, len(order))
	for _, provider := range order {
		entries = append(entries, ProviderCatalog{Provider: provider, Models: byProvider[provider]})
	}
	return entries
}

// StartOnce refreshes exactly once (synchronously). Call sites wrap it in a
// goroutine so boot is not blocked. onUpdate runs only after a successful
// refresh so the plugin can re-publish its retained catalog mailbox;
// subscribers (the chat plugin, and through it the chat manager panels) see
// updates without any polling. Later refreshes happen on demand through
// Refresh (the plugin's catalog-refresh RPC).
func (c *CatalogCache) StartOnce(ctx context.Context, onUpdate func(Catalog)) {
	if catalog, err := c.Refresh(ctx); err == nil {
		onUpdate(catalog)
	}
}
