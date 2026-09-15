package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"viewer/internal/plugins/fileservice"
)

func defaultDataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "viewer")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "viewer")
	}
	return filepath.Join(home, ".local", "share", "viewer")
}

func main() {
	kernelWS := flag.String("kernel-ws", "", "kernel WebSocket URL (required)")
	dataDir := flag.String("data-dir", defaultDataDir(), "store data directory")
	flag.Parse()
	if *kernelWS == "" {
		flag.Usage()
		os.Exit(2)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	plugin := fileservice.New(*dataDir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := plugin.Start(ctx, *kernelWS, os.Getenv("VIEWER_MANAGED") == "1"); err != nil {
		slog.Error("file-service startup failed", "error", err)
		os.Exit(1)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
	if err := plugin.Close(); err != nil {
		slog.Error("file-service shutdown failed", "error", err)
		os.Exit(1)
	}
}
