package main

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
	"time"
)

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

var pageIDProperty = map[string]any{"type": "string", "pattern": "^pg_[A-Z2-7]{26}$", "description": "The random page id explicitly supplied by the user."}
var leaseIDProperty = map[string]any{"type": "string", "description": "The lease id returned by browser_require."}

func schema(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
}

func withIDs(extra map[string]any) map[string]any {
	result := map[string]any{"page_id": pageIDProperty, "lease_id": leaseIDProperty}
	for key, value := range extra {
		result[key] = value
	}
	return result
}

var mcpTools = []mcpTool{
	{Name: "browser_status", Description: "Return status for the exact user-supplied page id. This is the only tool that works without a lease.", InputSchema: schema([]string{"page_id"}, map[string]any{"page_id": pageIDProperty})},
	{Name: "browser_require", Description: "Acquire exclusive control of one exact page. Returns a random lease_id. Fails with page_busy rather than waiting or selecting another page. Passing the current lease_id renews it idempotently.", InputSchema: schema([]string{"page_id"}, withIDs(nil))},
	{Name: "browser_release", Description: "Release the caller's lease when the page task is complete. The managed page remains open for later use.", InputSchema: schema([]string{"page_id", "lease_id"}, withIDs(nil))},
	{Name: "browser_snapshot", Description: "Read visible text and interactive elements from the exact page. Element ids are valid only with the returned snapshot_id.", InputSchema: schema([]string{"page_id", "lease_id"}, withIDs(map[string]any{"max_chars": map[string]any{"type": "integer", "minimum": 1000, "maximum": 50000}, "max_elements": map[string]any{"type": "integer", "minimum": 10, "maximum": 500}}))},
	{Name: "browser_click", Description: "Click an element from a current snapshot on the exact page.", InputSchema: schema([]string{"page_id", "lease_id", "snapshot_id", "element_id"}, withIDs(map[string]any{"snapshot_id": map[string]any{"type": "string"}, "element_id": map[string]any{"type": "string"}}))},
	{Name: "browser_double_click", Description: "Double-click an element from a current snapshot.", InputSchema: schema([]string{"page_id", "lease_id", "snapshot_id", "element_id"}, withIDs(map[string]any{"snapshot_id": map[string]any{"type": "string"}, "element_id": map[string]any{"type": "string"}}))},
	{Name: "browser_right_click", Description: "Right-click an element from a current snapshot.", InputSchema: schema([]string{"page_id", "lease_id", "snapshot_id", "element_id"}, withIDs(map[string]any{"snapshot_id": map[string]any{"type": "string"}, "element_id": map[string]any{"type": "string"}}))},
	{Name: "browser_hover", Description: "Hover an element from a current snapshot.", InputSchema: schema([]string{"page_id", "lease_id", "snapshot_id", "element_id"}, withIDs(map[string]any{"snapshot_id": map[string]any{"type": "string"}, "element_id": map[string]any{"type": "string"}}))},
	{Name: "browser_type", Description: "Focus and type ordinary text into an input, textarea, or contenteditable from a current snapshot. Password and file inputs are rejected.", InputSchema: schema([]string{"page_id", "lease_id", "snapshot_id", "element_id", "text"}, withIDs(map[string]any{"snapshot_id": map[string]any{"type": "string"}, "element_id": map[string]any{"type": "string"}, "text": map[string]any{"type": "string"}, "clear": map[string]any{"type": "boolean"}}))},
	{Name: "browser_send_keys", Description: "Send one keyboard key or chord such as Enter, Escape, Tab, Control+a, or Shift+ArrowLeft to the focused element. Password fields are rejected.", InputSchema: schema([]string{"page_id", "lease_id", "keys"}, withIDs(map[string]any{"keys": map[string]any{"type": "string", "minLength": 1}}))},
	{Name: "browser_dropdown_options", Description: "List options for a select element from a current snapshot.", InputSchema: schema([]string{"page_id", "lease_id", "snapshot_id", "element_id"}, withIDs(map[string]any{"snapshot_id": map[string]any{"type": "string"}, "element_id": map[string]any{"type": "string"}}))},
	{Name: "browser_select", Description: "Select one or more option values on a select element from a current snapshot.", InputSchema: schema([]string{"page_id", "lease_id", "snapshot_id", "element_id", "values"}, withIDs(map[string]any{"snapshot_id": map[string]any{"type": "string"}, "element_id": map[string]any{"type": "string"}, "values": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1}}))},
	{Name: "browser_find_text", Description: "Find visible text and scroll it into view on the exact page.", InputSchema: schema([]string{"page_id", "lease_id", "text"}, withIDs(map[string]any{"text": map[string]any{"type": "string", "minLength": 1}}))},
	{Name: "browser_evaluate", Description: "Evaluate JavaScript on the exact page and return a JSON-serializable value up to 256 KiB. Never use this to read credentials, cookies, tokens, or to bypass human authorization.", InputSchema: schema([]string{"page_id", "lease_id", "expression"}, withIDs(map[string]any{"expression": map[string]any{"type": "string", "minLength": 1}}))},
	{Name: "browser_screenshot", Description: "Capture the exact page as PNG or JPEG. Returns MCP image content and a local artifact path.", InputSchema: schema([]string{"page_id", "lease_id"}, withIDs(map[string]any{"format": map[string]any{"type": "string", "enum": []string{"png", "jpeg"}}, "quality": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}, "full_page": map[string]any{"type": "boolean"}}))},
	{Name: "browser_upload", Description: "Upload 1-16 clean absolute regular-file paths through a file input from the current snapshot.", InputSchema: schema([]string{"page_id", "lease_id", "snapshot_id", "element_id", "paths"}, withIDs(map[string]any{"snapshot_id": map[string]any{"type": "string"}, "element_id": map[string]any{"type": "string"}, "paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": 16}}))},
	{Name: "browser_downloads", Description: "List recent downloads belonging only to this exact Page.", InputSchema: schema([]string{"page_id", "lease_id"}, withIDs(nil))},
	{Name: "browser_fill", Description: "Fill an input, textarea, select, or contenteditable element from a current snapshot. Password fields are rejected.", InputSchema: schema([]string{"page_id", "lease_id", "snapshot_id", "element_id", "text"}, withIDs(map[string]any{"snapshot_id": map[string]any{"type": "string"}, "element_id": map[string]any{"type": "string"}, "text": map[string]any{"type": "string"}}))},
	{Name: "browser_navigate", Description: "Navigate the exact managed page to an http(s) URL. Never selects another page.", InputSchema: schema([]string{"page_id", "lease_id", "url"}, withIDs(map[string]any{"url": map[string]any{"type": "string", "format": "uri"}}))},
	{Name: "browser_go_back", Description: "Go back one entry in the exact page's navigation history.", InputSchema: schema([]string{"page_id", "lease_id"}, withIDs(nil))},
	{Name: "browser_go_forward", Description: "Go forward one entry in the exact page's navigation history.", InputSchema: schema([]string{"page_id", "lease_id"}, withIDs(nil))},
	{Name: "browser_reload", Description: "Reload the exact page, optionally bypassing cache.", InputSchema: schema([]string{"page_id", "lease_id"}, withIDs(map[string]any{"ignore_cache": map[string]any{"type": "boolean"}}))},
	{Name: "browser_wait", Description: "Wait for text, a URL substring, document readiness, or a Page download; without a predicate, wait for timeout_ms. A timeout has an unknown page state; take a fresh snapshot afterwards.", InputSchema: schema([]string{"page_id", "lease_id"}, withIDs(map[string]any{"text": map[string]any{"type": "string", "minLength": 1}, "url_contains": map[string]any{"type": "string", "minLength": 1}, "ready_state": map[string]any{"type": "string", "enum": []string{"loading", "interactive", "complete"}}, "download_id": map[string]any{"type": "string", "minLength": 1}, "timeout_ms": map[string]any{"type": "integer", "minimum": 100, "maximum": 30000}}))},
	{Name: "browser_scroll", Description: "Scroll the exact page vertically, or scroll an element from the current snapshot into view.", InputSchema: schema([]string{"page_id", "lease_id"}, withIDs(map[string]any{"delta_y": map[string]any{"type": "number"}, "snapshot_id": map[string]any{"type": "string"}, "element_id": map[string]any{"type": "string"}}))},
}

type MCPHandler struct {
	service *Service
}

func (h *MCPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if r.URL.Path != "/mcp" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "MCP uses POST /mcp", http.StatusMethodNotAllowed)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "MCP requires application/json", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var request mcpRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.JSONRPC != "2.0" || request.Method == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "invalid JSON-RPC request"}})
		return
	}
	if len(request.ID) == 0 || string(request.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rpcErr := h.dispatch(r.Context(), request.Method, request.Params)
	response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
	if rpcErr != nil {
		response["error"] = rpcErr
	} else {
		response["result"] = result
	}
	writeJSON(w, http.StatusOK, response)
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (h *MCPHandler) dispatch(ctx context.Context, method string, raw json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(raw, &params)
		version := params.ProtocolVersion
		switch version {
		case "2024-11-05", "2025-03-26", "2025-06-18":
		default:
			version = "2025-03-26"
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "viewer-browser", "version": "0.2.0"},
			"instructions":    "Use only the random page_id explicitly supplied by the user. There is intentionally no page-list or current-page tool. Call browser_require first and pass its lease_id to every read/action tool. Never substitute another page on any error. Call browser_release when finished.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": mcpTools}, nil
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(raw, &params); err != nil || params.Name == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid tools/call params"}
		}
		result, err := h.callTool(ctx, params.Name, params.Arguments)
		if err != nil {
			payload := map[string]any{"ok": false, "error": err}
			text, _ := json.Marshal(payload)
			return map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}, "structuredContent": payload, "isError": true}, nil
		}
		structured := result
		var imageContent map[string]any
		if params.Name == "browser_screenshot" {
			if object, ok := result.(map[string]any); ok {
				copyObject := make(map[string]any, len(object))
				for key, value := range object {
					copyObject[key] = value
				}
				data := stringField(copyObject, "data")
				mimeType := stringField(copyObject, "mime_type")
				delete(copyObject, "data")
				structured = copyObject
				if data != "" && mimeType != "" {
					imageContent = map[string]any{"type": "image", "data": data, "mimeType": mimeType}
				}
			}
		}
		text, _ := json.Marshal(structured)
		content := []map[string]any{{"type": "text", "text": string(text)}}
		if imageContent != nil {
			content = append(content, imageContent)
		}
		return map[string]any{"content": content, "structuredContent": structured, "isError": false}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	}
}

func (h *MCPHandler) callTool(ctx context.Context, name string, args map[string]any) (any, error) {
	pageID := stringArg(args, "page_id")
	if pageID == "" {
		return nil, errorf("invalid_request", "page_id is required")
	}
	if name == "browser_status" {
		return h.service.Status(pageID)
	}
	if name == "browser_require" {
		return h.service.Require(pageID, stringArg(args, "lease_id"))
	}
	leaseID := stringArg(args, "lease_id")
	if leaseID == "" {
		return nil, errorf("invalid_request", "lease_id is required; call browser_require first")
	}
	if name == "browser_release" {
		if err := h.service.Release(pageID, leaseID); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "page_id": pageID, "released": true}, nil
	}
	payload := make(map[string]any, len(args))
	for key, value := range args {
		if key != "page_id" && key != "lease_id" {
			payload[key] = value
		}
	}
	timeout := 35 * time.Second
	if name == "browser_wait" {
		if ms, ok := numberArg(args, "timeout_ms"); ok {
			timeout = time.Duration(ms)*time.Millisecond + 5*time.Second
		}
	}
	for _, tool := range mcpTools {
		if tool.Name == name {
			return h.service.Execute(ctx, pageID, leaseID, strings.TrimPrefix(name, "browser_"), payload, timeout)
		}
	}
	return nil, errorf("unknown_tool", "unknown browser tool %q", name)
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func numberArg(args map[string]any, key string) (float64, bool) {
	value, ok := args[key].(float64)
	return value, ok
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
