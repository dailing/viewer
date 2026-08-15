// Package pluginapi assembles the Viewer kernel and its in-process core plugins.
// Plugins still communicate exclusively over the kernel WebSocket.
package pluginapi

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"viewer/internal/kernel"
)

const startupTimeout = 15 * time.Second

// Config is the complete single-binary runtime configuration.
type Config struct {
	GatewayHost string
	GatewayPort int
	KernelHost  string
	KernelPort  int
	DataDir     string
	StaticFS    fs.FS
	// Plugins selects the core plugins to run by id. nil means the full
	// Registry; an empty slice runs the kernel alone (test/debug mode).
	Plugins []string
}

// Plugin is the lifecycle contract implemented by each registry adapter.
// Start must return after the plugin is ready; Wait owns its steady-state loop.
type Plugin interface {
	Start(context.Context) error
	Wait(context.Context) error
	Close(context.Context) error
}

// Entry is one compile-time core-plugin registration.
type Entry struct {
	ID      string
	Factory func(RuntimeConfig) (Plugin, error)
}

// RuntimeConfig is supplied to registry factories after the kernel is listening.
type RuntimeConfig struct {
	KernelWS    string
	GatewayHost string
	GatewayPort int
	DataDir     string
	StaticFS    fs.FS
}

type runningPlugin struct {
	id     string
	plugin Plugin
}

// Assembly owns the kernel and every compiled-in plugin.
type Assembly struct {
	config Config
	kernel *kernel.Server
	set    []Entry

	ctx     context.Context
	cancel  context.CancelFunc
	plugins []runningPlugin
	wg      sync.WaitGroup
	stop    sync.Once
}

// New validates and constructs an unstarted assembly.
func New(config Config) (*Assembly, error) {
	if config.GatewayHost == "" {
		config.GatewayHost = "127.0.0.1"
	}
	if config.KernelHost == "" {
		config.KernelHost = "127.0.0.1"
	}
	if config.DataDir == "" {
		return nil, errors.New("data directory is required")
	}
	set, err := selectEntries(config.Plugins)
	if err != nil {
		return nil, err
	}
	return &Assembly{config: config, set: set}, nil
}

// selectEntries resolves the configured plugin id list against the Registry.
// nil selects everything; ids must name registered core plugins.
func selectEntries(ids []string) ([]Entry, error) {
	if ids == nil {
		return Registry, nil
	}
	byID := make(map[string]Entry, len(Registry))
	for _, entry := range Registry {
		byID[entry.ID] = entry
	}
	selected := make([]Entry, 0, len(ids))
	for _, id := range ids {
		entry, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("unknown core plugin %q", id)
		}
		selected = append(selected, entry)
	}
	return selected, nil
}

// Start listens on the kernel first, then starts core plugins in registry order.
func (a *Assembly) Start(ctx context.Context) error {
	if err := os.MkdirAll(a.config.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	kernelConfig := kernel.DefaultConfig()
	kernelConfig.Host, kernelConfig.Port = a.config.KernelHost, a.config.KernelPort
	a.kernel = kernel.New(kernelConfig)
	if err := a.kernel.Start(); err != nil {
		return fmt.Errorf("start kernel: %w", err)
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	runtimeConfig := RuntimeConfig{
		KernelWS:    kernelWebSocket(a.config.KernelHost, a.kernel.Port()),
		GatewayHost: a.config.GatewayHost, GatewayPort: a.config.GatewayPort,
		DataDir: a.config.DataDir, StaticFS: a.config.StaticFS,
	}
	for _, entry := range a.set {
		plugin, err := entry.Factory(runtimeConfig)
		if err != nil {
			a.abortStart()
			return fmt.Errorf("create plugin %s: %w", entry.ID, err)
		}
		if err := a.startPlugin(ctx, entry.ID, plugin); err != nil {
			a.abortStart()
			return err
		}
	}
	slog.Info("single-binary assembly ready", "gateway", net.JoinHostPort(a.config.GatewayHost, strconv.Itoa(a.config.GatewayPort)), "kernel", runtimeConfig.KernelWS, "plugins", len(a.plugins))
	return nil
}

func (a *Assembly) startPlugin(ctx context.Context, id string, plugin Plugin) error {
	started := make(chan error, 1)
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		ready := false
		defer func() {
			if recovered := recover(); recovered != nil {
				err := fmt.Errorf("panic: %v", recovered)
				slog.Error("core plugin panic", "plugin", id, "error", err, "stack", string(debug.Stack()))
				if !ready {
					started <- err
				} else {
					closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					_ = closePlugin(closeCtx, id, plugin)
					cancel()
				}
			}
		}()
		startCtx, cancel := context.WithTimeout(a.ctx, startupTimeout)
		err := plugin.Start(startCtx)
		cancel()
		ready = true
		started <- err
		if err != nil {
			return
		}
		if err := plugin.Wait(a.ctx); err != nil && a.ctx.Err() == nil {
			slog.Error("core plugin stopped", "plugin", id, "error", err)
		} else if err == nil && a.ctx.Err() == nil {
			slog.Error("core plugin stopped unexpectedly", "plugin", id)
		}
	}()
	select {
	case err := <-started:
		if err != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = closePlugin(closeCtx, id, plugin)
			cancel()
			return fmt.Errorf("start plugin %s: %w", id, err)
		}
		a.plugins = append(a.plugins, runningPlugin{id: id, plugin: plugin})
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(startupTimeout + time.Second):
		return fmt.Errorf("start plugin %s: timeout", id)
	}
}

// Wait returns when the kernel HTTP server stops unexpectedly.
func (a *Assembly) Wait() error {
	if a.kernel == nil {
		return errors.New("assembly is not started")
	}
	return a.kernel.Wait()
}

// Shutdown broadcasts kernel close 4009, then closes plugins in reverse order.
func (a *Assembly) Shutdown(ctx context.Context) error {
	var result error
	a.stop.Do(func() {
		if a.kernel != nil {
			result = errors.Join(result, a.kernel.Shutdown(ctx))
		}
		if a.cancel != nil {
			a.cancel()
		}
		for index := len(a.plugins) - 1; index >= 0; index-- {
			item := a.plugins[index]
			result = errors.Join(result, closePlugin(ctx, item.id, item.plugin))
		}
		done := make(chan struct{})
		go func() { a.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			result = errors.Join(result, ctx.Err())
		}
	})
	return result
}

func (a *Assembly) abortStart() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.Shutdown(ctx)
}

func closePlugin(ctx context.Context, id string, plugin Plugin) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("core plugin close panic", "plugin", id, "panic", recovered, "stack", string(debug.Stack()))
			err = fmt.Errorf("close plugin %s panic: %v", id, recovered)
		}
	}()
	if closeErr := plugin.Close(ctx); closeErr != nil {
		return fmt.Errorf("close plugin %s: %w", id, closeErr)
	}
	return nil
}

func kernelWebSocket(host string, port int) string {
	dialHost := host
	if host == "0.0.0.0" || host == "" {
		dialHost = "127.0.0.1"
	} else if host == "::" {
		dialHost = "::1"
	}
	return "ws://" + net.JoinHostPort(dialHost, strconv.Itoa(port)) + "/ws"
}

func dataPath(config RuntimeConfig, name string) string {
	return filepath.Join(config.DataDir, name)
}
