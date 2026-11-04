package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"viewer/sdk/go/busclient"
)

var manifest = busclient.Manifest{
	ID: "viewer.browser", Version: "0.2.0", Name: "Browser", Icon: "bi-window-desktop",
	Description: "Persistent managed Chrome pages with isolated page ids, live H.264 video and exclusive control leases.",
	Slots: map[string]any{
		"state": map[string]any{}, "create": map[string]any{}, "close": map[string]any{},
		"release": map[string]any{}, "watch": map[string]any{}, "unwatch": map[string]any{},
		"control": map[string]any{}, "input": map[string]any{}, "navigate": map[string]any{},
		"mcp_status": map[string]any{}, "mcp_configure": map[string]any{},
	},
	Emits: map[string]any{"state": map[string]any{}},
}

func main() {
	kernelWS := flag.String("kernel-ws", "ws://127.0.0.1:8765/ws", "Viewer kernel WebSocket")
	mcpListen := flag.String("mcp-listen", "", "optional loopback MCP listen override (host:port); default uses config-store")
	stateFile := flag.String("state-file", defaultDataPath("browser-pages.json"), "managed-page state file")
	profileDir := flag.String("profile-dir", defaultDataPath("browser-profile"), "persistent Chrome user-data-dir")
	gatewayURL := flag.String("gateway-url", "http://127.0.0.1:18100", "Viewer gateway base URL for the generic media plane")
	chromePath := flag.String("chrome-path", "", "Chrome executable (auto-detected when empty)")
	xvfbPath := flag.String("xvfb-path", "Xvfb", "Xvfb executable")
	ffmpegPath := flag.String("ffmpeg-path", "ffmpeg", "FFmpeg executable")
	frontendDir := flag.String("frontend-dir", "frontend/dist", "built frontend asset directory")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	chrome, err := NewChromeManager(ChromeConfig{ChromePath: *chromePath, XvfbPath: *xvfbPath, ProfileDir: *profileDir})
	if err != nil {
		slog.Error("managed Chrome initialization failed", "error", err)
		os.Exit(1)
	}
	defer chrome.Close()
	client := busclient.New(*kernelWS, manifest, busclient.WithManaged(os.Getenv("VIEWER_MANAGED") == "1"), busclient.WithRequestTimeout(40*time.Second))
	service, err := NewService(client, *stateFile, chrome, func(pageID string) StreamController {
		return NewPageStream(pageID, *gatewayURL, *ffmpegPath)
	})
	if err != nil {
		slog.Error("browser service initialization failed", "error", err)
		os.Exit(1)
	}
	defer service.Close()
	mcp := newMCPServer(service)
	defer mcp.Close()

	for pattern := range map[string]any{
		"viewer.browser:_:state": nil, "viewer.browser:_:create": nil, "viewer.browser:_:close": nil,
		"viewer.browser:_:release": nil, "viewer.browser:_:watch": nil, "viewer.browser:_:unwatch": nil,
		"viewer.browser:_:control": nil, "viewer.browser:_:input": nil, "viewer.browser:_:navigate": nil,
		"viewer.browser:_:mcp_status": nil, "viewer.browser:_:mcp_configure": nil,
	} {
		if _, err := client.Subscribe(pattern, func(frame busclient.Frame) { handleRPC(client, service, mcp, frame) }); err != nil {
			slog.Error("browser subscription failed", "pattern", pattern, "error", err)
			os.Exit(1)
		}
	}
	if _, err := client.Subscribe("config:plugins.viewer.browser:config", func(frame busclient.Frame) { handleMCPConfigChange(mcp, frame) }); err != nil {
		slog.Error("browser config subscription failed", "error", err)
		os.Exit(1)
	}
	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = client.Connect(connectCtx)
	cancel()
	if err != nil {
		slog.Error("browser bus connection failed", "error", err)
		os.Exit(1)
	}
	defer client.Close()

	configCtx, configCancel := context.WithTimeout(ctx, mcpRPCBudget)
	mcpConfig, configErr := loadMCPConfig(configCtx, client)
	configCancel()
	if configErr != nil {
		slog.Error("load browser MCP config; using defaults", "error", configErr)
		mcpConfig = defaultMCPConfig()
	}
	if *mcpListen != "" {
		host, portText, parseErr := net.SplitHostPort(*mcpListen)
		port, portErr := strconv.Atoi(portText)
		if parseErr != nil || portErr != nil || host != "127.0.0.1" {
			slog.Error("invalid --mcp-listen; expected 127.0.0.1:port", "value", *mcpListen)
			os.Exit(1)
		}
		mcpConfig = MCPConfig{Enabled: true, Port: port}
	}
	if err := mcp.Apply(mcpConfig); err != nil {
		slog.Error("start browser MCP server", "error", err)
	}

	assetCtx, assetCancel := context.WithTimeout(ctx, 45*time.Second)
	if _, err := busclient.PushAssets(assetCtx, client, *frontendDir, map[string]any{"name": "Browser", "icon": "bi-window-desktop", "description": manifest.Description}); err != nil {
		slog.Error("browser frontend asset push failed", "error", err)
	}
	assetCancel()
	service.publishState()
	go service.Restore(ctx)

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				service.Maintain()
			}
		}
	}()
	slog.Info("viewer.browser managed Chrome backend started", "state_file", *stateFile, "profile_dir", *profileDir)
	<-ctx.Done()
}

func handleRPC(client *busclient.Client, service *Service, mcp *mcpServer, frame busclient.Frame) {
	value, ok := busclient.Object(frame)
	if !ok && !busclient.Cancelled(frame) {
		value = make(map[string]any)
	}
	if busclient.Cancelled(frame) {
		return
	}
	channel := frame.Channel
	var result any = map[string]any{"ok": true}
	var err error
	switch channel {
	case "viewer.browser:_:state":
		result = service.State()
	case "viewer.browser:_:create":
		result, err = service.CreatePage(context.Background(), stringField(value, "url"))
	case "viewer.browser:_:close":
		err = service.ClosePage(context.Background(), stringField(value, "page_id"))
	case "viewer.browser:_:release":
		err = service.ForceRelease(stringField(value, "page_id"))
	case "viewer.browser:_:watch":
		result, err = service.Watch(context.Background(), stringField(value, "page_id"), stringField(value, "viewer_id"), value)
	case "viewer.browser:_:unwatch":
		service.Unwatch(stringField(value, "page_id"), stringField(value, "viewer_id"))
	case "viewer.browser:_:control":
		takeover, _ := value["takeover"].(bool)
		result, err = service.HumanControl(stringField(value, "page_id"), stringField(value, "viewer_id"), takeover)
	case "viewer.browser:_:input":
		event, _ := value["event"].(map[string]any)
		err = service.Input(context.Background(), stringField(value, "page_id"), stringField(value, "viewer_id"), event)
	case "viewer.browser:_:navigate":
		result, err = service.HumanNavigate(context.Background(), stringField(value, "page_id"), stringField(value, "viewer_id"), stringField(value, "url"))
	case "viewer.browser:_:mcp_status":
		result = mcp.Status()
	case "viewer.browser:_:mcp_configure":
		result, err = configureMCP(client, mcp, value)
	default:
		err = errorf("invalid_request", "unknown browser RPC %q", channel)
	}
	respond(client, frame, result, err)
}

func respond(client *busclient.Client, frame busclient.Frame, result any, err error) {
	if err == nil {
		if replyErr := busclient.Respond(client, frame, result); replyErr != nil {
			slog.Warn("browser RPC response failed", "error", replyErr)
		}
		return
	}
	code, message := "internal_error", err.Error()
	var browserErr *Error
	if errors.As(err, &browserErr) {
		code, message = browserErr.Code, browserErr.Message
	}
	if replyErr := busclient.RespondError(client, frame, code, message); replyErr != nil {
		slog.Warn("browser RPC error response failed", "error", replyErr)
	}
}

func stringField(value map[string]any, key string) string {
	text, _ := value[key].(string)
	return text
}

func intField(value map[string]any, key string) int {
	number, _ := numberValue(value[key])
	return int(number)
}

func defaultDataPath(parts ...string) string {
	var root string
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		root = filepath.Join(dir, "viewer")
	} else if home, err := os.UserHomeDir(); err == nil {
		root = filepath.Join(home, ".local", "share", "viewer")
	} else {
		root = "."
	}
	return filepath.Join(append([]string{root}, parts...)...)
}
