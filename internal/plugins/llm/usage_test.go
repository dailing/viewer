package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchKimiUsagePrefersCountersAndHandlesMissingRemaining(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/coding/v1/usages" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing bearer credential")
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"usage": map[string]any{"limit": "100", "used": "100", "resetTime": "2026-11-22T07:08:22.016154Z"},
			"limits": []any{map[string]any{
				"window": map[string]any{"duration": 300, "timeUnit": "TIME_UNIT_MINUTE"},
				"detail": map[string]any{"limit": "100", "used": "12", "remaining": "88", "resetTime": "2026-11-16T19:08:22.016154Z"},
			}},
			// Deliberately contradictory: live counter fields are authoritative.
			"usages": map[string]any{
				"limit_5h": map[string]any{"used_ratio": 0, "reset_time": "2026-11-16T19:08:22Z"},
				"limit_7d": map[string]any{"used_ratio": 0, "reset_time": "2026-11-22T07:08:22Z"},
			},
		})
	}))
	defer server.Close()
	windows, err := fetchKimiUsage(context.Background(), server.Client(), server.URL+"/coding/v1", "test-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 2 || windows[0].ID != "5h" || windows[1].ID != "7d" {
		t.Fatalf("windows = %#v", windows)
	}
	if windows[0].UsedPercent != 12 || windows[0].WindowSeconds != shortWindowSeconds || windows[0].ResetAt != "2026-11-16T19:08:22Z" {
		t.Errorf("5h window = %#v", windows[0])
	}
	if windows[1].UsedPercent != 100 || windows[1].WindowSeconds != weekWindowSeconds || windows[1].ResetAt != "2026-11-22T07:08:22Z" {
		t.Errorf("7d window = %#v", windows[1])
	}
}

func TestFetchKimiUsageRatioFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"usages":{"limit_5h":{"used_ratio":0.1243,"reset_time":"2026-11-16T19:08:22Z"},"limit_7d":{"used_ratio":0.1201,"reset_time":"2026-11-22T07:08:22Z"}}}`))
	}))
	defer server.Close()
	windows, err := fetchKimiUsage(context.Background(), server.Client(), server.URL+"/coding/v1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 2 || windows[0].UsedPercent != 12.43 || windows[1].UsedPercent != 12.01 {
		t.Fatalf("ratio windows = %#v", windows)
	}
}

func TestKimiUsageAccountsFiltersProvidersAndSupportsActiveOnly(t *testing.T) {
	active := Config{Endpoint: "https://api.kimi.com/coding/v1", APIKey: "a", Model: "k2.8"}
	profiles := []storedProfile{
		{ID: "deepseek", Endpoint: "https://api.deepseek.com/v1", Model: "deepseek"},
		{ID: "kimi-dl", Name: "Kimi DL", Endpoint: active.Endpoint, APIKey: "a", Model: "k2.8"},
	}
	activeID, accounts, targets := kimiUsageAccounts(active, profiles)
	if activeID != "kimi-dl" || len(accounts) != 1 || len(targets) != 1 || !accounts[0].Active {
		t.Fatalf("activeID=%q accounts=%#v targets=%#v", activeID, accounts, targets)
	}
	activeID, accounts, targets = kimiUsageAccounts(active, nil)
	if activeID != "active" || len(accounts) != 1 || len(targets) != 1 || targets[0].APIKey != "a" {
		t.Fatalf("active-only activeID=%q accounts=%#v targets=%#v", activeID, accounts, targets)
	}
}
