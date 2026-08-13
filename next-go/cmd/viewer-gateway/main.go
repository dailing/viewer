package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"viewer/internal/plugins/gateway"
)

func main() {
	defaults := gateway.DefaultConfig()
	kernelWS := flag.String("kernel-ws", "", "kernel WebSocket URL (required)")
	host := flag.String("host", defaults.Host, "gateway listen host")
	port := flag.Int("port", defaults.Port, "gateway listen port")
	staticDir := flag.String("static", "", "frontend static directory")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	config := gateway.DefaultConfig()
	config.KernelWS, config.Host, config.Port = *kernelWS, *host, *port
	if *staticDir != "" {
		staticFS, err := gateway.DirectoryFS(*staticDir)
		if err != nil {
			slog.Error("invalid static directory", "error", err)
			os.Exit(2)
		}
		config.StaticFS = staticFS
	}
	server := gateway.New(config)
	if err := server.Start(); err != nil {
		slog.Error("gateway startup failed", "error", err)
		os.Exit(1)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	select {
	case received := <-signals:
		slog.Info("shutdown signal received", "signal", received.String())
	case err := <-waitForServer(server):
		if err != nil {
			slog.Error("gateway server failed", "error", err)
			os.Exit(1)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		slog.Error("gateway shutdown failed", "error", err)
		os.Exit(1)
	}
}

func waitForServer(server *gateway.Server) <-chan error {
	result := make(chan error, 1)
	go func() { result <- server.Wait() }()
	return result
}
