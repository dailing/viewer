package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func availableMCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func checkMCPHealth(t *testing.T, port int) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) == "" {
		t.Fatalf("health response = %s %q", response.Status, body)
	}
}

func TestMCPConfigDefaultsAndValidation(t *testing.T) {
	config, err := decodeMCPConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Enabled || config.Port != defaultMCPPort {
		t.Fatalf("default config = %+v", config)
	}
	config, err = decodeMCPConfig(map[string]any{"enabled": false, "port": 19001})
	if err != nil {
		t.Fatal(err)
	}
	if config.Enabled || config.Port != 19001 {
		t.Fatalf("decoded config = %+v", config)
	}
	if _, err := normalizeMCPConfig(MCPConfig{Enabled: true, Port: 70000}); err == nil {
		t.Fatal("expected invalid port error")
	}
}

func TestMCPServerDisableAndReconfigureWithoutBrowserRestart(t *testing.T) {
	server := newMCPServer(nil)
	firstPort := availableMCPPort(t)
	secondPort := availableMCPPort(t)
	if err := server.Apply(MCPConfig{Enabled: true, Port: firstPort}); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	checkMCPHealth(t, firstPort)
	if status := server.Status(); !status.Running || status.URL != fmt.Sprintf("http://127.0.0.1:%d/mcp", firstPort) {
		t.Fatalf("first status = %+v", status)
	}

	if err := server.Apply(MCPConfig{Enabled: true, Port: secondPort}); err != nil {
		t.Fatal(err)
	}
	checkMCPHealth(t, secondPort)
	if err := server.Apply(MCPConfig{Enabled: true, Port: 70000}); err == nil {
		t.Fatal("expected invalid replacement port error")
	}
	if status := server.Status(); !status.Running || status.Port != secondPort {
		t.Fatalf("failed replacement changed running server: %+v", status)
	}

	if err := server.Apply(MCPConfig{Enabled: false, Port: secondPort}); err != nil {
		t.Fatal(err)
	}
	status := server.Status()
	if status.Enabled || status.Running || status.URL != "" {
		t.Fatalf("disabled status = %+v", status)
	}
	client := &http.Client{Timeout: 200 * time.Millisecond}
	if response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", secondPort)); err == nil {
		response.Body.Close()
		t.Fatal("disabled MCP listener still accepts requests")
	}
}
