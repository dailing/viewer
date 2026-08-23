package chat

import "strings"

const configNamespace = "plugins.viewer-chat"

type SuperRole struct {
	ID                    string   `gorm:"primaryKey" json:"id"`
	Name                  string   `json:"name"`
	Description           string   `json:"description"`
	Prompt                string   `json:"prompt"`
	CWD                   string   `json:"cwd"`
	RoutingPolicyID       string   `json:"routing_policy_id"`
	SessionPolicy         string   `json:"session_policy"`
	ContextRecyclePercent *float64 `json:"context_recycle_percent"`
	ContextRecycleTokens  *int     `json:"context_recycle_tokens"`
	CreatedAt             int64    `json:"created_at"`
	UpdatedAt             int64    `json:"updated_at"`
	// Legacy C1 migration inputs; never persisted to the roles table.
	Provider string  `gorm:"-" json:"provider,omitempty"`
	Model    *string `gorm:"-" json:"model,omitempty"`
}

func (SuperRole) TableName() string { return "roles" }

type Workspace struct {
	ID                     string                `json:"id"`
	Name                   string                `json:"name"`
	CommonPrompt           string                `json:"common_prompt"`
	Roles                  []SuperRole           `json:"roles"`
	RoutingPolicies        []RoutingPolicyConfig `json:"routing_policies"`
	DefaultRoutingPolicyID string                `json:"default_routing_policy_id"`
}
type RoutingCandidateConfig struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	TargetID    string         `json:"target_id"`
	AgentID     string         `json:"agent_id"`
	ProviderID  string         `json:"provider_id"`
	ModelID     string         `json:"model_id"`
	SelectionID string         `json:"selection_id"`
	Enabled     bool           `json:"enabled"`
	Parameters  map[string]any `json:"parameters"`
}
type RoutingPolicyConfig struct {
	ID              string                   `json:"id"`
	Name            string                   `json:"name"`
	Description     string                   `json:"description"`
	Enabled         bool                     `json:"enabled"`
	AutoFailover    bool                     `json:"auto_failover"`
	MaxAttempts     int                      `json:"max_attempts"`
	CooldownSeconds int                      `json:"cooldown_seconds"`
	Candidates      []RoutingCandidateConfig `json:"candidates"`
}
type RoutingConfig struct {
	DefaultRoutingPolicyID string                `json:"default_routing_policy_id"`
	RoutingPolicies        []RoutingPolicyConfig `json:"routing_policies"`
}

type RoutingPolicyRow struct {
	ID             string `gorm:"primaryKey"`
	Name           string
	CandidatesJSON string `gorm:"column:candidates_json"`
	AutoFailover   bool
	MaxAttempts    int
	CreatedAt      int64
	UpdatedAt      int64
}

func (RoutingPolicyRow) TableName() string { return "routing_policies" }

type storedRoutingCandidate struct {
	Agent      string         `json:"agent"`
	Provider   string         `json:"provider"`
	Model      string         `json:"model"`
	Parameters map[string]any `json:"parameters"`
	Enabled    bool           `json:"enabled"`
}
type SummaryConfig struct {
	Enabled           bool `json:"enabled"`
	ToolCharBudget    int  `json:"tool_char_budget"`
	TimeoutSeconds    int  `json:"timeout_seconds"`
	ContextEnabled    bool `json:"context_enabled"`
	SummaryCharBudget int  `json:"summary_char_budget"`
	TailWordBudget    int  `json:"tail_word_budget"`
	TailByteBudget    int  `json:"tail_byte_budget"`
	ContextByteBudget int  `json:"context_byte_budget"`
}

type HindsightConfig struct {
	Endpoint       string `json:"endpoint"`
	Token          string `json:"token"`
	BankPrefix     string `json:"bank_prefix"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MaxTokens      int    `json:"max_tokens"`
	Limit          int    `json:"limit"`
}

func defaultWorkspace() Workspace {
	return Workspace{ID: "default", Name: "Default Super Workspace", Roles: []SuperRole{}, RoutingPolicies: []RoutingPolicyConfig{}}
}
func normalizeRole(role *SuperRole, creating bool) error {
	role.Name = strings.TrimSpace(role.Name)
	if role.Name == "" {
		role.Name = "New Role"
	}
	role.Description, role.Prompt, role.CWD = strings.TrimSpace(role.Description), strings.TrimSpace(role.Prompt), strings.TrimSpace(role.CWD)
	if role.SessionPolicy == "" {
		role.SessionPolicy = "reuse"
	}
	if creating {
		role.ID = newID()
		role.CreatedAt = nowMillis()
	}
	role.UpdatedAt = nowMillis()
	return nil
}
