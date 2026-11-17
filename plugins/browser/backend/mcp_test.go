package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mcpCall(t *testing.T, handler *MCPHandler, method string, params any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("MCP status = %d: %s", response.Code, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestMCPRejectsSimpleCrossOriginContentType(t *testing.T) {
	service, _, _, _ := newTestService(t)
	handler := &MCPHandler{service: service}
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("MCP status = %d, want %d", response.Code, http.StatusUnsupportedMediaType)
	}
}

func TestMCPInitializeAndToolSurface(t *testing.T) {
	service, _, _, _ := newTestService(t)
	handler := &MCPHandler{service: service}
	initialized := mcpCall(t, handler, "initialize", map[string]any{"protocolVersion": "2025-03-26"})
	result := initialized["result"].(map[string]any)
	serverInfo := result["serverInfo"].(map[string]any)
	if result["protocolVersion"] != "2025-03-26" || serverInfo["version"] != "0.3.0" || !strings.Contains(result["instructions"].(string), "page_id") {
		t.Fatalf("unexpected initialize result: %v", result)
	}
	listed := mcpCall(t, handler, "tools/list", map[string]any{})
	tools := listed["result"].(map[string]any)["tools"].([]any)
	found := make(map[string]bool)
	for _, raw := range tools {
		tool := raw.(map[string]any)
		name := tool["name"].(string)
		found[name] = true
		if name == "browser_list_targets" || name == "browser_bind" {
			t.Fatalf("unsafe discovery tool exposed: %s", name)
		}
	}
	for _, name := range []string{
		"browser_status", "browser_require", "browser_release", "browser_snapshot",
		"browser_click", "browser_fill", "browser_navigate", "browser_wait", "browser_scroll",
		"browser_go_back", "browser_go_forward", "browser_reload",
		"browser_evaluate", "browser_screenshot", "browser_upload", "browser_downloads",
		"browser_list_tabs", "browser_new_tab", "browser_switch_tab", "browser_close_tab",
	} {
		if !found[name] {
			t.Errorf("required browser tool is missing: %s", name)
		}
	}
}

func TestMCPScreenshotReturnsImageContentWithoutDuplicateBase64(t *testing.T) {
	service, _, _, pageID := newTestService(t)
	handler := &MCPHandler{service: service}
	required := mcpCall(t, handler, "tools/call", map[string]any{"name": "browser_require", "arguments": map[string]any{"page_id": pageID}})
	leaseID := required["result"].(map[string]any)["structuredContent"].(map[string]any)["lease_id"]
	response := mcpCall(t, handler, "tools/call", map[string]any{
		"name": "browser_screenshot", "arguments": map[string]any{"page_id": pageID, "lease_id": leaseID},
	})
	result := response["result"].(map[string]any)
	structured := result["structuredContent"].(map[string]any)
	if _, exists := structured["data"]; exists {
		t.Fatal("base64 data duplicated in structured content")
	}
	content := result["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("screenshot content = %#v", content)
	}
	image := content[1].(map[string]any)
	if image["type"] != "image" || image["mimeType"] != "image/png" || image["data"] != "aGVsbG8=" {
		t.Fatalf("image content = %#v", image)
	}
}

func TestMCPErrorsAreToolResultsAndRequireLease(t *testing.T) {
	service, _, _, pageID := newTestService(t)
	handler := &MCPHandler{service: service}
	missing := mcpCall(t, handler, "tools/call", map[string]any{"name": "browser_status", "arguments": map[string]any{"page_id": "pg_missing"}})
	missingResult := missing["result"].(map[string]any)
	if missingResult["isError"] != true {
		t.Fatalf("missing page was not an MCP tool error: %v", missingResult)
	}
	withoutLease := mcpCall(t, handler, "tools/call", map[string]any{"name": "browser_snapshot", "arguments": map[string]any{"page_id": pageID}})
	if withoutLease["result"].(map[string]any)["isError"] != true {
		t.Fatal("snapshot without lease unexpectedly succeeded")
	}
	required := mcpCall(t, handler, "tools/call", map[string]any{"name": "browser_require", "arguments": map[string]any{"page_id": pageID}})
	structured := required["result"].(map[string]any)["structuredContent"].(map[string]any)
	if structured["lease_id"] == "" || structured["page_id"] != pageID {
		t.Fatalf("unexpected require result: %v", structured)
	}
	released := mcpCall(t, handler, "tools/call", map[string]any{"name": "browser_release", "arguments": map[string]any{"page_id": pageID, "lease_id": structured["lease_id"]}})
	if released["result"].(map[string]any)["isError"] != false {
		t.Fatalf("release failed: %v", released)
	}
}
