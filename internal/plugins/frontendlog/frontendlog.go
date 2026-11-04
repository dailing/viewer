// Package frontendlog writes browser diagnostics to the backend log stream.
package frontendlog

import (
	"context"
	"log/slog"

	"viewer/sdk/go/busclient"
)

const LogChannel = "frontend:_:log"

var Manifest = busclient.Manifest{
	ID: "frontend-log", Version: "0.1.0",
	Slots: map[string]any{},
	Emits: map[string]any{},
}

type Plugin struct {
	client *busclient.Client
	logger *slog.Logger
}

func New() *Plugin {
	return &Plugin{logger: slog.Default()}
}

func (p *Plugin) Start(ctx context.Context, kernelWS string, managed bool) error {
	client := busclient.New(kernelWS, Manifest, busclient.WithManaged(managed))
	if _, err := client.Subscribe(LogChannel, p.log); err != nil {
		_ = client.Close()
		return err
	}
	p.client = client
	if err := client.Connect(ctx); err != nil {
		p.client = nil
		_ = client.Close()
		return err
	}
	p.logger.Info("frontend log sink started")
	return nil
}

func (p *Plugin) Close() error {
	if p.client == nil {
		return nil
	}
	return p.client.Close()
}

func (p *Plugin) log(frame busclient.Frame) {
	value, ok := busclient.Object(frame)
	if !ok {
		p.logger.Warn("invalid frontend log event", "reason", "payload must be an object")
		return
	}
	message, messageOK := value["message"].(string)
	level, levelOK := value["level"].(string)
	if !messageOK || message == "" || !levelOK || !validLevel(level) {
		p.logger.Warn("invalid frontend log event", "reason", "message and a valid level are required", "payload", value)
		return
	}

	attrs := []any{"source", "frontend"}
	if component, ok := value["component"].(string); ok && component != "" {
		attrs = append(attrs, "component", component)
	}
	if detail, exists := value["context"]; exists {
		attrs = append(attrs, "context", detail)
	}
	if clientTS, exists := value["ts"]; exists {
		attrs = append(attrs, "client_ts", clientTS)
	}
	if frame.Origin != nil {
		attrs = append(attrs, "origin_plugin", frame.Origin.Plugin, "browser_conn", frame.Origin.Instance)
	}
	p.logger.Log(context.Background(), parseLevel(level), message, attrs...)
}

func validLevel(level string) bool {
	switch level {
	case "debug", "info", "warn", "error":
		return true
	default:
		return false
	}
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
