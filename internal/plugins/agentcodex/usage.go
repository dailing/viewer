package agentcodex

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"viewer/internal/usage"
	"viewer/sdk/go/busclient"
)

// handleUsage performs a short-lived App Server RPC. Codex retains ownership
// of ChatGPT OAuth tokens and refresh; the browser receives only quota fields.
const usageCacheTTL = time.Minute

type codexUsageCache struct {
	mu      sync.Mutex
	at      time.Time
	account *usage.Account
}

func (p *Plugin) handleUsage(frame busclient.Frame) {
	var request struct {
		Force bool `json:"force"`
	}
	_ = decodeFrame(frame, &request)
	if !envBool("VIEWER_CODEX_APP_SERVER_ENABLED", true) {
		p.respond(frame, nil, errors.New("codex app-server is disabled"))
		return
	}
	p.usageCache.mu.Lock()
	defer p.usageCache.mu.Unlock()
	if !request.Force && p.usageCache.account != nil && time.Now().Before(p.usageCache.at.Add(usageCacheTTL)) {
		p.respond(frame, *p.usageCache.account, nil)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := newClient(ctx)
	if err != nil {
		p.respond(frame, nil, err)
		return
	}
	defer client.Close()
	value, err := client.AccountRateLimits(ctx)
	if err != nil {
		p.respond(frame, nil, err)
		return
	}
	account, err := codexUsageAccount(value)
	if err == nil {
		cached := account
		p.usageCache.account = &cached
		p.usageCache.at = time.Now()
	}
	p.respond(frame, account, err)
}

func codexUsageAccount(value map[string]any) (usage.Account, error) {
	account := usage.Account{ID: "codex", Name: "Codex GPT", Provider: "codex", FetchedAt: time.Now().UTC().Format(time.RFC3339), Windows: []usage.Window{}}
	snapshot := objectValue(value["rateLimits"])
	if buckets := objectValue(value["rateLimitsByLimitId"]); buckets != nil {
		if codex := objectValue(buckets["codex"]); codex != nil {
			snapshot = codex
		}
	}
	if snapshot == nil {
		return account, errors.New("codex rate-limit response has no codex snapshot")
	}
	account.Plan, _ = snapshot["planType"].(string)
	for _, slot := range []string{"primary", "secondary"} {
		window := objectValue(snapshot[slot])
		if window == nil {
			continue
		}
		minutes, ok := numberValue(window["windowDurationMins"])
		if !ok || minutes <= 0 {
			continue
		}
		used, ok := numberValue(window["usedPercent"])
		if !ok {
			continue
		}
		seconds := int64(minutes * 60)
		id := ""
		switch seconds {
		case 5 * 60 * 60:
			id = "5h"
		case 7 * 24 * 60 * 60:
			id = "7d"
		default:
			continue
		}
		item := usage.Window{ID: id, UsedPercent: min(max(used, 0), 100), WindowSeconds: seconds}
		if reset, exists := numberValue(window["resetsAt"]); exists && reset > 0 {
			item.ResetAt = time.Unix(int64(reset), 0).UTC().Format(time.RFC3339)
		}
		account.Windows = append(account.Windows, item)
	}
	if len(account.Windows) == 0 {
		return account, fmt.Errorf("codex rate-limit response has no 5-hour or weekly windows")
	}
	return account, nil
}

func objectValue(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func numberValue(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok
}
