package agenthermes

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHermesHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got := hermesHome("default"); got != filepath.Join(home, ".hermes") {
		t.Fatalf("default profile home = %q", got)
	}
	if got := hermesHome(""); got != filepath.Join(home, ".hermes") {
		t.Fatalf("empty profile home = %q", got)
	}
	if got := hermesHome("work"); got != filepath.Join(home, ".hermes", "profiles", "work") {
		t.Fatalf("named profile home = %q", got)
	}
}

func TestReadProcessCheckpointMissing(t *testing.T) {
	entries, err := readProcessCheckpoint(filepath.Join(t.TempDir(), "no-such-home"))
	if err != nil || entries != nil {
		t.Fatalf("missing file: entries=%v err=%v", entries, err)
	}
}

func writeCheckpoint(t *testing.T, dir string, entries []processCheckpoint) string {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "processes.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLiveBackgroundProcesses(t *testing.T) {
	// A real live process to anchor the liveness check.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	defer cmd.Process.Kill()
	start, _, ok := procStatStartTime(cmd.Process.Pid)
	if !ok {
		t.Skip("no /proc start time")
	}
	home := t.TempDir()
	writeCheckpoint(t, home, []processCheckpoint{
		{SessionID: "proc-live", Command: "sleep 30", PID: cmd.Process.Pid, HostStartTime: start, ParentSessionID: "sess-a"},
		{SessionID: "proc-dead", Command: "true", PID: 1 << 22, ParentSessionID: "sess-a"},
		{SessionID: "proc-other", Command: "sleep 30", PID: cmd.Process.Pid, HostStartTime: start, ParentSessionID: "sess-b"},
		{SessionID: "proc-recycled", Command: "sleep 30", PID: cmd.Process.Pid, HostStartTime: 1, ParentSessionID: "sess-a"},
	})
	live, err := liveBackgroundProcesses(home, "sess-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].SessionID != "proc-live" || live[0].Command != "sleep 30" {
		t.Fatalf("live = %+v", live)
	}
	live, err = liveBackgroundProcesses(home, "sess-b")
	if err != nil || len(live) != 1 {
		t.Fatalf("sess-b live = %+v err=%v", live, err)
	}
	live, err = liveBackgroundProcesses(home, "sess-c")
	if err != nil || len(live) != 0 {
		t.Fatalf("sess-c live = %+v err=%v", live, err)
	}
}

func TestPidAlive(t *testing.T) {
	self := os.Getpid()
	if !pidAlive(self, 0) {
		t.Fatal("own pid not alive")
	}
	start, _, ok := procStatStartTime(self)
	if !ok {
		t.Skip("no /proc start time")
	}
	if !pidAlive(self, start) {
		t.Fatal("own pid with correct start time not alive")
	}
	if pidAlive(self, start+100000) {
		t.Fatal("recycled-pid fingerprint accepted")
	}
	if pidAlive(-1, 0) {
		t.Fatal("negative pid alive")
	}
	if pidAlive(1<<22, 0) {
		t.Fatal("implausible pid alive")
	}
}
