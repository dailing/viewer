package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func browserFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "fixtures", "browser_actions.html"))
	if err != nil {
		t.Fatal(err)
	}
	frame, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "fixtures", "browser_frame.html"))
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/frame.html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(frame)
		case "/download.txt":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Disposition", "attachment; filename=fixture-download.txt")
			_, _ = fmt.Fprint(w, "downloaded fixture content\n")
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(strings.ReplaceAll(string(fixture), "{{CROSS_ORIGIN}}", server.URL)))
		}
	}))
	return server
}

func integrationAction(t *testing.T, page PageDriver, action string, payload map[string]any) map[string]any {
	t.Helper()
	result, err := page.Execute(context.Background(), action, payload)
	if err != nil {
		t.Fatalf("%s failed: %v", action, err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("%s result = %#v", action, result)
	}
	return object
}

func integrationSnapshot(t *testing.T, page PageDriver) map[string]any {
	t.Helper()
	return integrationAction(t, page, "snapshot", map[string]any{})
}

func integrationElement(t *testing.T, snapshot map[string]any, text string) string {
	t.Helper()
	for _, raw := range snapshot["elements"].([]any) {
		element := raw.(map[string]any)
		if strings.Contains(stringField(element, "text"), text) {
			return stringField(element, "id")
		}
	}
	t.Fatalf("element containing %q not found in %#v", text, snapshot["elements"])
	return ""
}

func integrationElementPayload(snapshot map[string]any, elementID string) map[string]any {
	return map[string]any{"snapshot_id": snapshot["snapshot_id"], "element_id": elementID}
}

func TestChromeAdvancedActions(t *testing.T) {
	manager := newIntegrationChrome(t)
	server := browserFixtureServer(t)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	page, err := manager.OpenPage(ctx, "pg_actions", server.URL, defaultViewport())
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close(context.Background())

	snapshot := integrationSnapshot(t, page)
	payload := integrationElementPayload(snapshot, integrationElement(t, snapshot, "Ordinary name"))
	payload["text"], payload["clear"] = "Alice", true
	integrationAction(t, page, "type", payload)
	snapshot = integrationSnapshot(t, page)
	for _, raw := range snapshot["elements"].([]any) {
		element := raw.(map[string]any)
		if stringField(element, "text") == "Ordinary name" && stringField(element, "value") != "Alice" {
			t.Fatalf("typed value = %#v", element)
		}
	}

	choice := integrationElementPayload(snapshot, integrationElement(t, snapshot, "One Two Three"))
	options := integrationAction(t, page, "dropdown_options", choice)
	if len(options["options"].([]any)) != 3 {
		t.Fatalf("options = %#v", options)
	}
	choice["values"] = []string{"two"}
	integrationAction(t, page, "select", choice)

	snapshot = integrationSnapshot(t, page)
	mouseCases := []struct {
		element string
		action  string
		wait    string
	}{
		{"Click counter", "click", "1 clicks"},
		{"Double click", "double_click", "double clicked"},
		{"Right click", "right_click", "right clicked"},
		{"Hover target", "hover", "hovered"},
	}
	for _, test := range mouseCases {
		t.Run(test.action, func(t *testing.T) {
			integrationAction(t, page, test.action, integrationElementPayload(snapshot, integrationElement(t, snapshot, test.element)))
			integrationAction(t, page, "wait", map[string]any{"text": test.wait})
		})
	}

	keyPayload := integrationElementPayload(snapshot, integrationElement(t, snapshot, "Focus and send keys"))
	integrationAction(t, page, "click", keyPayload)
	for _, chord := range []string{"a", "Control+a", "Delete"} {
		integrationAction(t, page, "send_keys", map[string]any{"keys": chord})
	}
	integrationAction(t, page, "find_text", map[string]any{"text": "Unique text needle"})

	snapshot = integrationSnapshot(t, page)
	passwordID := ""
	for _, raw := range snapshot["elements"].([]any) {
		element := raw.(map[string]any)
		if stringField(element, "input_type") == "password" {
			passwordID = stringField(element, "id")
		}
	}
	if passwordID == "" {
		t.Fatal("password element missing")
	}
	integrationAction(t, page, "click", integrationElementPayload(snapshot, passwordID))
	if _, err := page.Execute(ctx, "send_keys", map[string]any{"keys": "x"}); err == nil {
		t.Fatal("password keyboard input unexpectedly succeeded")
	} else {
		requireErrorCode(t, err, "sensitive_field")
	}
}

func TestChromeFileUploadAndDownload(t *testing.T) {
	manager := newIntegrationChrome(t)
	server := browserFixtureServer(t)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	page, err := manager.OpenPage(ctx, "pg_files", server.URL, defaultViewport())
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close(context.Background())
	uploadPath := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(uploadPath, []byte("upload fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := integrationSnapshot(t, page)
	fileInput := ""
	for _, raw := range snapshot["elements"].([]any) {
		element := raw.(map[string]any)
		if stringField(element, "input_type") == "file" {
			fileInput = stringField(element, "id")
		}
	}
	payload := integrationElementPayload(snapshot, fileInput)
	payload["paths"] = []string{uploadPath}
	integrationAction(t, page, "upload", payload)
	result := integrationAction(t, page, "evaluate", map[string]any{"expression": `document.querySelector('input[type=file]').files[0]?.name`})
	if result["value"] != "upload.txt" {
		t.Fatalf("uploaded file = %#v", result)
	}

	integrationAction(t, page, "click", integrationElementPayload(snapshot, integrationElement(t, snapshot, "Download fixture")))
	var downloadID string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		result = integrationAction(t, page, "downloads", map[string]any{})
		for _, download := range result["downloads"].([]map[string]any) {
			if stringField(download, "state") == "completed" {
				downloadID = stringField(download, "id")
			}
		}
		if downloadID != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if downloadID == "" {
		t.Fatalf("download records = %#v", result)
	}
	result = integrationAction(t, page, "wait", map[string]any{"download_id": downloadID})
	data, err := os.ReadFile(stringField(result, "path"))
	if err != nil || string(data) != "downloaded fixture content\n" {
		t.Fatalf("downloaded content = %q, %v", data, err)
	}
}

func TestChromeEvaluationAndScreenshot(t *testing.T) {
	manager := newIntegrationChrome(t)
	server := browserFixtureServer(t)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	page, err := manager.OpenPage(ctx, "pg_visual", server.URL, defaultViewport())
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close(context.Background())
	result := integrationAction(t, page, "evaluate", map[string]any{"expression": `({answer:42,items:[1,2]})`})
	value := result["value"].(map[string]any)
	if value["answer"] != float64(42) || result["value_type"] != "object" {
		t.Fatalf("evaluation result = %#v", result)
	}
	result = integrationAction(t, page, "evaluate", map[string]any{"expression": `Promise.resolve(21*2)`})
	if result["value"] != float64(42) {
		t.Fatalf("promise result = %#v", result)
	}
	if _, err := page.Execute(ctx, "evaluate", map[string]any{"expression": `throw new Error("fixture failure")`}); err == nil {
		t.Fatal("JavaScript exception unexpectedly succeeded")
	} else {
		requireErrorCode(t, err, "evaluation_error")
	}
	for _, format := range []string{"png", "jpeg"} {
		result = integrationAction(t, page, "screenshot", map[string]any{"format": format, "full_page": true})
		if stringField(result, "data") == "" || intField(result, "width") <= 0 || intField(result, "height") <= 720 {
			t.Fatalf("screenshot result = %#v", result)
		}
		path := stringField(result, "path")
		if !strings.HasPrefix(path, manager.config.ProfileDir+string(os.PathSeparator)) {
			t.Fatalf("screenshot escaped profile: %q", path)
		}
		if info, err := os.Stat(path); err != nil || info.Size() == 0 {
			t.Fatalf("screenshot file = %v, %v", info, err)
		}
	}
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
