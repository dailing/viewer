package pluginapi

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRegistryOrderAndCompleteness(t *testing.T) {
	ids := make([]string, 0, len(Registry))
	for _, entry := range Registry {
		ids = append(ids, entry.ID)
	}
	want := []string{"bus-inspector", "config-store", "viewer.agent-hermes", "viewer.agent-codex", "instance-store", "file-service", "chat", "terminal", "supervisor", "gateway"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("registry ids = %v, want %v", ids, want)
	}
}

func TestEnsureEmptyRegistryDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	if err := ensureEmptyRegistry(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("custom"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureEmptyRegistry(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "custom" {
		t.Fatalf("registry was overwritten: %q", data)
	}
}

type panicClosePlugin struct{}

func (panicClosePlugin) Start(context.Context) error { return nil }
func (panicClosePlugin) Wait(context.Context) error  { return nil }
func (panicClosePlugin) Close(context.Context) error { panic("close boom") }

func TestClosePluginRecoversPanic(t *testing.T) {
	if err := closePlugin(context.Background(), "panic-test", panicClosePlugin{}); err == nil {
		t.Fatal("expected recovered close panic error")
	}
}
