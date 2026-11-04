package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"viewer/sdk/go/busclient"
)

const (
	mcpConfigNamespace = "plugins.viewer.browser"
	defaultMCPPort     = 18732
	mcpRPCBudget       = 10 * time.Second
)

type MCPConfig struct {
	Enabled bool `json:"enabled"`
	Port    int  `json:"port"`
}

type MCPStatus struct {
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	URL       string `json:"url,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

func defaultMCPConfig() MCPConfig {
	return MCPConfig{Enabled: true, Port: defaultMCPPort}
}

func normalizeMCPConfig(config MCPConfig) (MCPConfig, error) {
	if config.Port == 0 {
		config.Port = defaultMCPPort
	}
	if config.Port < 1 || config.Port > 65535 {
		return config, fmt.Errorf("port must be between 1 and 65535")
	}
	return config, nil
}

func decodeMCPConfig(value any) (MCPConfig, error) {
	config := defaultMCPConfig()
	if value != nil {
		encoded, err := json.Marshal(value)
		if err != nil {
			return config, err
		}
		if err := json.Unmarshal(encoded, &config); err != nil {
			return config, err
		}
	}
	return normalizeMCPConfig(config)
}

// mcpServer owns only the loopback MCP listener. Reconfiguration never stops
// the Browser backend, Chrome, pages, streams, or bus connection.
type mcpServer struct {
	service *Service

	applyMu sync.Mutex
	mu      sync.Mutex
	config  MCPConfig
	server  *http.Server
	lastErr string
}

func newMCPServer(service *Service) *mcpServer {
	return &mcpServer{service: service, config: defaultMCPConfig()}
}

func (s *mcpServer) Status() MCPStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := MCPStatus{
		Enabled: s.config.Enabled, Running: s.server != nil,
		Host: "127.0.0.1", Port: s.config.Port, LastError: s.lastErr,
	}
	if status.Running {
		status.URL = "http://" + net.JoinHostPort(status.Host, strconv.Itoa(status.Port)) + "/mcp"
	}
	return status
}

func (s *mcpServer) setError(err error) {
	s.mu.Lock()
	s.lastErr = err.Error()
	s.mu.Unlock()
}

// Apply binds a replacement before closing the previous listener. A bad new
// port therefore leaves the working MCP endpoint untouched.
func (s *mcpServer) Apply(raw MCPConfig) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	config, err := normalizeMCPConfig(raw)
	if err != nil {
		s.setError(err)
		return err
	}
	s.mu.Lock()
	unchanged := config == s.config && ((config.Enabled && s.server != nil) || (!config.Enabled && s.server == nil))
	s.mu.Unlock()
	if unchanged {
		s.mu.Lock()
		s.lastErr = ""
		s.mu.Unlock()
		return nil
	}

	var replacement *http.Server
	var listener net.Listener
	if config.Enabled {
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(config.Port))
		listener, err = net.Listen("tcp", address)
		if err != nil {
			wrapped := fmt.Errorf("listen %s: %w", address, err)
			s.setError(wrapped)
			return wrapped
		}
		replacement = &http.Server{
			Handler:           &MCPHandler{service: s.service},
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
	}

	s.mu.Lock()
	previous := s.server
	s.server = replacement
	s.config = config
	s.lastErr = ""
	s.mu.Unlock()
	if replacement != nil {
		go func() {
			slog.Info("browser MCP listening", "address", listener.Addr().String(), "endpoint", "http://"+listener.Addr().String()+"/mcp")
			if serveErr := replacement.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
				slog.Error("browser MCP server failed", "error", serveErr)
				s.setError(serveErr)
			}
		}()
	}
	if previous != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = previous.Shutdown(ctx)
		cancel()
	}
	return nil
}

func (s *mcpServer) Close() {
	_ = s.Apply(MCPConfig{Enabled: false, Port: s.Status().Port})
}

func loadMCPConfig(ctx context.Context, client *busclient.Client) (MCPConfig, error) {
	value, err := client.Request(ctx, "config:_:get", map[string]any{
		"plugin": mcpConfigNamespace, "key": "mcp",
	}, mcpRPCBudget)
	if err != nil {
		return defaultMCPConfig(), err
	}
	return decodeMCPConfig(value)
}

func handleMCPConfigChange(server *mcpServer, frame busclient.Frame) {
	value, ok := busclient.Object(frame)
	if !ok {
		return
	}
	config, err := decodeMCPConfig(value["mcp"])
	if err != nil {
		slog.Error("invalid browser MCP config", "error", err)
		return
	}
	if err := server.Apply(config); err != nil {
		slog.Error("reload browser MCP server", "error", err)
	}
}

func configureMCP(client *busclient.Client, server *mcpServer, value map[string]any) (MCPStatus, error) {
	config, err := decodeMCPConfig(value)
	if err != nil {
		return server.Status(), err
	}
	previous := server.Status()
	if err := server.Apply(config); err != nil {
		return server.Status(), err
	}
	ctx, cancel := context.WithTimeout(context.Background(), mcpRPCBudget)
	defer cancel()
	if _, err := client.Request(ctx, "config:_:set", map[string]any{
		"plugin": mcpConfigNamespace, "key": "mcp", "value": config,
	}, mcpRPCBudget); err != nil {
		_ = server.Apply(MCPConfig{Enabled: previous.Enabled, Port: previous.Port})
		return server.Status(), err
	}
	return server.Status(), nil
}
