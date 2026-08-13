// Package supervisor implements the C0 plugin process supervisor.
package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"viewer/internal/busclient"
)

const (
	StateStarting = "starting"
	StateRunning  = "running"
	StateCrashed  = "crashed"
	StateBroken   = "broken"
	StateStopped  = "stopped"
)

var Manifest = busclient.Manifest{
	ID: "supervisor", Version: "0.1.0",
	Slots: map[string]any{"restart": map[string]any{}, "states": map[string]any{}},
	Emits: map[string]any{"states": map[string]any{}, "lifecycle": map[string]any{}},
}

type Config struct {
	KernelWS         string
	RegistryPath     string
	LogDir           string
	BackoffBase      time.Duration
	BackoffCap       time.Duration
	BreakerMaxCrash  int
	BreakerWindow    time.Duration
	TerminationGrace time.Duration
}

func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		LogDir: filepath.Join(home, ".view", "logs"), BackoffBase: time.Second,
		BackoffCap: 30 * time.Second, BreakerMaxCrash: 5,
		BreakerWindow: 60 * time.Second, TerminationGrace: 2 * time.Second,
	}
}

type registry struct {
	Plugins []registryEntry `json:"plugins"`
}

type registryEntry struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Enabled *bool  `json:"enabled"`
}

type managedPlugin struct {
	opMu          sync.Mutex
	id, path      string
	cmd           *exec.Cmd
	state         string
	exitCode      *int
	crashes       []time.Time
	generation    uint64
	done          chan struct{}
	restartCancel context.CancelFunc
}

type State struct {
	State    string `json:"state"`
	PID      *int   `json:"pid"`
	ExitCode *int   `json:"exit_code"`
	Crashes  int    `json:"crashes"`
}

type Plugin struct {
	config Config
	client *busclient.Client

	mu       sync.Mutex
	managed  map[string]*managedPlugin
	stopping bool
}

func New(config Config) (*Plugin, error) {
	defaults := DefaultConfig()
	if config.KernelWS == "" || config.RegistryPath == "" {
		return nil, errors.New("kernel websocket and registry path are required")
	}
	if config.LogDir == "" {
		config.LogDir = defaults.LogDir
	}
	if config.BackoffBase <= 0 {
		config.BackoffBase = defaults.BackoffBase
	}
	if config.BackoffCap <= 0 {
		config.BackoffCap = defaults.BackoffCap
	}
	if config.BreakerMaxCrash <= 0 {
		config.BreakerMaxCrash = defaults.BreakerMaxCrash
	}
	if config.BreakerWindow <= 0 {
		config.BreakerWindow = defaults.BreakerWindow
	}
	if config.TerminationGrace <= 0 {
		config.TerminationGrace = defaults.TerminationGrace
	}

	data, err := os.ReadFile(config.RegistryPath)
	if err != nil {
		return nil, fmt.Errorf("read registry: %w", err)
	}
	var decoded registry
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, fmt.Errorf("decode registry: %w", err)
	}
	managed := make(map[string]*managedPlugin)
	for _, entry := range decoded.Plugins {
		if entry.Enabled != nil && !*entry.Enabled {
			continue
		}
		if entry.ID == "" || entry.Path == "" {
			return nil, errors.New("enabled registry entries require id and path")
		}
		run := filepath.Join(entry.Path, "backend", "run")
		info, statErr := os.Stat(run)
		if statErr != nil || info.IsDir() {
			slog.Error("plugin has no backend/run", "plugin", entry.ID, "path", run)
			continue
		}
		managed[entry.ID] = &managedPlugin{id: entry.ID, path: entry.Path, state: StateStopped}
	}
	return &Plugin{config: config, managed: managed}, nil
}

func (p *Plugin) Run(ctx context.Context) error {
	if err := p.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	p.Close()
	return nil
}

// Start connects the supervisor and starts the configured external plugins.
func (p *Plugin) Start(ctx context.Context) error {
	return p.StartWithManaged(ctx, os.Getenv("VIEWER_MANAGED") == "1")
}

// StartWithManaged is Start with an explicit hello managed flag. The assembled
// runtime passes false; standalone supervised processes derive it from the env.
func (p *Plugin) StartWithManaged(ctx context.Context, managed bool) error {
	if err := os.MkdirAll(p.config.LogDir, 0o755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	p.client = busclient.New(p.config.KernelWS, Manifest, busclient.WithManaged(managed))
	if _, err := p.client.Subscribe("plugins:_:list", p.trackRegistry); err != nil {
		return err
	}
	if _, err := p.client.Subscribe("supervisor:_:restart", p.restartRPC); err != nil {
		return err
	}
	if err := p.client.Connect(ctx); err != nil {
		return fmt.Errorf("connect supervisor: %w", err)
	}

	p.mu.Lock()
	plugins := make([]*managedPlugin, 0, len(p.managed))
	for _, item := range p.managed {
		plugins = append(plugins, item)
	}
	p.mu.Unlock()
	for _, item := range plugins {
		if err := p.spawn(item); err != nil {
			p.recordSpawnFailure(item, err)
		}
	}
	p.publishStates()
	return nil
}

func (p *Plugin) Close() { p.shutdown() }

func (p *Plugin) spawn(item *managedPlugin) error {
	item.opMu.Lock()
	defer item.opMu.Unlock()
	return p.spawnLocked(item)
}

func (p *Plugin) spawnLocked(item *managedPlugin) error {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return context.Canceled
	}
	item.generation++
	generation := item.generation
	item.restartCancel = nil
	p.mu.Unlock()

	logPath := filepath.Join(p.config.LogDir, item.id+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", logPath, err)
	}
	cmd := exec.Command(filepath.Join(item.path, "backend", "run"), "--kernel-ws", p.config.KernelWS)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.Env = append(os.Environ(), "VIEWER_MANAGED=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start %s: %w", item.id, err)
	}

	p.mu.Lock()
	if p.stopping || item.generation != generation {
		p.mu.Unlock()
		_ = signalProcessGroup(cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Wait()
		_ = logFile.Close()
		return context.Canceled
	}
	item.cmd, item.done = cmd, make(chan struct{})
	item.state, item.exitCode = StateStarting, nil
	done := item.done
	p.mu.Unlock()
	slog.Info("spawned plugin", "plugin", item.id, "pid", cmd.Process.Pid)
	p.publishStates()
	go p.waitProcess(item, cmd, generation, done, logFile)
	return nil
}

func (p *Plugin) waitProcess(item *managedPlugin, cmd *exec.Cmd, generation uint64, done chan struct{}, logFile *os.File) {
	err := cmd.Wait()
	_ = logFile.Close()
	close(done)
	exitCode := cmd.ProcessState.ExitCode()
	p.mu.Lock()
	if p.stopping || item.generation != generation || item.cmd != cmd {
		p.mu.Unlock()
		return
	}
	item.exitCode = &exitCode
	now := time.Now()
	item.crashes = append(item.crashes, now)
	cutoff := now.Add(-p.config.BreakerWindow)
	first := 0
	for first < len(item.crashes) && item.crashes[first].Before(cutoff) {
		first++
	}
	item.crashes = append([]time.Time(nil), item.crashes[first:]...)
	broken := len(item.crashes) >= p.config.BreakerMaxCrash
	if broken {
		item.state = StateBroken
	} else {
		item.state = StateCrashed
	}
	attempt := len(item.crashes)
	p.mu.Unlock()
	slog.Warn("plugin exited", "plugin", item.id, "exit_code", exitCode, "error", err)
	p.publishLifecycle(item.id, "crashed", map[string]any{"exit_code": exitCode})
	p.publishStates()
	if !broken {
		p.scheduleRestart(item, backoff(p.config.BackoffBase, p.config.BackoffCap, attempt))
	}
}

func (p *Plugin) recordSpawnFailure(item *managedPlugin, err error) {
	slog.Error("plugin spawn failed", "plugin", item.id, "error", err)
	now := time.Now()
	p.mu.Lock()
	item.exitCode = nil
	item.crashes = append(item.crashes, now)
	cutoff := now.Add(-p.config.BreakerWindow)
	first := 0
	for first < len(item.crashes) && item.crashes[first].Before(cutoff) {
		first++
	}
	item.crashes = append([]time.Time(nil), item.crashes[first:]...)
	item.state = StateCrashed
	if len(item.crashes) >= p.config.BreakerMaxCrash {
		item.state = StateBroken
	}
	broken, attempt := item.state == StateBroken, len(item.crashes)
	p.mu.Unlock()
	p.publishLifecycle(item.id, "crashed", map[string]any{"exit_code": nil})
	p.publishStates()
	if !broken {
		p.scheduleRestart(item, backoff(p.config.BackoffBase, p.config.BackoffCap, attempt))
	}
}

func backoff(base, cap time.Duration, attempt int) time.Duration {
	delay := base
	for i := 1; i < attempt && delay < cap; i++ {
		if delay > cap/2 {
			return cap
		}
		delay *= 2
	}
	if delay > cap {
		return cap
	}
	return delay
}

func (p *Plugin) scheduleRestart(item *managedPlugin, delay time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	if p.stopping || item.state == StateBroken {
		p.mu.Unlock()
		cancel()
		return
	}
	if item.restartCancel != nil {
		item.restartCancel()
	}
	item.restartCancel = cancel
	p.mu.Unlock()
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return
		}
		if err := p.spawn(item); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			p.recordSpawnFailure(item, err)
			return
		}
		p.publishLifecycle(item.id, "restarted", map[string]any{"pid": p.pid(item)})
	}()
}

func (p *Plugin) trackRegistry(frame busclient.Frame) {
	entries, _ := frame.Value.([]any)
	online := make(map[string]bool)
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		manifest, _ := entry["manifest"].(map[string]any)
		id, _ := manifest["id"].(string)
		if id == "" {
			id, _ = entry["id"].(string)
		}
		online[id] = true
	}
	changed := false
	p.mu.Lock()
	for id, item := range p.managed {
		if item.state == StateStarting && online[id] {
			item.state, item.crashes = StateRunning, nil
			changed = true
		}
	}
	p.mu.Unlock()
	if changed {
		p.publishStates()
	}
}

func (p *Plugin) restartRPC(frame busclient.Frame) {
	value, _ := frame.Value.(map[string]any)
	if value["_cancel"] == true {
		return
	}
	id, _ := value["id"].(string)
	p.mu.Lock()
	item := p.managed[id]
	p.mu.Unlock()
	if item == nil {
		p.respondError(value, "not_found", "no such managed plugin: "+id)
		return
	}
	go func() {
		item.opMu.Lock()
		defer item.opMu.Unlock()
		p.stopOneLocked(item, false)
		p.mu.Lock()
		item.crashes = nil
		p.mu.Unlock()
		if err := p.spawnLocked(item); err != nil {
			p.respondError(value, "restart_failed", err.Error())
			return
		}
		pid := p.pid(item)
		p.publishLifecycle(item.id, "restarted", map[string]any{"pid": pid})
		p.respond(value, map[string]any{"id": item.id, "pid": pid})
	}()
}

func (p *Plugin) stopOne(item *managedPlugin, final bool) {
	item.opMu.Lock()
	defer item.opMu.Unlock()
	p.stopOneLocked(item, final)
}

func (p *Plugin) stopOneLocked(item *managedPlugin, final bool) {
	p.mu.Lock()
	if item.restartCancel != nil {
		item.restartCancel()
		item.restartCancel = nil
	}
	item.generation++
	cmd, done := item.cmd, item.done
	p.mu.Unlock()
	if cmd != nil && cmd.Process != nil && cmd.ProcessState == nil {
		_ = signalProcessGroup(cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(p.config.TerminationGrace):
			_ = signalProcessGroup(cmd.Process.Pid, syscall.SIGKILL)
			if done != nil {
				<-done
			}
		}
		// The direct child may exit after SIGTERM while leaving descendants in
		// its process group. Remove any such orphaned subtree before respawn.
		_ = signalProcessGroup(cmd.Process.Pid, syscall.SIGKILL)
	}
	if final {
		p.mu.Lock()
		item.state = StateStopped
		p.mu.Unlock()
	}
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(-pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (p *Plugin) shutdown() {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return
	}
	p.stopping = true
	items := make([]*managedPlugin, 0, len(p.managed))
	for _, item := range p.managed {
		if item.restartCancel != nil {
			item.restartCancel()
			item.restartCancel = nil
		}
		items = append(items, item)
	}
	p.mu.Unlock()
	var wg sync.WaitGroup
	for _, item := range items {
		wg.Add(1)
		go func(m *managedPlugin) { defer wg.Done(); p.stopOne(m, true) }(item)
	}
	wg.Wait()
	if p.client != nil {
		_ = p.client.Close()
	}
}

func (p *Plugin) pid(item *managedPlugin) *int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if item.cmd == nil || item.cmd.Process == nil {
		return nil
	}
	pid := item.cmd.Process.Pid
	return &pid
}

func (p *Plugin) publishStates() {
	if p.client == nil || !p.client.Connected() {
		return
	}
	p.mu.Lock()
	states := make(map[string]State, len(p.managed))
	for id, item := range p.managed {
		var pid *int
		if item.cmd != nil && item.cmd.Process != nil {
			value := item.cmd.Process.Pid
			pid = &value
		}
		states[id] = State{State: item.state, PID: pid, ExitCode: item.exitCode, Crashes: len(item.crashes)}
	}
	p.mu.Unlock()
	if err := p.client.Set(context.Background(), "supervisor:_:states", states); err != nil {
		slog.Warn("publish supervisor states", "error", err)
	}
}

func (p *Plugin) publishLifecycle(id, state string, extra map[string]any) {
	if p.client == nil || !p.client.Connected() {
		return
	}
	value := map[string]any{"state": state}
	for key, item := range extra {
		value[key] = item
	}
	if err := p.client.Publish(context.Background(), "plugins:"+id+":lifecycle", value); err != nil {
		slog.Warn("publish lifecycle", "error", err)
	}
}

func (p *Plugin) respond(request map[string]any, result any) {
	reply, replyOK := request["_reply_to"].(string)
	corr, corrOK := request["_corr"].(string)
	if !replyOK || !corrOK {
		return
	}
	_ = p.client.Publish(context.Background(), reply, map[string]any{"_corr": corr, "ok": true, "result": result})
}

func (p *Plugin) respondError(request map[string]any, code, message string) {
	reply, replyOK := request["_reply_to"].(string)
	corr, corrOK := request["_corr"].(string)
	if !replyOK || !corrOK {
		return
	}
	_ = p.client.Publish(context.Background(), reply, map[string]any{"_corr": corr, "ok": false, "error": map[string]any{"code": code, "message": message}})
}
