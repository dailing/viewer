package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"viewer/internal/kernel"
)

func main() {
	defaults := kernel.DefaultConfig()
	host := flag.String("host", defaults.Host, "kernel listen host")
	port := flag.Int("port", defaults.Port, "kernel listen port")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)
	config := kernel.DefaultConfig()
	config.Host = *host
	config.Port = *port
	server := kernel.New(config)
	if err := server.Start(); err != nil {
		slog.Error("kernel startup failed", "error", err)
		os.Exit(1)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	select {
	case signal := <-signals:
		slog.Info("shutdown signal received", "signal", signal.String())
	case err := <-waitForServer(server):
		if err != nil {
			slog.Error("kernel server failed", "error", err)
			os.Exit(1)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		slog.Error("kernel shutdown failed", "error", err)
		os.Exit(1)
	}
}

func waitForServer(server *kernel.Server) <-chan error {
	result := make(chan error, 1)
	go func() { result <- server.Wait() }()
	return result
}
