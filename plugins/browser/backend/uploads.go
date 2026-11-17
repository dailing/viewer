package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func validateUploadPaths(raw any) ([]string, error) {
	var values []any
	switch typed := raw.(type) {
	case []any:
		values = typed
	case []string:
		for _, value := range typed {
			values = append(values, value)
		}
	}
	if len(values) == 0 || len(values) > 16 {
		return nil, errorf("invalid_request", "paths must contain 1 to 16 files")
	}
	paths := make([]string, 0, len(values))
	for _, rawPath := range values {
		path, ok := rawPath.(string)
		if !ok || path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, errorf("invalid_path", "upload paths must be clean absolute paths")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, errorf("invalid_path", "upload path is not an accessible regular file: %s", path)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func (p *chromePage) upload(ctx context.Context, payload map[string]any) (map[string]any, error) {
	paths, err := validateUploadPaths(payload["paths"])
	if err != nil {
		return nil, err
	}
	if _, err := p.executeRuntimeForElement(ctx, "validate_upload", payload); err != nil {
		return nil, err
	}
	tab, frameID, err := p.snapshotElement(payload)
	if err != nil {
		return nil, err
	}
	frame, err := p.frameByID(tab, frameID)
	if err != nil {
		return nil, err
	}
	expression := fmt.Sprintf("globalThis.__viewerManagedBrowser.resolveElement(%s,%s)", strconvQuote(stringField(payload, "snapshot_id")), strconvQuote(stringField(payload, "element_id")))
	raw, err := tab.conn.callSession(ctx, frame.SessionID, "Runtime.evaluate", map[string]any{"expression": expression, "contextId": frame.ContextID, "returnByValue": false})
	if err != nil {
		return nil, err
	}
	var response struct {
		Result struct {
			ObjectID string `json:"objectId"`
			Subtype  string `json:"subtype"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if response.ExceptionDetails != nil {
		return nil, errors.New(response.ExceptionDetails.Text)
	}
	if response.Result.ObjectID == "" || response.Result.Subtype != "node" {
		return nil, errorf("stale_element", "File input element could not be resolved")
	}
	if _, err := tab.conn.callSession(ctx, frame.SessionID, "DOM.setFileInputFiles", map[string]any{"files": paths, "objectId": response.Result.ObjectID}); err != nil {
		return nil, err
	}
	return map[string]any{"uploaded": true, "paths": paths, "element_id": stringField(payload, "element_id")}, nil
}
