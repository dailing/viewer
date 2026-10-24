// Background-process visibility for chat's line-hold feature.
//
// Hermes checkpoints every registry-tracked background process to
// <hermes home>/processes.json (crash recovery). Each entry carries the
// owning conversation as parent_session_id — the ACP session id — so this
// file is the provider-side source of truth for "does session X still have
// background work". The agent never pushes completion notifications to ACP
// clients (its completion queue has no consumer in acp mode), so chat polls
// this RPC instead: at turn end and on a ticker while a line is held.
package agenthermes

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"viewer/sdk/go/busclient"
)

// processCheckpoint mirrors the entry schema Hermes writes to
// processes.json (tools/process_registry.py _CHECKPOINT_FIELDS + session_id).
type processCheckpoint struct {
	SessionID       string  `json:"session_id"`
	Command         string  `json:"command"`
	PID             int     `json:"pid"`
	HostStartTime   int64   `json:"host_start_time"`
	ParentSessionID string  `json:"parent_session_id"`
	StartedAt       float64 `json:"started_at"`
}

// backgroundProcess is one live entry of the background RPC reply.
type backgroundProcess struct {
	SessionID string  `json:"session_id"`
	Command   string  `json:"command"`
	PID       int     `json:"pid"`
	StartedAt float64 `json:"started_at"`
}

// startTimeDriftTolerance mirrors Hermes's START_TIME_DRIFT_TOLERANCE
// (gateway/status.py): the recorded and live /proc start-time fingerprints
// describe the same process incarnation within this many clock ticks.
const startTimeDriftTolerance = 200

// hermesHome resolves the HERMES_HOME a profile uses: the default profile
// lives at ~/.hermes, named profiles at ~/.hermes/profiles/<name>. Must
// match how newClient passes the profile to `hermes -p <profile> acp`.
func hermesHome(profile string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "~"
	}
	base := filepath.Join(home, ".hermes")
	if profile == "" || profile == "default" {
		return base
	}
	return filepath.Join(base, "profiles", profile)
}

// readProcessCheckpoint loads the checkpoint file; a missing file means no
// tracked processes, not an error.
func readProcessCheckpoint(home string) ([]processCheckpoint, error) {
	data, err := os.ReadFile(filepath.Join(home, "processes.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries := []processCheckpoint{}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// procStatStartTime reads field 22 of /proc/<pid>/stat (start time in clock
// ticks since boot) the same way Hermes does (naive whitespace split), plus
// the process state letter so zombies report dead. Matching Hermes's parser
// exactly matters: the recorded host_start_time came from the same read.
func procStatStartTime(pid int) (start int64, zombie bool, ok bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 22 {
		return 0, false, false
	}
	value, err := strconv.ParseInt(fields[21], 10, 64)
	if err != nil {
		return 0, false, false
	}
	return value, fields[2] == "Z", true
}

// pidAlive reports whether the checkpoint entry still names a live process:
// signal-0 liveness, zombie exclusion, and a PID-reuse guard comparing the
// recorded start-time fingerprint against the live one (drift-tolerant,
// unreadable live value falls back to the signal-0 verdict like Hermes).
func pidAlive(pid int, hostStartTime int64) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		if err == syscall.EPERM {
			return true // exists but owned by someone else
		}
		return false
	}
	start, zombie, ok := procStatStartTime(pid)
	if !ok {
		return true // unreadable: trust signal-0, as Hermes does
	}
	if zombie {
		return false
	}
	if hostStartTime > 0 {
		delta := start - hostStartTime
		if delta < 0 {
			delta = -delta
		}
		if delta > startTimeDriftTolerance {
			return false // PID recycled by an unrelated process
		}
	}
	return true
}

// liveBackgroundProcesses returns the checkpoint entries owned by sessionID
// whose process is still alive.
func liveBackgroundProcesses(home, sessionID string) ([]backgroundProcess, error) {
	entries, err := readProcessCheckpoint(home)
	if err != nil {
		return nil, err
	}
	live := []backgroundProcess{}
	for _, entry := range entries {
		if entry.ParentSessionID != sessionID || !pidAlive(entry.PID, entry.HostStartTime) {
			continue
		}
		live = append(live, backgroundProcess{SessionID: entry.SessionID, Command: entry.Command, PID: entry.PID, StartedAt: entry.StartedAt})
	}
	return live, nil
}

// handleBackground answers chat's turn-end/watcher poll: which background
// processes of this session are still running. Unknown sessions fall back
// to the default profile home — sessions resident before a plugin restart
// lose their recorded home but the default covers the common case.
func (p *Plugin) handleBackground(frame busclient.Frame) {
	var request struct {
		SessionID string `json:"session_id"`
	}
	err := decodeFrame(frame, &request)
	if err == nil && strings.TrimSpace(request.SessionID) == "" {
		err = errors.New("session_id is required")
	}
	if err != nil {
		p.respond(frame, nil, err)
		return
	}
	p.mu.Lock()
	current := p.sessions[request.SessionID]
	p.mu.Unlock()
	home := hermesHome("default")
	if current != nil && current.home != "" {
		home = current.home
	}
	live, err := liveBackgroundProcesses(home, request.SessionID)
	if err != nil {
		log.Printf("viewer-agent-hermes background checkpoint read failed session=%s home=%s: %v", request.SessionID, home, err)
		p.respond(frame, nil, err)
		return
	}
	p.respond(frame, map[string]any{"processes": live}, nil)
}
