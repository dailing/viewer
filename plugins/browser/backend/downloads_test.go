package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadTrackerCompletionAndIsolation(t *testing.T) {
	root := t.TempDir()
	tracker := newDownloadTracker(root)
	id := tracker.begin("pg_one", map[string]any{"guid": "guid-one", "url": "http://example.test/file", "suggestedFilename": "file.txt"})
	if id != "guid-one" {
		t.Fatalf("download id = %q", id)
	}
	if err := os.WriteFile(filepath.Join(root, id), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	tracker.progress(map[string]any{"guid": id, "state": "completed", "receivedBytes": 7, "totalBytes": 7})
	result, err := tracker.wait(context.Background(), "pg_one", id)
	if err != nil {
		t.Fatal(err)
	}
	if result["state"] != "completed" || result["path"] != filepath.Join(root, "pg_one", id) {
		t.Fatalf("completed download = %#v", result)
	}
	if _, err := tracker.wait(context.Background(), "pg_two", id); err == nil {
		t.Fatal("cross-page download unexpectedly visible")
	} else {
		requireErrorCode(t, err, "download_not_found")
	}
}

func TestDownloadTrackerCancellation(t *testing.T) {
	tracker := newDownloadTracker(t.TempDir())
	tracker.begin("pg_one", map[string]any{"guid": "guid-cancel"})
	tracker.progress(map[string]any{"guid": "guid-cancel", "state": "canceled"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := tracker.wait(ctx, "pg_one", "guid-cancel"); err == nil {
		t.Fatal("canceled download unexpectedly succeeded")
	} else {
		requireErrorCode(t, err, "download_failed")
	}
}

func TestValidateUploadPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(path, []byte("upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := validateUploadPaths([]any{path})
	if err != nil || len(paths) != 1 {
		t.Fatalf("paths = %#v, %v", paths, err)
	}
	for _, invalid := range []any{[]any{}, []any{"relative.txt"}, []any{path + "/.."}, []any{filepath.Dir(path)}} {
		if _, err := validateUploadPaths(invalid); err == nil {
			t.Fatalf("invalid paths accepted: %#v", invalid)
		}
	}
}
