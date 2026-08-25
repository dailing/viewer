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

	"viewer/sdk/go/busclient"
)

const (
	StateStarting = "starting"
	StateRunning  = "running"
	StateCrashed  = "crashed"
	StateBroken   = "broken"
	StateStopped  = "stopped"
)

var Manifest = busclient.Manifest{
	ID: "supervisor", Version: "0.2.0",
	Slots: map[string]any{"restart": map[string]any{}, "states": map[string]any{}, "list": map[string]any{}, "upsert": map[string]any{}, "delete": map[string]any{}, "start": map[string]any{}, "stop": map[string]any{}},
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
		BackoffCap: 30 * time.Second, BreakerMaxCrash: 3,
		BreakerWindow: 60 * time.Second, TerminationGrace: 2 * time.Second,
	}
}

type registry struct {
	Plugins []registryEntry `json:"plugins"`
}

// registryEntry is one managed external plugin. Command overrides the
// default launch (<path>/backend/run); "--kernel-ws <addr>" is always
// appended (fixed launch ABI, framework section 14.3). Autostart defaults to
// false: external plugins start manually from the manager pane.
type registryEntry struct {
	ID        string   `json:"id"`
	Name      string   `json:"name,omitempty"`
	Path      string   `json:"path"`
	Command   []string `json:"command,omitempty"`
	Enabled   *bool    `json:"enabled"`
	Autostart *bool    `json:"autostart,omitempty"`
}

type managedPlugin struct {
	opMu          sync.Mutex
	id, path      string
	entry         registryEntry
	cmd           *exec.Cmd
	state         string
	exitCode      *int
	crashes       []time.Time
	startedAt     time.Time
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

	// registered holds every registry.json id (including disabled entries)
	// so the orphan-assets sweep never collects a merely disabled plugin.
	registered map[string]bool
	// online/assetIDs are the latest plugins:_:list and plugins:_:assets
	// mailbox snapshots, kept for the one-shot orphan-assets sweep.
	online   map[string]bool
	assetIDs map[string]bool
	// gcCancel stops the orphan-assets sweep; the sweep cannot use the Start
	// ctx because the assembly cancels it as soon as Start returns.
	gcCancel context.CancelFunc
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
	registered := make(map[string]bool, len(decoded.Plugins))
	for _, entry := range decoded.Plugins {
		if entry.ID != "" {
			registered[entry.ID] = true
		}
		if entry.Enabled != nil && !*entry.Enabled {
			continue
		}
		if entry.ID == "" || entry.Path == "" {
			return nil, errors.New("enabled registry entries require id and path")
		}
		if len(entry.Command) == 0 {
			run := filepath.Join(entry.Path, "backend", "run")
			info, statErr := os.Stat(run)
			if statErr != nil || info.IsDir() {
				slog.Error("plugin has no backend/run", "plugin", entry.ID, "path", run)
				continue
			}
		}
		managed[entry.ID] = &managedPlugin{id: entry.ID, path: entry.Path, entry: entry, state: StateStopped}
	}
	return &Plugin{config: config, managed: managed, registered: registered}, nil
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
	for pattern, handler := range map[string]func(busclient.Frame){
		"supervisor:_:list": p.listRPC, "supervisor:_:upsert": p.upsertRPC,
		"supervisor:_:delete": p.deleteRPC, "supervisor:_:start": p.startRPC,
		"supervisor:_:stop": p.stopRPC,
	} {
		if _, err := p.client.Subscribe(pattern, handler); err != nil {
			return err
		}
	}
	if err := p.client.Connect(ctx); err != nil {
		return fmt.Errorf("connect supervisor: %w", err)
	}
	if err := p.startAssetsGC(); err != nil {
		// Non-fatal: orphaned assets are a hygiene issue, not a startup blocker.
		slog.Warn("orphan-assets sweep unavailable", "error", err)
	}

	p.mu.Lock()
	plugins := make([]*managedPlugin, 0, len(p.managed))
	for _, item := range p.managed {
		if item.entry.Autostart != nil && *item.entry.Autostart {
			plugins = append(plugins, item)
		}
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
	// Launch ABI: entry command or the default backend/run; the kernel
	// address is always appended as the fixed --kernel-ws argument. Relative
	// executables resolve against the plugin directory, not our cwd.
	argv := item.entry.Command
	if len(argv) == 0 {
		argv = []string{filepath.Join(item.path, "backend", "run")}
	}
	executable := argv[0]
	if !filepath.IsAbs(executable) {
		executable = filepath.Join(item.path, executable)
	}
	args := append(append([]string{}, argv[1:]...), "--kernel-ws", p.config.KernelWS)
	cmd := exec.Command(executable, args...)
	cmd.Dir = item.path
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
	item.startedAt = time.Now()
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
	// Retry counter: consecutive crashes only — a run that stayed up past
	// BreakerWindow resets it. Retries stop after BreakerMaxCrash (default
	// 3) consecutive failures and the plugin is marked broken.
	if !item.startedAt.IsZero() && now.Sub(item.startedAt) >= p.config.BreakerWindow {
		item.crashes = nil
	}
	item.crashes = append(item.crashes, now)
	broken := len(item.crashes) > p.config.BreakerMaxCrash
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
	item.state = StateCrashed
	if len(item.crashes) > p.config.BreakerMaxCrash {
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
	p.online = online
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

// assetsGCSettle delays the one-shot orphan-assets sweep so in-process,
// managed, and already-running standalone plugins have time to reconnect
// and re-push before their entries are judged.
const assetsGCSettle = 30 * time.Second

// startAssetsGC keeps a snapshot of the plugins:_:assets mailbox and, once
// after the settle window, removes entries whose plugin is neither in
// registry.json nor connected. This is the backstop for delete paths that
// bypassed the manager RPC (which removes assets itself): without it the
// gateway republishes leftover bundles from disk on every boot and the
// shell would keep offering entry points for dead plugins.
func (p *Plugin) startAssetsGC() error {
	sub, err := p.client.Subscribe("plugins:_:assets", p.trackAssets)
	if err != nil {
		return err
	}
	// The Start ctx is cancelled by the assembly when Start returns, so the
	// sweep runs on its own context tied to shutdown instead.
	gcCtx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.gcCancel = cancel
	p.mu.Unlock()
	go func() {
		timer := time.NewTimer(assetsGCSettle)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-gcCtx.Done():
			return
		}
		p.gcOrphanAssets(gcCtx)
		_ = sub.Unsubscribe(context.Background())
	}()
	return nil
}

func (p *Plugin) trackAssets(frame busclient.Frame) {
	entries, _ := frame.Value.(map[string]any)
	ids := make(map[string]bool, len(entries))
	for id := range entries {
		ids[id] = true
	}
	p.mu.Lock()
	p.assetIDs = ids
	p.mu.Unlock()
}

// staleAssetIDs lists asset-entry ids that are neither registered nor online.
func staleAssetIDs(assets, registered, online map[string]bool) []string {
	stale := make([]string, 0, len(assets))
	for id := range assets {
		if registered[id] || online[id] {
			continue
		}
		stale = append(stale, id)
	}
	return stale
}

func (p *Plugin) gcOrphanAssets(ctx context.Context) {
	p.mu.Lock()
	stale := staleAssetIDs(p.assetIDs, p.registered, p.online)
	p.mu.Unlock()
	for _, id := range stale {
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := p.client.Request(reqCtx, "gateway:_:assets:remove", map[string]any{"id": id}, 5*time.Second)
		cancel()
		if err != nil {
			slog.Warn("orphan assets remove failed", "plugin", id, "error", err)
			continue
		}
		slog.Info("removed orphaned plugin assets", "plugin", id)
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
	if p.gcCancel != nil {
		p.gcCancel()
		p.gcCancel = nil
	}
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
