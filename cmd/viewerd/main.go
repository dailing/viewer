package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"viewer/internal/pluginapi"
	"viewer/internal/plugins/gateway"
	viewerweb "viewer/web"
)

func main() {
	dataDefault, err := defaultDataDir()
	if err != nil {
		slog.Error("cannot determine default data directory", "error", err)
		os.Exit(1)
	}
	host := flag.String("host", "127.0.0.1", "gateway listen host")
	port := flag.Int("port", 18730, "gateway listen port")
	kernelHost := flag.String("kernel-host", "127.0.0.1", "kernel listen host (keep loopback unless explicitly required)")
	kernelPort := flag.Int("kernel-port", 8765, "kernel listen port")
	dataDir := flag.String("data-dir", dataDefault, "store data directory")
	staticDir := flag.String("static", "", "frontend directory overriding embedded assets")
	pluginsFlag := flag.String("plugins", "all", `core plugins to run: "all", "none", or comma-separated plugin ids`)
	waitPID := flag.Int("wait-pid", 0, "wait for this pid to exit before starting (used by graceful self-restart)")
	flag.Parse()

	logFile, err := configureLogging(*dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot initialize viewer log: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()
	slog.Info("file logging initialized", "path", filepath.Join(*dataDir, "viewerd.log"))
	staticFS, err := viewerweb.Dist()
	if err != nil {
		slog.Error("embedded frontend unavailable", "error", err)
		os.Exit(1)
	}
	if *staticDir != "" {
		staticFS, err = gateway.DirectoryFS(*staticDir)
		if err != nil {
			slog.Error("invalid static directory", "error", err)
			os.Exit(2)
		}
	}
	assembly, err := pluginapi.New(pluginapi.Config{
		GatewayHost: *host, GatewayPort: *port,
		KernelHost: *kernelHost, KernelPort: *kernelPort,
		DataDir: *dataDir, StaticFS: staticFS,
		Plugins: parsePluginSelection(*pluginsFlag),
	})
	if err != nil {
		slog.Error("invalid assembly configuration", "error", err)
		os.Exit(2)
	}
	if *waitPID > 0 {
		waitCtx, cancelWait := context.WithTimeout(context.Background(), 30*time.Second)
		err := waitForPIDExit(waitCtx, *waitPID)
		cancelWait()
		if err != nil {
			slog.Error("previous process did not exit in time", "pid", *waitPID, "error", err)
			os.Exit(1)
		}
		slog.Info("previous process exited, taking over", "pid", *waitPID)
	}
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 30*time.Second)
	if err := assembly.Start(startupCtx); err != nil {
		cancelStartup()
		slog.Error("single-binary startup failed", "error", err)
		os.Exit(1)
	}
	cancelStartup()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	select {
	case received := <-signals:
		slog.Info("shutdown signal received", "signal", received.String())
	case err := <-waitForAssembly(assembly):
		if err != nil {
			slog.Error("kernel server failed", "error", err)
			os.Exit(1)
		}
		return
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := assembly.Shutdown(shutdownCtx); err != nil {
		slog.Error("single-binary shutdown failed", "error", err)
		os.Exit(1)
	}
}

func configureLogging(dataDir string) (*os.File, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	path := filepath.Join(dataDir, "viewerd.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	output := io.MultiWriter(os.Stderr, file)
	slog.SetDefault(slog.New(slog.NewJSONHandler(output, nil)))
	log.SetOutput(output)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.LUTC)
	return file, nil
}

func defaultDataDir() (string, error) {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "viewer"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "viewer"), nil
}

// parsePluginSelection maps the --plugins flag to a pluginapi selection:
// "all" runs the full registry, "none" runs the kernel alone, anything else
// is a comma-separated list of core plugin ids (validated by pluginapi.New).
func parsePluginSelection(flagValue string) []string {
	switch flagValue {
	case "all":
		return nil
	case "none":
		return []string{}
	default:
		parts := strings.Split(flagValue, ",")
		for index := range parts {
			parts[index] = strings.TrimSpace(parts[index])
		}
		return parts
	}
}

func waitForAssembly(assembly *pluginapi.Assembly) <-chan error {
	result := make(chan error, 1)
	go func() { result <- assembly.Wait() }()
	return result
}

// waitForPIDExit polls pid with signal 0 until it disappears, so a
// replacement process never races the previous one for the listen ports.
// syscall.Kill(pid, 0) returns ESRCH once the process has been reaped
// (orphans are reaped by init after the parent exits).
func waitForPIDExit(ctx context.Context, pid int) error {
	const pollInterval = 200 * time.Millisecond
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("signal check for pid %d: %w", pid, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("pid %d still alive after %v", pid, 30*time.Second)
		case <-time.After(pollInterval):
		}
	}
}
