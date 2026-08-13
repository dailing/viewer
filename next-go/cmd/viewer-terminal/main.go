package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"viewer/internal/busclient"
	"viewer/internal/plugins/terminal"
)

func main() {
	kernelWS := flag.String("kernel-ws", "", "kernel WebSocket URL (required)")
	flag.Parse()
	if *kernelWS == "" {
		slog.Error("--kernel-ws is required")
		os.Exit(2)
	}

	plugin := terminal.New(*kernelWS, busclient.WithManaged(os.Getenv("VIEWER_MANAGED") == "1"))
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 15*time.Second)
	if err := plugin.Start(connectCtx); err != nil {
		cancelConnect()
		slog.Error("terminal plugin startup failed", "error", err)
		os.Exit(1)
	}
	cancelConnect()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	signal := <-signals
	slog.Info("terminal plugin stopping", "signal", signal.String())
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := plugin.Close(shutdownCtx); err != nil {
		slog.Error("terminal plugin shutdown failed", "error", err)
		os.Exit(1)
	}
}
