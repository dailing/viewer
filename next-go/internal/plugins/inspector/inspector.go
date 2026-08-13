// Package inspector implements the bus-inspector debugging plugin.
package inspector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"viewer/internal/busclient"
	"viewer/internal/protocol"
)

const (
	MatchesChannel = "bus-inspector:_:matches"
	StatsChannel   = "bus-inspector:_:stats"
	SnapshotBudget = 800_000
)

var Manifest = busclient.Manifest{
	ID: "bus-inspector", Version: "0.1.0",
	Slots: map[string]any{
		"set-filter": map[string]any{}, "pause": map[string]any{},
		"resume": map[string]any{}, "clear": map[string]any{}, "snapshot": map[string]any{},
	},
	Emits: map[string]any{"matches": map[string]any{}, "stats": map[string]any{}},
}

type Config struct {
	KernelWS      string
	RingSize      int
	Echo          bool
	EmitThreshold int
	StatsInterval time.Duration
}

func DefaultConfig() Config {
	return Config{RingSize: 5000, EmitThreshold: 200, StatsInterval: time.Second}
}

type Entry struct {
	Seq     uint64            `json:"seq"`
	TS      int64             `json:"ts"`
	Type    string            `json:"type"`
	Channel string            `json:"channel"`
	Origin  map[string]string `json:"origin"`
	TraceID string            `json:"trace_id"`
	Depth   int               `json:"depth"`
	Value   any               `json:"value"`
}

type Stats struct {
	Captured   uint64            `json:"captured"`
	Emitted    uint64            `json:"emitted"`
	Dropped    uint64            `json:"dropped"`
	RatePerSec float64           `json:"rate_per_sec"`
	Paused     bool              `json:"paused"`
	Filter     map[string]string `json:"filter"`
	RingSize   int               `json:"ring_size"`
	RingUsed   int               `json:"ring_used"`
}

type Plugin struct {
	config                     Config
	client                     *busclient.Client
	mu                         sync.Mutex
	ring                       []Entry
	seq                        uint64
	filter                     map[string]string
	paused                     bool
	captured, emitted, dropped uint64
	windowCaptured             uint64
	rate                       float64
	windowStart                time.Time
	emitWindowStart            time.Time
	windowEmitted              int
}

func New(config Config) (*Plugin, error) {
	defaults := DefaultConfig()
	if config.KernelWS == "" {
		return nil, fmt.Errorf("kernel websocket is required")
	}
	if config.RingSize == 0 {
		config.RingSize = defaults.RingSize
	}
	if config.RingSize < 1 {
		return nil, fmt.Errorf("ring size must be positive")
	}
	if config.EmitThreshold <= 0 {
		config.EmitThreshold = defaults.EmitThreshold
	}
	if config.StatsInterval <= 0 {
		config.StatsInterval = defaults.StatsInterval
	}
	now := time.Now()
	return &Plugin{config: config, filter: make(map[string]string), windowStart: now, emitWindowStart: now}, nil
}

func (p *Plugin) Run(ctx context.Context) error {
	managed := os.Getenv("VIEWER_MANAGED") == "1"
	p.client = busclient.New(p.config.KernelWS, Manifest, busclient.WithManaged(managed))
	if _, err := p.client.Subscribe(">", p.capture); err != nil {
		return err
	}
	handlers := map[string]func(busclient.Frame){
		"set-filter": p.setFilter, "pause": p.pause, "resume": p.resume,
		"clear": p.clear, "snapshot": p.snapshot,
	}
	for name, handler := range handlers {
		if _, err := p.client.Subscribe("bus-inspector:_:"+name, handler); err != nil {
			return err
		}
	}
	if err := p.client.Connect(ctx); err != nil {
		return fmt.Errorf("connect bus-inspector: %w", err)
	}
	p.publishStats()
	ticker := time.NewTicker(p.config.StatsInterval)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			p.mu.Lock()
			elapsed := now.Sub(p.windowStart).Seconds()
			if elapsed > 0 {
				p.rate = float64(p.windowCaptured) / elapsed
			}
			p.windowCaptured, p.windowStart = 0, now
			p.mu.Unlock()
			p.publishStats()
		case <-ctx.Done():
			return p.client.Close()
		}
	}
}

func (p *Plugin) capture(frame busclient.Frame) {
	if frame.Origin != nil && frame.Origin.Plugin == Manifest.ID {
		return
	}
	origin := map[string]string{}
	if frame.Origin != nil {
		origin["plugin"], origin["instance"] = frame.Origin.Plugin, frame.Origin.Instance
	}
	p.mu.Lock()
	p.seq++
	entry := Entry{Seq: p.seq, TS: frame.TS, Type: frame.Type, Channel: frame.Channel,
		Origin: origin, TraceID: frame.TraceID, Depth: frame.Depth, Value: frame.Value}
	if len(p.ring) == p.config.RingSize {
		copy(p.ring, p.ring[1:])
		p.ring[len(p.ring)-1] = entry
	} else {
		p.ring = append(p.ring, entry)
	}
	p.captured++
	p.windowCaptured++
	paused, matches := p.paused, p.matchesLocked(entry)
	now := time.Now()
	shouldEmit := !paused && matches
	if shouldEmit {
		if now.Sub(p.emitWindowStart) >= time.Second {
			p.emitWindowStart, p.windowEmitted = now, 0
		}
		if p.windowEmitted >= p.config.EmitThreshold {
			p.dropped++
			shouldEmit = false
		} else {
			p.windowEmitted++
			p.emitted++
		}
	}
	echo := p.config.Echo
	p.mu.Unlock()
	if echo {
		value, _ := json.Marshal(entry.Value)
		if len(value) > 200 {
			value = value[:200]
		}
		fmt.Printf("%d %8s %s %s %s\n", entry.TS, entry.Type, entry.Channel, entry.Origin["plugin"], value)
	}
	if shouldEmit && p.client != nil {
		if err := p.client.Publish(context.Background(), MatchesChannel, entry); err != nil {
			slog.Warn("publish inspector match", "error", err)
		}
	}
}

func (p *Plugin) matchesLocked(entry Entry) bool {
	f := p.filter
	if pattern := f["channel"]; pattern != "" && !protocol.ChannelMatches(pattern, entry.Channel) {
		return false
	}
	if value := f["type"]; value != "" && value != entry.Type {
		return false
	}
	if value := f["origin"]; value != "" && value != entry.Origin["plugin"] {
		return false
	}
	if value := f["trace_id"]; value != "" && value != entry.TraceID {
		return false
	}
	if text := f["text"]; text != "" {
		encoded, _ := json.Marshal(entry.Value)
		if !strings.Contains(string(encoded), text) {
			return false
		}
	}
	return true
}

func (p *Plugin) setFilter(frame busclient.Frame) {
	request, _ := frame.Value.(map[string]any)
	if request["_cancel"] == true {
		return
	}
	filter := make(map[string]string)
	for _, key := range []string{"channel", "type", "origin", "trace_id", "text"} {
		if value, ok := request[key].(string); ok && value != "" {
			filter[key] = value
		}
	}
	p.mu.Lock()
	p.filter = filter
	p.mu.Unlock()
	p.publishStats()
	p.respond(request, map[string]any{"filter": filter})
}

func (p *Plugin) pause(frame busclient.Frame) {
	request, _ := frame.Value.(map[string]any)
	if request["_cancel"] == true {
		return
	}
	p.mu.Lock()
	p.paused = true
	p.mu.Unlock()
	p.publishStats()
	p.respond(request, map[string]any{"paused": true})
}

func (p *Plugin) resume(frame busclient.Frame) {
	request, _ := frame.Value.(map[string]any)
	if request["_cancel"] == true {
		return
	}
	p.mu.Lock()
	p.paused = false
	p.mu.Unlock()
	p.publishStats()
	p.respond(request, map[string]any{"paused": false})
}

func (p *Plugin) clear(frame busclient.Frame) {
	request, _ := frame.Value.(map[string]any)
	if request["_cancel"] == true {
		return
	}
	p.mu.Lock()
	p.ring = nil
	p.mu.Unlock()
	p.publishStats()
	p.respond(request, map[string]any{"cleared": true})
}

func (p *Plugin) snapshot(frame busclient.Frame) {
	request, _ := frame.Value.(map[string]any)
	if request["_cancel"] == true {
		return
	}
	limit := 100
	if raw, ok := request["limit"].(float64); ok {
		limit = int(raw)
	}
	if limit < 1 {
		limit = 1
	}
	var before uint64
	if raw, ok := request["before_seq"].(float64); ok && raw > 0 {
		before = uint64(raw)
	}
	p.mu.Lock()
	ring := append([]Entry(nil), p.ring...)
	p.mu.Unlock()
	p.respond(request, map[string]any{"entries": snapshotEntries(ring, limit, before)})
}

func snapshotEntries(ring []Entry, limit int, before uint64) []Entry {
	entries := make([]Entry, 0, limit)
	used := 0
	for i := len(ring) - 1; i >= 0 && len(entries) < limit; i-- {
		entry := ring[i]
		if before != 0 && entry.Seq >= before {
			continue
		}
		encoded, _ := json.Marshal(entry)
		size := len(encoded)
		if len(entries) > 0 {
			size++
		}
		if len(entries) > 0 && used+size > SnapshotBudget {
			break
		}
		entries = append(entries, entry)
		used += size
	}
	return entries
}

func (p *Plugin) publishStats() {
	if p.client == nil || !p.client.Connected() {
		return
	}
	p.mu.Lock()
	filter := make(map[string]string, len(p.filter))
	for key, value := range p.filter {
		filter[key] = value
	}
	stats := Stats{Captured: p.captured, Emitted: p.emitted, Dropped: p.dropped,
		RatePerSec: p.rate, Paused: p.paused, Filter: filter,
		RingSize: p.config.RingSize, RingUsed: len(p.ring)}
	p.mu.Unlock()
	if err := p.client.Set(context.Background(), StatsChannel, stats); err != nil {
		slog.Warn("publish inspector stats", "error", err)
	}
}

func (p *Plugin) respond(request map[string]any, result any) {
	reply, replyOK := request["_reply_to"].(string)
	corr, corrOK := request["_corr"].(string)
	if !replyOK || !corrOK {
		return
	}
	_ = p.client.Publish(context.Background(), reply, map[string]any{"_corr": corr, "ok": true, "result": result})
}
