package supervisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"viewer/internal/kernel"
	"viewer/sdk/go/busclient"
)

// startTestManager brings up a real kernel + supervisor with a fast backoff
// and returns a caller client for the manager RPCs.
func startTestManager(t *testing.T, registryPath string) *busclient.Client {
	t.Helper()
	kernelConfig := kernel.DefaultConfig()
	kernelConfig.Host, kernelConfig.Port = "127.0.0.1", 0
	kernelServer := kernel.New(kernelConfig)
	if err := kernelServer.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kernelServer.Shutdown(context.Background()) })
	kernelURL := fmt.Sprintf("ws://127.0.0.1:%d/ws", kernelServer.Port())

	if _, err := os.Stat(registryPath); os.IsNotExist(err) {
		if err := os.WriteFile(registryPath, []byte("{\n  \"plugins\": []\n}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plugin, err := New(Config{
		KernelWS: kernelURL, RegistryPath: registryPath, LogDir: t.TempDir(),
		BackoffBase: 50 * time.Millisecond, BackoffCap: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := plugin.StartWithManaged(ctx, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(plugin.Close)

	caller := busclient.New(kernelURL, busclient.Manifest{ID: "tester", Version: "0.1.0", Slots: map[string]any{}, Emits: map[string]any{}})
	if err := caller.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = caller.Close() })
	return caller
}

func makePluginDir(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backend", "run"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func listEntry(t *testing.T, caller *busclient.Client, id string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reply, err := caller.Request(ctx, "supervisor:_:list", map[string]any{}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range reply.(map[string]any)["plugins"].([]any) {
		entry := raw.(map[string]any)
		if entry["id"] == id {
			return entry
		}
	}
	return nil
}

func waitForState(t *testing.T, caller *busclient.Client, id, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		entry := listEntry(t, caller, id)
		if entry != nil && entry["state"] == want {
			return entry
		}
		if time.Now().After(deadline) {
			t.Fatalf("plugin %s never reached state %s: %#v", id, want, entry)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

func TestManagerLifecycleAndRetry(t *testing.T) {
	registryPath := filepath.Join(t.TempDir(), "registry.json")
	caller := startTestManager(t, registryPath)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	crasher := makePluginDir(t, "#!/bin/sh\nexit 1\n")
	sleeper := makePluginDir(t, "#!/bin/sh\nsleep 30\n")

	// upsert registers both; neither auto-starts (manual launch default).
	if _, err := caller.Request(ctx, "supervisor:_:upsert", map[string]any{"id": "crasher", "path": crasher}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := caller.Request(ctx, "supervisor:_:upsert", map[string]any{"id": "sleeper", "path": sleeper, "name": "Sleeper", "command": []string{"backend/run"}}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if entry := listEntry(t, caller, "sleeper"); entry == nil || entry["state"] != "stopped" || entry["name"] != "Sleeper" {
		t.Fatalf("sleeper entry=%#v", entry)
	}
	// The registry file persisted both entries for the next boot.
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !containsAll(text, "crasher", "sleeper") {
		t.Fatalf("registry.json missing entries: %s", text)
	}

	// start runs the crasher; after 3 retries it lands on broken.
	if _, err = caller.Request(ctx, "supervisor:_:start", map[string]any{"id": "crasher"}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	entry := waitForState(t, caller, "crasher", "broken")
	if crashes, _ := entry["crashes"].(float64); crashes != 4 {
		t.Fatalf("expected 4 consecutive failures (run + 3 retries), got %#v", entry["crashes"])
	}

	// start/stop on the sleeper works and stop is final (no restart).
	if _, err = caller.Request(ctx, "supervisor:_:start", map[string]any{"id": "sleeper"}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if entry = waitForState(t, caller, "sleeper", "starting"); entry["pid"] == nil {
		t.Fatalf("sleeper should have a pid: %#v", entry)
	}
	if _, err = caller.Request(ctx, "supervisor:_:stop", map[string]any{"id": "sleeper"}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitForState(t, caller, "sleeper", "stopped")
	time.Sleep(300 * time.Millisecond)
	if entry = listEntry(t, caller, "sleeper"); entry["state"] != "stopped" {
		t.Fatalf("stopped plugin must stay stopped: %#v", entry)
	}

	// delete removes the entry and persists.
	if _, err = caller.Request(ctx, "supervisor:_:delete", map[string]any{"id": "sleeper"}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if entry = listEntry(t, caller, "sleeper"); entry != nil {
		t.Fatalf("deleted plugin still listed: %#v", entry)
	}
	if data, err = os.ReadFile(registryPath); err != nil || containsAll(string(data), "sleeper") {
		t.Fatalf("registry.json should drop sleeper: %s err=%v", data, err)
	}
}

func TestManagerAutostart(t *testing.T) {
	temp := t.TempDir()
	sleeper := makePluginDir(t, "#!/bin/sh\nsleep 30\n")
	registryPath := filepath.Join(temp, "registry.json")
	autostart := true
	contents := []byte(fmt.Sprintf("{\n  \"plugins\": [{\"id\": \"sleeper\", \"path\": %q, \"autostart\": %t}]\n}\n", sleeper, autostart))
	if err := os.WriteFile(registryPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	caller := startTestManager(t, registryPath)
	waitForState(t, caller, "sleeper", "starting")
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		found := false
		for i := 0; i+len(part) <= len(text); i++ {
			if text[i:i+len(part)] == part {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
