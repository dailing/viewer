package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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

func routeWithLLM(ctx context.Context, client *http.Client, config LLMConfig, message string, roles []SuperRole, history string) ([]string, string, error) {
	if strings.TrimSpace(config.Endpoint) == "" || strings.TrimSpace(config.Model) == "" {
		return nil, "", fmt.Errorf("LLM router is not configured: set plugins.viewer-chat.llm.endpoint and model")
	}
	body := map[string]any{"model": config.Model, "response_format": map[string]string{"type": "json_object"}, "messages": []map[string]string{{"role": "system", "content": "Follow the routing prompt exactly. Return only a JSON object with role_ids and rationale."}, {"role": "user", "content": renderDispatchPrompt(message, roles, history)}}}
	data, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.Endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("Content-Type", "application/json")
	if config.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+config.APIKey)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("dispatch model failed: %w", err)
	}
	defer response.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", fmt.Errorf("dispatch model returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(limited)))
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(limited, &envelope) != nil || len(envelope.Choices) == 0 {
		return nil, "", fmt.Errorf("dispatch model returned a malformed completion")
	}
	return parseRoute(envelope.Choices[0].Message.Content, roles)
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

func defaultHTTPClient() *http.Client { return &http.Client{Timeout: 30 * time.Second} }
