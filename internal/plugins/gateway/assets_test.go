package gateway

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"viewer/internal/kernel"
	"viewer/sdk/go/busclient"
)

// testAssetsServer builds a gateway with the asset pipeline against a real
// kernel (bus RPC round trips included) on ephemeral ports.
func testAssetsServer(t *testing.T) (*Server, *busclient.Client, string) {
	t.Helper()
	kernelConfig := kernel.DefaultConfig()
	kernelConfig.Host, kernelConfig.Port = "127.0.0.1", 0
	kernelServer := kernel.New(kernelConfig)
	if err := kernelServer.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kernelServer.Shutdown(context.Background()) })
	kernelURL := fmt.Sprintf("ws://127.0.0.1:%d/ws", kernelServer.Port())

	server := New(Config{KernelWS: kernelURL, Host: "127.0.0.1", Port: 0, AssetsDir: t.TempDir()})
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	caller := busclient.New(kernelURL, busclient.Manifest{ID: "my-plugin", Version: "0.1.0", Slots: map[string]any{}, Emits: map[string]any{}})
	if err := caller.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = caller.Close() })
	return server, caller, kernelURL
}

func TestAssetsPushServeAndReload(t *testing.T) {
	server, caller, kernelURL := testAssetsServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// A watcher sees every plugins:_:assets mailbox update.
	var watchMu sync.Mutex
	var lastAssets map[string]any
	watcher := busclient.New(kernelURL, busclient.Manifest{ID: "watcher", Version: "0.1.0", Slots: map[string]any{}, Emits: map[string]any{}})
	if _, err := watcher.Subscribe("plugins:_:assets", func(frame busclient.Frame) {
		if value, ok := frame.Value.(map[string]any); ok {
			watchMu.Lock()
			lastAssets = value
			watchMu.Unlock()
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := watcher.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = watcher.Close() }()

	bundle := []byte("export default { id: 'my-plugin' };\n")
	reply, err := caller.Request(ctx, "gateway:_:assets:push", map[string]any{
		"manifest": map[string]any{"name": "My Plugin", "icon": "bi-puzzle"},
		"files": []map[string]any{
			{"path": "frontend.js", "data_b64": base64.StdEncoding.EncodeToString(bundle)},
			{"path": "assets/style.css", "data_b64": base64.StdEncoding.EncodeToString([]byte("body{}"))},
		},
	}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := reply.(map[string]any)
	if !ok || entry["entry"] != "frontend.js" {
		t.Fatalf("reply=%#v", reply)
	}
	url := entry["url"].(string) + "frontend.js"

	// The bundle is served same-origin with immutable caching.
	recorder := httptest.NewRecorder()
	server.serveHTTP(recorder, httptest.NewRequest(http.MethodGet, url, nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != string(bundle) {
		t.Fatalf("GET %s: code=%d body=%q", url, recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") == "" || recorder.Header().Get("Content-Type") == "" {
		t.Fatalf("headers=%v", recorder.Header())
	}
	// Traversal and unknown hashes are rejected.
	for _, bad := range []string{"/plugins/my-plugin/assets/" + entry["hash"].(string) + "/../entry.json", "/plugins/my-plugin/assets/0000000000000000/frontend.js", "/plugins/../etc/passwd"} {
		rec := httptest.NewRecorder()
		server.serveHTTP(rec, httptest.NewRequest(http.MethodGet, bad, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s: code=%d", bad, rec.Code)
		}
	}

	// The mailbox update reached subscribers with the manifest attached.
	deadline := time.Now().Add(3 * time.Second)
	for {
		watchMu.Lock()
		current := lastAssets
		watchMu.Unlock()
		if entry, ok := current["my-plugin"].(map[string]any); ok {
			manifest, _ := entry["manifest"].(map[string]any)
			if manifest["name"] == "My Plugin" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("mailbox never carried my-plugin: %#v", current)
		}
		time.Sleep(20 * time.Millisecond)
	}

	server.assetsState.mu.Lock()
	stored := server.assetsState.assets["my-plugin"]
	server.assetsState.mu.Unlock()
	if stored == nil || stored.Manifest["name"] != "My Plugin" {
		t.Fatalf("stored=%#v", stored)
	}

	// A fresh server over the same assets dir rebuilds the map from disk.
	reloaded := New(Config{KernelWS: "ws://127.0.0.1:1/ws", AssetsDir: server.config.AssetsDir})
	reloaded.assetsState = &assetsState{assets: map[string]*assetEntry{}, uploads: map[string]*uploadSession{}}
	reloaded.loadAssetsFromDisk()
	if reloaded.assetsState.assets["my-plugin"] == nil || reloaded.assetsState.assets["my-plugin"].Hash != stored.Hash {
		t.Fatalf("reload failed: %#v", reloaded.assetsState.assets)
	}

	// assets:remove drops the entry and deletes the on-disk library.
	removeReply, err := caller.Request(ctx, "gateway:_:assets:remove", map[string]any{"id": "my-plugin"}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if removed, _ := removeReply.(map[string]any)["removed"].(bool); !removed {
		t.Fatalf("removeReply=%#v", removeReply)
	}
	if _, statErr := os.Stat(filepath.Join(server.config.AssetsDir, "my-plugin")); !os.IsNotExist(statErr) {
		t.Fatalf("assets dir should be gone, stat err=%v", statErr)
	}
}

func TestAssetsPushChunkedAndValidation(t *testing.T) {
	server, caller, _ := testAssetsServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	begin, err := caller.Request(ctx, "gateway:_:assets:push", map[string]any{"op": "begin"}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	uploadID := begin.(map[string]any)["upload_id"].(string)
	content := []byte("chunked bundle content\n")
	if _, err = caller.Request(ctx, "gateway:_:assets:push", map[string]any{"op": "file", "upload_id": uploadID, "path": "frontend.js", "data_b64": base64.StdEncoding.EncodeToString(content)}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// Intra-file chunking: append:true continues the previous file op.
	tail := []byte("// appended chunk\n")
	if _, err = caller.Request(ctx, "gateway:_:assets:push", map[string]any{"op": "file", "upload_id": uploadID, "path": "frontend.js", "append": true, "data_b64": base64.StdEncoding.EncodeToString(tail)}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	entry, err := caller.Request(ctx, "gateway:_:assets:push", map[string]any{"op": "commit", "upload_id": uploadID}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	url := entry.(map[string]any)["url"].(string) + "frontend.js"
	recorder := httptest.NewRecorder()
	server.serveHTTP(recorder, httptest.NewRequest(http.MethodGet, url, nil))
	if recorder.Body.String() != string(content)+string(tail) {
		t.Fatalf("chunked content mismatch: %q", recorder.Body.String())
	}
	// Committing an unknown upload fails cleanly.
	if _, err = caller.Request(ctx, "gateway:_:assets:push", map[string]any{"op": "commit", "upload_id": uploadID}, 5*time.Second); err == nil {
		t.Fatal("second commit should fail")
	}
	// Missing entry file is rejected.
	if _, err = caller.Request(ctx, "gateway:_:assets:push", map[string]any{"files": []map[string]any{{"path": "other.js", "data_b64": base64.StdEncoding.EncodeToString(content)}}}, 5*time.Second); err == nil {
		t.Fatal("push without frontend.js should fail")
	}
	// Bad asset paths are rejected.
	if _, err = caller.Request(ctx, "gateway:_:assets:push", map[string]any{"files": []map[string]any{{"path": "../evil.js", "data_b64": base64.StdEncoding.EncodeToString(content)}}}, 5*time.Second); err == nil {
		t.Fatal("traversal path should fail")
	}
}
