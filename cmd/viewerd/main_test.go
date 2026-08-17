package main

import (
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureLoggingWritesDataDirFile(t *testing.T) {
	previousSlog := slog.Default()
	previousWriter, previousFlags := log.Writer(), log.Flags()
	defer func() {
		slog.SetDefault(previousSlog)
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	}()

	dataDir := t.TempDir()
	file, err := configureLogging(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	slog.Info("structured-log-probe", "turn_id", "turn-test")
	log.Print("standard-log-probe")
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(dataDir, "viewerd.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"structured-log-probe", `"turn_id":"turn-test"`, "standard-log-probe"} {
		if !strings.Contains(string(contents), expected) {
			t.Fatalf("viewerd.log missing %q: %s", expected, contents)
		}
	}
}
