package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	base, cap := time.Second, 30*time.Second
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for index, expected := range want {
		if got := backoff(base, cap, index+1); got != expected {
			t.Fatalf("attempt %d: got %s, want %s", index+1, got, expected)
		}
	}
}

func TestNewLoadsOnlyEnabledRunnablePlugins(t *testing.T) {
	temp := t.TempDir()
	enabled := filepath.Join(temp, "enabled")
	disabled := filepath.Join(temp, "disabled")
	for _, path := range []string{enabled, disabled} {
		if err := os.MkdirAll(filepath.Join(path, "backend"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "backend", "run"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	falseValue := false
	contents, err := json.Marshal(registry{Plugins: []registryEntry{
		{ID: "enabled", Path: enabled},
		{ID: "disabled", Path: disabled, Enabled: &falseValue},
		{ID: "missing", Path: filepath.Join(temp, "missing")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(temp, "registry.json")
	if err := os.WriteFile(registryPath, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	plugin, err := New(Config{KernelWS: "ws://127.0.0.1:29399/ws", RegistryPath: registryPath})
	if err != nil {
		t.Fatal(err)
	}
	if len(plugin.managed) != 1 || plugin.managed["enabled"] == nil {
		t.Fatalf("managed plugins = %#v", plugin.managed)
	}
}
