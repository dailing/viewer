package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newIntegrationChrome(t *testing.T) *ChromeManager {
	t.Helper()
	if os.Getenv("VIEWER_BROWSER_INTEGRATION") != "1" {
		t.Skip("set VIEWER_BROWSER_INTEGRATION=1 for real Chrome integration")
	}
	manager, err := NewChromeManager(ChromeConfig{ProfileDir: filepath.Join(t.TempDir(), "profile")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	return manager
}

func TestChromeNavigationAndWaits(t *testing.T) {
	manager := newIntegrationChrome(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/one":
			_, _ = fmt.Fprint(w, "<title>Page one</title><h1>Page one</h1>")
		case "/two":
			_, _ = fmt.Fprint(w, "<title>Page two</title><h1>Page two</h1>")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	page, err := manager.OpenPage(ctx, "pg_navigation", server.URL+"/one", defaultViewport())
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close(context.Background())
	if _, err := page.Execute(ctx, "navigate", map[string]any{"url": server.URL + "/two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := page.Execute(ctx, "wait", map[string]any{"text": "Page two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := page.Execute(ctx, "go_back", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := page.Execute(ctx, "wait", map[string]any{"text": "Page one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := page.Execute(ctx, "go_forward", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := page.Execute(ctx, "wait", map[string]any{"url_contains": "/two", "ready_state": "complete"}); err == nil {
		t.Fatal("multiple wait predicates unexpectedly succeeded")
	}
	if _, err := page.Execute(ctx, "wait", map[string]any{"url_contains": "/two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := page.Execute(ctx, "reload", map[string]any{"ignore_cache": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := page.Execute(ctx, "wait", map[string]any{"ready_state": "complete"}); err != nil {
		t.Fatal(err)
	}
}

func TestChromeEventDrain(t *testing.T) {
	manager := newIntegrationChrome(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<title>Integration fixture</title><h1>Ready</h1>")
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	page, err := manager.OpenPage(ctx, "pg_integration", server.URL, defaultViewport())
	if err != nil {
		t.Fatal(err)
	}
	url, title, err := page.Metadata(ctx)
	if err != nil || url != server.URL+"/" || title != "Integration fixture" {
		t.Fatalf("metadata = %q %q, %v", url, title, err)
	}
	if err := page.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
