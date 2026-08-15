package broker

import (
	"encoding/json"
	"sync"
	"time"

	"viewer/sdk/go/protocol"
)

type RegistryEntry struct {
	ID          string            `json:"id"`
	InstanceID  *string           `json:"instance_id"`
	Manifest    protocol.Manifest `json:"manifest"`
	Managed     bool              `json:"managed"`
	Conn        string            `json:"conn"`
	ConnectedAt int64             `json:"connected_at"`
}

type Registry struct {
	mu      sync.Mutex
	broker  *Broker
	entries map[string]RegistryEntry
	order   []string
}

func NewRegistry(b *Broker) *Registry {
	return &Registry{broker: b, entries: make(map[string]RegistryEntry)}
}

func (r *Registry) Register(hello protocol.Hello) RegistryEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UnixMilli()
	entry := RegistryEntry{
		ID: hello.Manifest.ID, InstanceID: hello.InstanceID, Manifest: hello.Manifest,
		Managed: hello.Managed, Conn: hello.Conn, ConnectedAt: now,
	}
	if _, exists := r.entries[hello.Conn]; !exists {
		r.order = append(r.order, hello.Conn)
	}
	r.entries[hello.Conn] = entry
	r.publishListLocked(now)
	r.publishLifecycleLocked(entry, "activated", now)
	return entry
}

func (r *Registry) Deregister(conn string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, exists := r.entries[conn]
	if !exists {
		return
	}
	delete(r.entries, conn)
	for i, item := range r.order {
		if item == conn {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	now := time.Now().UnixMilli()
	r.publishListLocked(now)
	r.publishLifecycleLocked(entry, "deactivated", now)
}

func (r *Registry) Entries() []RegistryEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entriesLocked()
}

func (r *Registry) entriesLocked() []RegistryEntry {
	entries := make([]RegistryEntry, 0, len(r.entries))
	for _, conn := range r.order {
		entries = append(entries, r.entries[conn])
	}
	return entries
}

func (r *Registry) publishListLocked(now int64) {
	value, _ := json.Marshal(r.entriesLocked())
	r.broker.Publish(protocol.Delivery{
		Type: "set", Channel: "plugins:_:list", Value: value, Depth: 0, TS: now,
		Origin: protocol.Origin{Plugin: protocol.KernelPluginID, Instance: protocol.DefaultInstanceID},
	}, "")
}

func (r *Registry) publishLifecycleLocked(entry RegistryEntry, state string, now int64) {
	value, _ := json.Marshal(map[string]any{"state": state, "conn": entry.Conn})
	r.broker.Publish(protocol.Delivery{
		Type: "publish", Channel: "plugins:" + entry.ID + ":lifecycle", Value: value,
		Depth: 0, TS: now,
		Origin: protocol.Origin{Plugin: protocol.KernelPluginID, Instance: protocol.DefaultInstanceID},
	}, "")
}
