package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"viewer/internal/plugins/supervisor"
)

func main() {
	defaults := supervisor.DefaultConfig()
	kernelWS := flag.String("kernel-ws", "", "kernel WebSocket URL")
	registry := flag.String("registry", "", "plugin registry JSON path")
	logDir := flag.String("log-dir", defaults.LogDir, "per-plugin log directory")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	plugin, err := supervisor.New(supervisor.Config{KernelWS: *kernelWS, RegistryPath: *registry, LogDir: *logDir})
	if err != nil {
		slog.Error("invalid supervisor configuration", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := plugin.Run(ctx); err != nil {
		slog.Error("supervisor failed", "error", err)
		os.Exit(1)
	}
}
