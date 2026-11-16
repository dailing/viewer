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
