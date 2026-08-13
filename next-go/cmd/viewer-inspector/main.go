package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"viewer/internal/plugins/inspector"
)

func main() {
	defaults := inspector.DefaultConfig()
	kernelWS := flag.String("kernel-ws", "", "kernel WebSocket URL")
	ringSize := flag.Int("ring-size", defaults.RingSize, "captured frame ring size")
	echo := flag.Bool("echo", false, "print captured frames to stdout")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	plugin, err := inspector.New(inspector.Config{KernelWS: *kernelWS, RingSize: *ringSize, Echo: *echo})
	if err != nil {
		slog.Error("invalid inspector configuration", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := plugin.Run(ctx); err != nil {
		slog.Error("inspector failed", "error", err)
		os.Exit(1)
	}
}
