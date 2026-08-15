// Package configstore implements plugin-level configuration persistence.
package configstore

import (
	"context"
	"log/slog"
	"sync"

	"viewer/internal/plugins/pluginrpc"
	"viewer/internal/plugins/storefile"
	"viewer/sdk/go/busclient"
)

const defaultFilename = "config-store.json"

var Manifest = busclient.Manifest{
	ID: "config-store", Version: "0.1.0",
	Slots: map[string]any{"get": map[string]any{}, "set": map[string]any{}, "list": map[string]any{}},
	Emits: map[string]any{"config": map[string]any{}},
}

type Plugin struct {
	dbPath string
	mu     sync.Mutex
	data   map[string]map[string]any
	client *busclient.Client
}

func New(dbPath string) (*Plugin, error) {
	if dbPath == "" {
		var err error
		dbPath, err = storefile.DefaultPath(defaultFilename)
		if err != nil {
			return nil, err
		}
	}
	return &Plugin{dbPath: dbPath, data: make(map[string]map[string]any)}, nil
}

func MailboxChannel(pluginID string) string { return "config:" + pluginID + ":config" }

func (p *Plugin) Start(ctx context.Context, kernelWS string, managed bool) error {
	if err := storefile.Load(p.dbPath, &p.data); err != nil {
		return err
	}
	if p.data == nil {
		p.data = make(map[string]map[string]any)
	}
	client := busclient.New(kernelWS, Manifest, busclient.WithManaged(managed))
	for pattern, handler := range map[string]func(busclient.Frame){
		"config:_:get":  p.get,
		"config:_:set":  p.set,
		"config:_:list": p.list,
	} {
		if _, err := client.Subscribe(pattern, handler); err != nil {
			_ = client.Close()
			return err
		}
	}
	p.client = client
	if err := client.Connect(ctx); err != nil {
		p.client = nil
		_ = client.Close()
		return err
	}
	slog.Info("config-store started", "db", p.dbPath)
	return nil
}

func (p *Plugin) Close() error {
	if p.client == nil {
		return nil
	}
	return p.client.Close()
}

func (p *Plugin) get(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	value, ok := pluginrpc.Object(frame)
	pluginID, valid := stringField(value, "plugin")
	if !ok || !valid {
		p.replyError(frame, "invalid_request", "missing required field: plugin")
		return
	}
	p.mu.Lock()
	config := cloneObject(p.data[pluginID])
	key, hasKey := optionalString(value, "key")
	var result any = config
	if hasKey && key != "" {
		result = config[key]
	}
	p.mu.Unlock()
	p.reply(frame, result)
}

func (p *Plugin) set(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	value, ok := pluginrpc.Object(frame)
	pluginID, validPlugin := stringField(value, "plugin")
	key, validKey := stringField(value, "key")
	if !ok || !validPlugin || !validKey {
		p.replyError(frame, "invalid_request", "missing required fields: plugin, key")
		return
	}
	newValue := value["value"]
	p.mu.Lock()
	config := p.data[pluginID]
	if config == nil {
		config = make(map[string]any)
		p.data[pluginID] = config
	}
	if newValue == nil {
		delete(config, key)
	} else {
		config[key] = newValue
	}
	if err := storefile.Save(p.dbPath, p.data); err != nil {
		p.mu.Unlock()
		p.replyError(frame, "write_error", err.Error())
		return
	}
	full := cloneObject(config)
	if err := p.client.Set(context.Background(), MailboxChannel(pluginID), full); err != nil {
		p.mu.Unlock()
		slog.Error("publish config mailbox", "error", err)
		return
	}
	p.mu.Unlock()
	p.reply(frame, map[string]any{"plugin": pluginID, "key": key, "value": newValue})
}

func (p *Plugin) list(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	p.mu.Lock()
	result := make(map[string]any, len(p.data))
	for pluginID, config := range p.data {
		result[pluginID] = cloneObject(config)
	}
	p.mu.Unlock()
	p.reply(frame, result)
}

func (p *Plugin) reply(frame busclient.Frame, result any) {
	if err := pluginrpc.Respond(p.client, frame, result); err != nil {
		slog.Error("config-store RPC response failed", "error", err)
	}
}

func (p *Plugin) replyError(frame busclient.Frame, code, message string) {
	if err := pluginrpc.RespondError(p.client, frame, code, message); err != nil {
		slog.Error("config-store RPC error response failed", "error", err)
	}
}

func stringField(value map[string]any, key string) (string, bool) {
	if value == nil {
		return "", false
	}
	result, ok := value[key].(string)
	return result, ok
}

func optionalString(value map[string]any, key string) (string, bool) {
	raw, exists := value[key]
	if !exists || raw == nil {
		return "", false
	}
	result, ok := raw.(string)
	return result, ok
}

func cloneObject(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
