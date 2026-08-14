// Package agentdriver defines the provider-neutral bus contract shared by chat
// and headless agent plugins.
package agentdriver

const (
	KindAgentText  = "agent_text"
	KindThinking   = "thinking"
	KindToolCall   = "tool_call"
	KindToolResult = "tool_result"
	KindFileChange = "file_change"
	KindCommand    = "command"
	KindOther      = "other"
)

type Target struct {
	Agent      string         `json:"agent"`
	Provider   string         `json:"provider"`
	Model      string         `json:"model"`
	Parameters map[string]any `json:"parameters"`
}

type Block struct {
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	Payload string `json:"payload"`
}

type EventFrame struct {
	SessionID string `json:"session_id"`
	Seq       int    `json:"seq"`
	Kind      string `json:"kind"`
	RawJSON   string `json:"raw_json"`
	Block     Block  `json:"block"`
}

type ProviderCatalog struct {
	Provider        string         `json:"provider"`
	Models          []string       `json:"models"`
	ParameterSchema map[string]any `json:"parameter_schema,omitempty"`
}

type Catalog struct {
	Agent     string            `json:"agent"`
	Providers []ProviderCatalog `json:"providers"`
}
