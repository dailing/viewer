package main

import (
	"context"
	"flag"
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
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
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
