// Package instancestore implements per-instance state persistence.
package instancestore

import (
	"context"
	"log/slog"
	"sync"

	"viewer/internal/plugins/pluginrpc"
	"viewer/internal/plugins/storefile"
	"viewer/sdk/go/busclient"
)

const defaultFilename = "instance-store.json"

var Manifest = busclient.Manifest{
	ID: "instance-store", Version: "0.1.0",
	Slots: map[string]any{
		"get": map[string]any{}, "set": map[string]any{},
		"delete": map[string]any{}, "list": map[string]any{},
	},
	Emits: map[string]any{"state": map[string]any{}},
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

func MailboxChannel(pluginID, instance string) string {
	return "instance-store:" + pluginID + ":" + instance
}

func (p *Plugin) Start(ctx context.Context, kernelWS string, managed bool) error {
	if err := storefile.Load(p.dbPath, &p.data); err != nil {
		return err
	}
	if p.data == nil {
		p.data = make(map[string]map[string]any)
	}
	client := busclient.New(kernelWS, Manifest, busclient.WithManaged(managed))
	for pattern, handler := range map[string]func(busclient.Frame){
		"instance:_:get":    p.get,
		"instance:_:set":    p.set,
		"instance:_:delete": p.delete,
		"instance:_:list":   p.list,
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
	slog.Info("instance-store started", "db", p.dbPath)
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
	pluginID, instance, valid := reference(value)
	if !ok || !valid {
		p.replyError(frame, "invalid_request", "missing required fields: plugin, instance")
		return
	}
	p.mu.Lock()
	state := p.data[pluginID][instance]
	key, hasKey := optionalString(value, "key")
	var result any = state
	if object, isObject := state.(map[string]any); hasKey && key != "" && isObject {
		result = object[key]
	}
	p.mu.Unlock()
	p.reply(frame, result)
}

func (p *Plugin) set(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	value, ok := pluginrpc.Object(frame)
	pluginID, instance, valid := reference(value)
	if !ok || !valid {
		p.replyError(frame, "invalid_request", "missing required fields: plugin, instance")
		return
	}
	key, hasKey := optionalString(value, "key")
	newValue := value["value"]
	p.mu.Lock()
	instances := p.data[pluginID]
	if instances == nil {
		instances = make(map[string]any)
		p.data[pluginID] = instances
	}
	if !hasKey {
		object, isObject := newValue.(map[string]any)
		if !isObject {
			p.mu.Unlock()
			p.replyError(frame, "invalid_request", "whole-state replace requires a JSON object value")
			return
		}
		instances[instance] = object
	} else {
		state, isObject := instances[instance].(map[string]any)
		if !isObject {
			state = make(map[string]any)
			instances[instance] = state
		}
		if newValue == nil {
			delete(state, key)
		} else {
			state[key] = newValue
		}
	}
	if err := storefile.Save(p.dbPath, p.data); err != nil {
		p.mu.Unlock()
		p.replyError(frame, "write_error", err.Error())
		return
	}
	state := cloneState(instances[instance])
	if err := p.client.Set(context.Background(), MailboxChannel(pluginID, instance), state); err != nil {
		p.mu.Unlock()
		slog.Error("publish instance mailbox", "error", err)
		return
	}
	p.mu.Unlock()
	p.reply(frame, map[string]any{"plugin": pluginID, "instance": instance, "state": state})
}

func (p *Plugin) delete(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	value, ok := pluginrpc.Object(frame)
	pluginID, instance, valid := reference(value)
	if !ok || !valid {
		p.replyError(frame, "invalid_request", "missing required fields: plugin, instance")
		return
	}
	p.mu.Lock()
	instances := p.data[pluginID]
	previous, existed := instances[instance]
	existed = existed && previous != nil
	delete(instances, instance)
	if err := storefile.Save(p.dbPath, p.data); err != nil {
		p.mu.Unlock()
		p.replyError(frame, "write_error", err.Error())
		return
	}
	if err := p.client.Set(context.Background(), MailboxChannel(pluginID, instance), nil); err != nil {
		p.mu.Unlock()
		slog.Error("publish instance tombstone", "error", err)
		return
	}
	p.mu.Unlock()
	p.reply(frame, map[string]any{"plugin": pluginID, "instance": instance, "existed": existed})
}

func (p *Plugin) list(frame busclient.Frame) {
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
	instances := make(map[string]any, len(p.data[pluginID]))
	for instance, state := range p.data[pluginID] {
		instances[instance] = cloneState(state)
	}
	p.mu.Unlock()
	p.reply(frame, instances)
}

func (p *Plugin) reply(frame busclient.Frame, result any) {
	if err := pluginrpc.Respond(p.client, frame, result); err != nil {
		slog.Error("instance-store RPC response failed", "error", err)
	}
}

func (p *Plugin) replyError(frame busclient.Frame, code, message string) {
	if err := pluginrpc.RespondError(p.client, frame, code, message); err != nil {
		slog.Error("instance-store RPC error response failed", "error", err)
	}
}

func reference(value map[string]any) (string, string, bool) {
	pluginID, pluginOK := stringField(value, "plugin")
	instance, instanceOK := stringField(value, "instance")
	return pluginID, instance, pluginOK && instanceOK && pluginID != "" && instance != ""
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

func cloneState(state any) any {
	object, ok := state.(map[string]any)
	if !ok {
		return state
	}
	clone := make(map[string]any, len(object))
	for key, value := range object {
		clone[key] = value
	}
	return clone
}
