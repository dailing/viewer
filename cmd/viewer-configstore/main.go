package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"viewer/internal/plugins/configstore"
)

func main() {
	kernelWS := flag.String("kernel-ws", "", "kernel WebSocket URL (required)")
	dbPath := flag.String("db", "", "config JSON file path")
	flag.Parse()
	if *kernelWS == "" {
		flag.Usage()
		os.Exit(2)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	plugin, err := configstore.New(*dbPath)
	if err != nil {
		slog.Error("config-store setup failed", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := plugin.Start(ctx, *kernelWS, os.Getenv("VIEWER_MANAGED") == "1"); err != nil {
		slog.Error("config-store startup failed", "error", err)
		os.Exit(1)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
	if err := plugin.Close(); err != nil {
		slog.Error("config-store shutdown failed", "error", err)
		os.Exit(1)
	}
}
