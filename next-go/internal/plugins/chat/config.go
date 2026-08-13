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
