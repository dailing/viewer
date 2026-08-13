package chat

import (
	"context"
	"errors"
	"os"
	"strings"

	"viewer/internal/codexserver"
)

type codexAgent struct {
	client *codexserver.Client
	model  string
	update func(driverEvent)
}

func (p *Plugin) newCodexAgent(ctx context.Context, model string) (agent, string, error) {
	if !envBool("VIEWER_CODEX_APP_SERVER_ENABLED", true) {
		return nil, "", errors.New("codex app-server is disabled")
	}
	command := strings.TrimSpace(os.Getenv("VIEWER_CODEX_APP_SERVER_COMMAND"))
	if command == "" {
		command = "codex"
	}
	client, err := codexserver.New(ctx, codexserver.ProcessConfig{Command: command, Arguments: []string{"app-server", "--stdio"}, YOLO: envBool("VIEWER_CODEX_APP_SERVER_YOLO", true)})
	if err != nil {
		return nil, "", err
	}
	result := &codexAgent{client: client, model: model}
	client.OnUpdate(func(update codexserver.Update) {
		if result.update == nil {
			return
		}
		text := ""
		if update.Method == "item/agentMessage/delta" {
			text, _ = update.Params["delta"].(string)
		}
		result.update(driverEvent{Provider: "codex-app-server", SessionID: update.ThreadID, Kind: update.Method, Raw: update.Raw, Data: update.Params, Text: text})
	})
	return result, model, nil
}

func (c *codexAgent) Initialize(context.Context) (map[string]any, error) {
	return map[string]any{"transport": "codex-app-server"}, nil
}
func (c *codexAgent) NewSession(ctx context.Context, cwd string) (string, error) {
	return c.client.ThreadStart(ctx, cwd, c.model)
}
func (c *codexAgent) LoadSession(ctx context.Context, id, cwd string) error {
	return c.client.ThreadResume(ctx, id, cwd)
}
func (c *codexAgent) Prompt(ctx context.Context, id, text string) (string, error) {
	turn, err := c.client.TurnStart(ctx, id, text, c.model)
	if err != nil {
		return "", err
	}
	status, _ := turn["status"].(string)
	if status == "failed" {
		return status, errors.New("codex turn failed")
	}
	if status == "interrupted" {
		return "cancelled", nil
	}
	return "end_turn", nil
}
func (c *codexAgent) Cancel(ctx context.Context, id string) error {
	return c.client.TurnInterrupt(ctx, id)
}
func (c *codexAgent) OnUpdate(callback func(driverEvent)) { c.update = callback }
func (c *codexAgent) Stderr() string                      { return "" }
func (c *codexAgent) Close() error                        { return c.client.Close() }
