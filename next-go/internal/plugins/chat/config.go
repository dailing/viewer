package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

func decodeInto(value any, target any) error {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func (p *Plugin) configGet(ctx context.Context, key string, target any) error {
	if p.client == nil {
		return errors.New("config-store unavailable")
	}
	value, err := p.client.Request(ctx, "config:_:get", map[string]any{"plugin": configNamespace, "key": key}, 5*time.Second)
	if err != nil {
		return fmt.Errorf("read %s config: %w", key, err)
	}
	return decodeInto(value, target)
}

func (p *Plugin) configSet(ctx context.Context, key string, value any) error {
	_, err := p.client.Request(ctx, "config:_:set", map[string]any{"plugin": configNamespace, "key": key, "value": value}, 5*time.Second)
	if err != nil {
		return fmt.Errorf("write %s config: %w", key, err)
	}
	return nil
}

func (p *Plugin) workspace(ctx context.Context) (Workspace, error) {
	result := defaultWorkspace()
	var base struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		CommonPrompt string `json:"common_prompt"`
	}
	if err := p.configGet(ctx, "workspace", &base); err != nil {
		return result, err
	}
	if base.ID != "" {
		result.ID = base.ID
	}
	if base.Name != "" {
		result.Name = base.Name
	}
	result.CommonPrompt = base.CommonPrompt
	if err := p.configGet(ctx, "roles", &result.Roles); err != nil {
		return result, err
	}
	var routing RoutingConfig
	if err := p.configGet(ctx, "routing", &routing); err != nil {
		return result, err
	}
	result.RoutingPolicies, result.DefaultRoutingPolicyID = routing.RoutingPolicies, routing.DefaultRoutingPolicyID
	if result.Roles == nil {
		result.Roles = []SuperRole{}
	}
	if result.RoutingPolicies == nil {
		result.RoutingPolicies = []RoutingPolicyConfig{}
	}
	return result, nil
}

func roleByID(roles []SuperRole, id string) (SuperRole, bool) {
	for _, role := range roles {
		if role.ID == id {
			return role, true
		}
	}
	return SuperRole{}, false
}

func (p *Plugin) llmConfig(ctx context.Context) (LLMConfig, error) {
	var result LLMConfig
	if err := p.configGet(ctx, "llm", &result); err != nil {
		return result, err
	}
	// Accept both the aggregate object and the literal llm.* namespace keys.
	for key, target := range map[string]*string{"llm.endpoint": &result.Endpoint, "llm.key": &result.APIKey, "llm.model": &result.Model} {
		if *target != "" {
			continue
		}
		if err := p.configGet(ctx, key, target); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (p *Plugin) summaryConfig(ctx context.Context) SummaryConfig {
	result := SummaryConfig{Enabled: true, ToolCharBudget: 4000, TimeoutSeconds: 60, ContextEnabled: true, SummaryCharBudget: 8000, TailWordBudget: defaultHistoryWordBudget}
	_ = p.configGet(ctx, "turn_summary", &result)
	if result.ToolCharBudget < 0 {
		result.ToolCharBudget = 0
	}
	if result.TimeoutSeconds <= 0 {
		result.TimeoutSeconds = 60
	}
	if result.SummaryCharBudget < 0 {
		result.SummaryCharBudget = 0
	}
	if result.TailWordBudget < 0 {
		result.TailWordBudget = 0
	}
	return result
}

func (p *Plugin) hindsightConfig(ctx context.Context) HindsightConfig {
	result := HindsightConfig{BankPrefix: "super-workspace", TimeoutSeconds: 10, MaxTokens: 800, Limit: 8}
	_ = p.configGet(ctx, "hindsight", &result)
	for key, target := range map[string]*string{"hindsight.endpoint": &result.Endpoint, "hindsight.token": &result.Token, "hindsight.bank_prefix": &result.BankPrefix} {
		if *target == "" {
			_ = p.configGet(ctx, key, target)
		}
	}
	if result.TimeoutSeconds <= 0 {
		result.TimeoutSeconds = 10
	}
	if result.MaxTokens < 0 {
		result.MaxTokens = 0
	}
	if result.Limit <= 0 {
		result.Limit = 8
	}
	return result
}
