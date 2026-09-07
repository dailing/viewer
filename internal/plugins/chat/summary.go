package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

const turnSummarySystemPrompt = "You condense one agent work turn from a multi-role chat into a compact briefing for other agents who did not see the turn. Be factual: never invent constraints, decisions, file names or numbers that are not present in the transcript. Keep names of files, scripts, commands and important numbers verbatim. Write in the same language as the transcript (Chinese if the transcript is Chinese). Keep the whole summary under 800 characters."

const turnSummaryUserTemplate = `Below is the transcript of one agent turn (user query, assistant messages, tool calls, file changes; long tool outputs are truncated).

Write a summary with exactly these four sections:
## 任务
(what the user asked for in this turn, 1-2 sentences)
## 关键动作与改动
(what the agent did: files/scripts/commands/decisions, with concrete names; bullet points)
## 结果
(outcome: what works now, verification results, artifacts)
## 未决事项
(open questions / next steps; write "无" if none)

TRANSCRIPT:
%s
`

func truncateText(value string, budget int) string {
	text := strings.TrimSpace(value)
	if budget <= 0 {
		return ""
	}
	if len([]rune(text)) <= budget {
		return text
	}
	runes := []rune(text)
	return fmt.Sprintf("%s\n… [truncated, %d chars omitted]", string(runes[:budget]), len(runes)-budget)
}

func (p *Plugin) buildTurnTranscript(turnID string, toolCharBudget int) (string, int, int, error) {
	turn, err := p.store.turn(turnID)
	if err != nil || turn == nil {
		return "", 0, 0, err
	}
	messages, err := p.store.turnMessages(turnID)
	if err != nil {
		return "", 0, 0, err
	}
	messageBlocks, err := p.store.turnMessageBlocks(turnID)
	if err != nil {
		return "", 0, 0, err
	}
	blocks := []string{}
	query, err := p.store.latestUserMessage(turn.ChatID, turn.StartedAt)
	if err != nil {
		return "", 0, 0, err
	}
	if query != nil && strings.TrimSpace(query.Text) != "" {
		blocks = append(blocks, "### User query\n"+strings.TrimSpace(query.Text))
	}
	if len(messageBlocks) > 0 {
		for _, block := range messageBlocks {
			if line := transcriptBlockLine(block); line != "" {
				blocks = append(blocks, line)
			}
		}
	} else {
		// Pre-M7 turns have no parsed blocks; preserve their visible-message
		// transcript as a non-destructive migration fallback.
		for _, message := range messages {
			text := strings.TrimSpace(message.Text)
			if text == "" {
				continue
			}
			label := "Assistant"
			if message.Role == "user" {
				label = "User query"
			}
			blocks = append(blocks, "### "+label+"\n"+text)
		}
	}
	transcript := strings.Join(blocks, "\n\n")
	return transcript, len(blocks), len([]rune(transcript)), nil
}

func transcriptBlockLine(block MessageBlock) string {
	payload := map[string]any{}
	_ = json.Unmarshal([]byte(block.Payload), &payload)
	switch block.Kind {
	case "agent_text":
		if text := strings.TrimSpace(block.Text); text != "" {
			return "### Assistant\n" + text
		}
		return ""
	case "tool_call":
		return fmt.Sprintf("[tool: %s]", strings.TrimSpace(fallback(stringify(payload["name"]), "unknown")+" "+stringify(payload["status"])))
	case "file_change":
		return fmt.Sprintf("[file: %s]", fallback(stringify(payload["path"]), "unknown"))
	case "command":
		return fmt.Sprintf("[cmd: %s → %s]", fallback(stringify(payload["command"]), "unknown"), fallback(stringify(payload["status"]), "unknown"))
	default:
		return ""
	}
}

func stringify(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case nil:
		return ""
	default:
		encoded, err := json.Marshal(value)
		if err == nil {
			return string(encoded)
		}
		return fmt.Sprint(value)
	}
}

func (p *Plugin) generateTurnSummary(turnID, provider string) {
	config := p.summaryConfig(p.ctx)
	if !config.Enabled {
		return
	}
	turn, err := p.store.turn(turnID)
	if err != nil || turn == nil {
		return
	}
	transcript, count, chars, err := p.buildTurnTranscript(turnID, config.ToolCharBudget)
	if err != nil || transcript == "" || !strings.Contains(transcript, "### Assistant") {
		return
	}
	base := &TurnSummary{TurnID: turnID, ChatID: turn.ChatID, RoleID: turn.RoleID, RoleName: turn.RoleName, Provider: provider, Status: "failed", SourceMessageCount: count, SourceCharCount: chars, OccurredAt: derefMillis(turn.EndedAt, nowMillis()), CreatedAt: nowMillis()}
	ctx, cancel := context.WithTimeout(p.ctx, time.Duration(config.TimeoutSeconds)*time.Second)
	started := time.Now()
	result, err := summarizeTranscript(ctx, p.llmFn, transcript, config.TimeoutSeconds)
	cancel()
	base.LatencyMS = int(time.Since(started).Milliseconds())
	if err != nil {
		base.Error = truncateText(err.Error(), 1000)
		_ = p.store.saveTurnSummary(base)
		log.Printf("viewer-chat turn summary failed turn_id=%s: %v", turnID, err)
		return
	}
	base.Status, base.Summary, base.Model, base.ProfileID = "completed", result.Content, result.Model, result.Model
	_ = p.store.saveTurnSummary(base)
}

func summarizeTranscript(ctx context.Context, complete llmCompleter, transcript string, timeoutSeconds int) (completionResult, error) {
	return complete(ctx, []map[string]string{{"role": "system", "content": turnSummarySystemPrompt}, {"role": "user", "content": fmt.Sprintf(turnSummaryUserTemplate, transcript)}}, false, timeoutSeconds)
}

func derefMillis(value *int64, fallback int64) int64 {
	if value != nil {
		return *value
	}
	return fallback
}

func formatSummaryTime(value int64) string {
	return time.UnixMilli(value).Local().Format("01-02 15:04")
}

func truncateUTF8Prefix(value string, byteBudget int) string {
	if byteBudget <= 0 {
		return ""
	}
	if len(value) <= byteBudget {
		return value
	}
	end := byteBudget
	for end > 0 && (value[end]&0xc0) == 0x80 {
		end--
	}
	return strings.TrimSpace(value[:end])
}

func truncateUTF8Suffix(value string, byteBudget int) string {
	if byteBudget <= 0 {
		return ""
	}
	if len(value) <= byteBudget {
		return value
	}
	start := len(value) - byteBudget
	for start < len(value) && (value[start]&0xc0) == 0x80 {
		start++
	}
	return strings.TrimSpace(value[start:])
}

func renderRecentHistory(messages []Message, heading string, byteBudget int) string {
	if len(messages) == 0 || byteBudget <= len(heading)+1 {
		return ""
	}
	remaining := byteBudget - len(heading) - 1
	newestFirst := make([]string, 0, len(messages))
	for index := len(messages) - 1; index >= 0 && remaining > 0; index-- {
		message := messages[index]
		sender := "User"
		if message.RoleID != "" {
			sender = fallback(message.RoleName, "Agent")
		}
		line := fmt.Sprintf("%s: %s", sender, message.Text)
		if len(line) > remaining {
			line = truncateUTF8Prefix(line, remaining)
		}
		if line == "" {
			break
		}
		newestFirst = append(newestFirst, line)
		remaining -= len(line) + 1
	}
	lines := []string{heading}
	for index := len(newestFirst) - 1; index >= 0; index-- {
		lines = append(lines, newestFirst[index])
	}
	return strings.Join(lines, "\n")
}

func capRecentContext(value string, byteBudget int) string {
	if byteBudget <= 0 || value == "" {
		return ""
	}
	if len(value) <= byteBudget {
		return value
	}
	const marker = "Older context omitted to fit the configured byte budget.\n"
	if byteBudget <= len(marker) {
		return truncateUTF8Prefix(marker, byteBudget)
	}
	return marker + truncateUTF8Suffix(value, byteBudget-len(marker))
}

func lastString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}
