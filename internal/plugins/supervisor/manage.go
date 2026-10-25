// Plugin-manager RPC surface (framework v0.31): the supervisor doubles as
// the plugin manager for external plugins. Registry entries carry a launch
// command line (default <path>/backend/run), are persisted to registry.json
// atomically, and start manually (or via autostart); a failing plugin is
// retried up to BreakerMaxCrash consecutive times, then marked broken.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"viewer/internal/plugins/storefile"
	"viewer/sdk/go/busclient"
)

var pluginIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

// entryPayload is the manager-facing view of one registry entry plus its
// live process state (supervisor:_:list reply and pane rows).
func (p *Plugin) entryPayload(item *managedPlugin) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	payload := map[string]any{
		"id":        item.id,
		"path":      item.entry.Path,
		"command":   item.entry.Command,
		"enabled":   item.entry.enabled(),
		"autostart": item.entry.Autostart != nil && *item.entry.Autostart,
		"state":     item.state,
		"crashes":   len(item.crashes),
	}
	if item.entry.Name != "" {
		payload["name"] = item.entry.Name
	}
	// Inline the pid read: pid() takes p.mu, which we already hold here.
	if item.cmd != nil && item.cmd.Process != nil {
		payload["pid"] = item.cmd.Process.Pid
	}
	if item.exitCode != nil {
		payload["exit_code"] = *item.exitCode
	}
	return payload
}

func (p *Plugin) listRPC(frame busclient.Frame) {
	if _, ok := managerObject(frame); !ok {
		return
	}
	p.mu.Lock()
	items := make([]*managedPlugin, 0, len(p.managed))
	for _, item := range p.managed {
		items = append(items, item)
	}
	p.mu.Unlock()
	plugins := make([]map[string]any, 0, len(items))
	for _, item := range items {
		plugins = append(plugins, p.entryPayload(item))
	}
	sort.Slice(plugins, func(i, j int) bool { return plugins[i]["id"].(string) < plugins[j]["id"].(string) })
	p.respond(frame, map[string]any{"plugins": plugins})
}

// upsertRPC adds or updates a registry entry and persists the registry. A
// running plugin keeps its current process; the new launch config applies on
// the next start.
func (p *Plugin) upsertRPC(frame busclient.Frame) {
	value, ok := managerObject(frame)
	if !ok {
		return
	}
	entry, err := decodeEntry(value)
	if err != nil {
		p.respondError(frame, "bad_request", err.Error())
		return
	}
	if err = p.validateEntry(entry); err != nil {
		p.respondError(frame, "bad_request", err.Error())
		return
	}
	p.registryMu.Lock()
	p.mu.Lock()
	item := p.managed[entry.ID]
	created := item == nil
	var previous registryEntry
	if created {
		item = &managedPlugin{id: entry.ID, state: StateStopped}
		p.managed[entry.ID] = item
	} else {
		previous = item.entry
	}
	p.registered[entry.ID] = true
	item.entry = entry
	item.path = entry.Path
	p.mu.Unlock()
	if err = p.saveRegistryLocked(); err != nil {
		p.mu.Lock()
		if created {
			delete(p.managed, entry.ID)
			delete(p.registered, entry.ID)
		} else {
			item.entry = previous
			item.path = previous.Path
		}
		p.mu.Unlock()
		p.registryMu.Unlock()
		p.respondError(frame, "persist_failed", err.Error())
		return
	}
	p.registryMu.Unlock()
	p.publishStates()
	p.respond(frame, p.entryPayload(item))
}

// deleteRPC stops the plugin, removes the registry entry, and asks the
// gateway to drop its frontend assets (mailbox update unloads the shell side).
func (p *Plugin) deleteRPC(frame busclient.Frame) {
	value, ok := managerObject(frame)
	if !ok {
		return
	}
	id, _ := value["id"].(string)
	p.registryMu.Lock()
	p.mu.Lock()
	item := p.managed[id]
	p.mu.Unlock()
	if item == nil {
		p.registryMu.Unlock()
		p.respondError(frame, "not_found", "no such managed plugin: "+id)
		return
	}
	item.opMu.Lock()
	p.stopOneLocked(item, true)
	p.mu.Lock()
	delete(p.managed, id)
	delete(p.registered, id)
	p.mu.Unlock()
	if err := p.saveRegistryLocked(); err != nil {
		p.mu.Lock()
		p.managed[id] = item
		p.registered[id] = true
		p.mu.Unlock()
		item.opMu.Unlock()
		p.registryMu.Unlock()
		p.publishStates()
		p.respondError(frame, "persist_failed", err.Error())
		return
	}
	item.opMu.Unlock()
	p.registryMu.Unlock()
	p.publishStates()
	p.publishLifecycle(id, "deleted", nil)
	if p.client != nil && p.client.Connected() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = p.client.Request(ctx, "gateway:_:assets:remove", map[string]any{"id": id}, 5*time.Second)
	}
	p.respond(frame, map[string]any{"deleted": id})
}

func (p *Plugin) startRPC(frame busclient.Frame) {
	p.startOrRestartRPC(frame, false)
}

func (p *Plugin) stopRPC(frame busclient.Frame) {
	value, ok := managerObject(frame)
	if !ok {
		return
	}
	id, _ := value["id"].(string)
	p.mu.Lock()
	item := p.managed[id]
	p.mu.Unlock()
	if item == nil {
		p.respondError(frame, "not_found", "no such managed plugin: "+id)
		return
	}
	go func() {
		p.stopOne(item, true)
		p.publishStates()
		p.respond(frame, map[string]any{"id": id, "state": StateStopped})
	}()
}

// managerObject unwraps an RPC frame value; cancelled probes are dropped.
func managerObject(frame busclient.Frame) (map[string]any, bool) {
	value, _ := frame.Value.(map[string]any)
	if value == nil || value["_cancel"] == true {
		return nil, false
	}
	return value, true
}

func decodeEntry(value map[string]any) (registryEntry, error) {
	var entry registryEntry
	entry.ID, _ = value["id"].(string)
	entry.Name, _ = value["name"].(string)
	entry.Path, _ = value["path"].(string)
	if raw, exists := value["command"]; exists {
		list, ok := raw.([]any)
		if !ok {
			return entry, errors.New("command must be an array of argv strings")
		}
		for _, item := range list {
			part, ok := item.(string)
			if !ok || part == "" {
				return entry, errors.New("command must be an array of argv strings")
			}
			entry.Command = append(entry.Command, part)
		}
	}
	if raw, exists := value["enabled"]; exists {
		enabled, ok := raw.(bool)
		if !ok {
			return entry, errors.New("enabled must be a boolean")
		}
		entry.Enabled = &enabled
	}
	if raw, exists := value["autostart"]; exists {
		autostart, ok := raw.(bool)
		if !ok {
			return entry, errors.New("autostart must be a boolean")
		}
		entry.Autostart = &autostart
	}
	return entry, nil
}

// validateEntry enforces the launch contract: a valid id, an existing
// directory, and either an explicit command line or a backend/run entry.
func (p *Plugin) validateEntry(entry registryEntry) error {
	if !pluginIDPattern.MatchString(entry.ID) {
		return errors.New("id must match ^[a-z0-9][a-z0-9_.-]*$")
	}
	if entry.Path == "" {
		return errors.New("path is required")
	}
	info, err := os.Stat(entry.Path)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("path is not an existing directory: %s", entry.Path)
	}
	if len(entry.Command) > 0 {
		if strings.TrimSpace(entry.Command[0]) == "" {
			return errors.New("command[0] must be a non-empty executable")
		}
		return nil
	}
	run := filepath.Join(entry.Path, "backend", "run")
	runInfo, err := os.Stat(run)
	if err != nil || runInfo.IsDir() {
		return errors.New("no command given and backend/run is missing")
	}
	return nil
}

// saveRegistry atomically persists a complete, deterministic registry snapshot.
func (p *Plugin) saveRegistry() error {
	p.registryMu.Lock()
	defer p.registryMu.Unlock()
	return p.saveRegistryLocked()
}

// registryMu must be held by the caller. Disk I/O is deliberately outside p.mu
// so process readers stay responsive while manager mutations are serialized.
func (p *Plugin) saveRegistryLocked() error {
	if p.config.RegistryPath == "" {
		return errors.New("no registry path configured")
	}
	p.mu.Lock()
	entries := make([]registryEntry, 0, len(p.managed))
	for _, item := range p.managed {
		entries = append(entries, item.entry)
	}
	p.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return storefile.Save(p.config.RegistryPath, registry{Plugins: entries})
}
