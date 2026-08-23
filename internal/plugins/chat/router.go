package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	defaultHistoryWordBudget = 1200
	routerHistoryByteBudget  = 12 * 1024
)
const dispatchTemplate = `You route one user message to persistent agent roles.

Default to exactly one role.
Choose multiple roles only when the user explicitly asks for multiple independent tasks, or when no single role can reasonably complete the request.

Use recent visible chat history only to understand context, not as a separate task.
Return only JSON:
{"role_ids":["role-id"],"rationale":"short reason"}

Available roles:
{{roles_json}}

Recent visible chat history:
{{history}}

Current message:
{{message}}`

func renderDispatchPrompt(message string, roles []SuperRole, history string) string {
	payload := make([]map[string]string, 0, len(roles))
	for _, role := range roles {
		payload = append(payload, map[string]string{"id": role.ID, "name": role.Name, "description": role.Description, "cwd": role.CWD})
	}
	encoded, _ := json.MarshalIndent(payload, "", "  ")
	result := strings.ReplaceAll(dispatchTemplate, "{{roles_json}}", string(encoded))
	result = strings.ReplaceAll(result, "{{history}}", history)
	return strings.ReplaceAll(result, "{{message}}", message)
}

func routeWithLLM(ctx context.Context, complete llmCompleter, message string, roles []SuperRole, history string) ([]string, string, error) {
	messages := []map[string]string{{"role": "system", "content": "Follow the routing prompt exactly. Return only a JSON object with role_ids and rationale."}, {"role": "user", "content": renderDispatchPrompt(message, roles, history)}}
	result, err := complete(ctx, messages, true, 0)
	if err != nil {
		if llmNotConfigured(err) {
			return nil, "", errors.New("LLM router is not configured: set endpoint and model in the LLM pane")
		}
		return nil, "", fmt.Errorf("dispatch model failed: %w", err)
	}
	return parseRoute(result.Content, roles)
}

func parseRoute(content string, roles []SuperRole) ([]string, string, error) {
	trimmed := strings.TrimSpace(content)
	start, end := strings.Index(trimmed, "{"), strings.LastIndex(trimmed, "}")
	var raw struct {
		RoleIDs   any    `json:"role_ids"`
		Rationale string `json:"rationale"`
	}
	if start >= 0 && end >= start && json.Unmarshal([]byte(trimmed[start:end+1]), &raw) == nil {
		valid := map[string]bool{}
		for _, role := range roles {
			valid[role.ID] = true
		}
		selected := []string{}
		appendID := func(id string) {
			if valid[id] {
				for _, prior := range selected {
					if prior == id {
						return
					}
				}
				selected = append(selected, id)
			}
		}
		switch value := raw.RoleIDs.(type) {
		case string:
			appendID(value)
		case []any:
			for _, item := range value {
				appendID(fmt.Sprint(item))
			}
		}
		if len(selected) > 0 {
			return selected, raw.Rationale, nil
		}
	}
	if len(roles) > 0 {
		return []string{roles[0].ID}, "Router output was invalid; fell back to the first eligible role.", nil
	}
	return nil, "", fmt.Errorf("no eligible roles")
}

// No client-level Timeout: every caller bounds its own context (router:
// llm.timeout_seconds; summary: turn_summary.timeout_seconds; hindsight:
// hindsight.timeout_seconds). A shared cap used to silently truncate the
// summary's configured 60s budget to 30s under local-server queueing.
func defaultHTTPClient() *http.Client { return &http.Client{} }
