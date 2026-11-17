package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"viewer/internal/plugins/pluginrpc"
	"viewer/internal/usage"
	"viewer/sdk/go/busclient"
)

const (
	shortWindowSeconds = 5 * 60 * 60
	weekWindowSeconds  = 7 * 24 * 60 * 60
	usageCacheTTL      = time.Minute
)

type kimiUsageCache struct {
	mu    sync.Mutex
	at    time.Time
	reply map[string]any
}

type usageNumberEntry struct {
	Limit     any    `json:"limit"`
	Used      any    `json:"used"`
	Remaining any    `json:"remaining"`
	ResetTime string `json:"resetTime"`
}

type usageRatioEntry struct {
	UsedRatio *float64 `json:"used_ratio"`
	ResetTime string   `json:"reset_time"`
}

type kimiUsagePayload struct {
	Usage  usageNumberEntry `json:"usage"`
	Limits []struct {
		Window struct {
			Duration int    `json:"duration"`
			TimeUnit string `json:"timeUnit"`
		} `json:"window"`
		Detail usageNumberEntry `json:"detail"`
	} `json:"limits"`
	Usages struct {
		Limit5h usageRatioEntry `json:"limit_5h"`
		Limit7d usageRatioEntry `json:"limit_7d"`
	} `json:"usages"`
}

// handleUsage returns every configured Kimi Coding Plan account. Quota reads
// deliberately bypass the completion fallback chain and its circuit breaker:
// each named profile represents a distinct credential whose own quota matters.
func (p *Plugin) handleUsage(frame busclient.Frame) {
	raw, _ := pluginrpc.Object(frame)
	force, _ := raw["force"].(bool)
	p.usageCache.mu.Lock()
	defer p.usageCache.mu.Unlock()
	if !force && p.usageCache.reply != nil && time.Now().Before(p.usageCache.at.Add(usageCacheTTL)) {
		_ = pluginrpc.Respond(p.client, frame, p.usageCache.reply)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	active, profiles, err := p.readUsageConfigs(ctx)
	if err != nil {
		_ = pluginrpc.RespondError(p.client, frame, "config_error", err.Error())
		return
	}
	activeID, accounts, targets := kimiUsageAccounts(active, profiles)
	var wg sync.WaitGroup
	for index := range accounts {
		if accounts[index].Error != "" {
			continue
		}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			windows, err := fetchKimiUsage(ctx, p.httpClient, targets[index].Endpoint, targets[index].APIKey)
			if err != nil {
				accounts[index].Error = err.Error()
				accounts[index].Windows = []usage.Window{}
				return
			}
			accounts[index].Windows = windows
		}(index)
	}
	wg.Wait()
	reply := map[string]any{"active_profile_id": activeID, "accounts": accounts}
	p.usageCache.reply = reply
	p.usageCache.at = time.Now()
	_ = pluginrpc.Respond(p.client, frame, reply)
}

func (p *Plugin) readUsageConfigs(ctx context.Context) (Config, []storedProfile, error) {
	value, err := p.client.Request(ctx, "config:_:get", map[string]any{"plugin": configNamespace, "key": "active"}, rpcBudget)
	if err != nil {
		return Config{}, nil, fmt.Errorf("read llm config: %w", err)
	}
	var active Config
	if value != nil {
		encoded, _ := json.Marshal(value)
		if err := json.Unmarshal(encoded, &active); err != nil {
			return Config{}, nil, fmt.Errorf("decode llm config: %w", err)
		}
	}
	value, err = p.client.Request(ctx, "config:_:get", map[string]any{"plugin": configNamespace, "key": "profiles"}, rpcBudget)
	if err != nil {
		return Config{}, nil, fmt.Errorf("read llm profiles: %w", err)
	}
	var profiles []storedProfile
	if value != nil {
		encoded, _ := json.Marshal(value)
		if err := json.Unmarshal(encoded, &profiles); err != nil {
			return Config{}, nil, fmt.Errorf("decode llm profiles: %w", err)
		}
	}
	return active, profiles, nil
}

// kimiUsageAccounts aligns accounts with the profiles slice. An active-only
// Kimi config is appended so the panel also works without a library entry.
func kimiUsageAccounts(active Config, profiles []storedProfile) (string, []usage.Account, []storedProfile) {
	activeID := ""
	accounts := []usage.Account{}
	targets := []storedProfile{}
	now := time.Now().UTC().Format(time.RFC3339)
	for index, profile := range profiles {
		if !isKimiCodeEndpoint(profile.Endpoint) {
			continue
		}
		id := strings.TrimSpace(profile.ID)
		if id == "" {
			id = fmt.Sprintf("profile-%d", index)
		}
		name := strings.TrimSpace(profile.Name)
		if name == "" {
			name = "Kimi " + id
		}
		isActive := sameUsageConfig(active, profile)
		if isActive {
			activeID = id
		}
		accounts = append(accounts, usage.Account{ID: id, Name: name, Provider: "kimi", Model: profile.Model, Active: isActive, FetchedAt: now, Windows: []usage.Window{}})
		targets = append(targets, profile)
	}
	if activeID == "" && isKimiCodeEndpoint(active.Endpoint) {
		activeID = "active"
		targets = append(targets, storedProfile{ID: activeID, Name: "Kimi active", Endpoint: active.Endpoint, APIKey: active.APIKey, Model: active.Model})
		accounts = append(accounts, usage.Account{ID: activeID, Name: "Kimi active", Provider: "kimi", Model: active.Model, Active: true, FetchedAt: now, Windows: []usage.Window{}})
	}
	return activeID, accounts, targets
}

func sameUsageConfig(active Config, profile storedProfile) bool {
	return normalizeEndpoint(active.Endpoint) == normalizeEndpoint(profile.Endpoint) && active.Model == profile.Model && active.APIKey == profile.APIKey
}

func isKimiCodeEndpoint(endpoint string) bool {
	return strings.HasSuffix(normalizeEndpoint(endpoint), "/coding/v1")
}

func fetchKimiUsage(ctx context.Context, client *http.Client, endpoint, apiKey string) ([]usage.Window, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, normalizeEndpoint(endpoint)+"/usages", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 32<<10))
		return nil, fmt.Errorf("Kimi usage returned HTTP %d", response.StatusCode)
	}
	var payload kimiUsagePayload
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Kimi usage: %w", err)
	}
	windows := make([]usage.Window, 0, 2)
	for _, item := range payload.Limits {
		if item.Window.Duration == 300 && strings.TrimPrefix(item.Window.TimeUnit, "TIME_UNIT_") == "MINUTE" {
			if percent, ok := counterUsedPercent(item.Detail); ok {
				windows = append(windows, usage.Window{ID: "5h", UsedPercent: percent, ResetAt: normalizeResetTime(item.Detail.ResetTime), WindowSeconds: shortWindowSeconds})
			}
			break
		}
	}
	if !hasWindow(windows, "5h") && payload.Usages.Limit5h.UsedRatio != nil {
		windows = append(windows, usage.Window{ID: "5h", UsedPercent: ratioPercent(*payload.Usages.Limit5h.UsedRatio), ResetAt: normalizeResetTime(payload.Usages.Limit5h.ResetTime), WindowSeconds: shortWindowSeconds})
	}
	if percent, ok := counterUsedPercent(payload.Usage); ok {
		windows = append(windows, usage.Window{ID: "7d", UsedPercent: percent, ResetAt: normalizeResetTime(payload.Usage.ResetTime), WindowSeconds: weekWindowSeconds})
	} else if payload.Usages.Limit7d.UsedRatio != nil {
		windows = append(windows, usage.Window{ID: "7d", UsedPercent: ratioPercent(*payload.Usages.Limit7d.UsedRatio), ResetAt: normalizeResetTime(payload.Usages.Limit7d.ResetTime), WindowSeconds: weekWindowSeconds})
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("Kimi usage returned no rate-limit windows")
	}
	return windows, nil
}

func counterUsedPercent(entry usageNumberEntry) (float64, bool) {
	limit, ok := usageFloat(entry.Limit)
	if !ok || limit <= 0 {
		return 0, false
	}
	if remaining, exists := usageFloat(entry.Remaining); exists {
		return clampPercent((limit - remaining) / limit * 100), true
	}
	if used, exists := usageFloat(entry.Used); exists {
		return clampPercent(used / limit * 100), true
	}
	return 0, false
}

func usageFloat(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func ratioPercent(ratio float64) float64 { return clampPercent(ratio * 100) }

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func hasWindow(windows []usage.Window, id string) bool {
	for _, window := range windows {
		if window.ID == id {
			return true
		}
	}
	return false
}

func normalizeResetTime(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339)
}
