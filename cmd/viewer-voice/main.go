package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"viewer/internal/plugins/voice"
)

func main() {
	kernelWS := flag.String("kernel-ws", "", "kernel WebSocket URL")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	plugin, err := voice.New(voice.Config{KernelWS: *kernelWS})
	if err != nil {
		slog.Error("invalid voice configuration", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := plugin.Run(ctx); err != nil {
		slog.Error("voice plugin failed", "error", err)
		os.Exit(1)
	}
}
