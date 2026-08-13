package chat

import (
	"context"
	"fmt"
	"log"
	"net/http"
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
	blocks := []string{}
	query, err := p.store.latestUserMessage(turn.ChatID, turn.StartedAt)
	if err != nil {
		return "", 0, 0, err
	}
	if query != nil && strings.TrimSpace(query.Text) != "" {
		blocks = append(blocks, "### User query\n"+strings.TrimSpace(query.Text))
	}
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
	transcript := strings.Join(blocks, "\n\n")
	return transcript, len(blocks), len([]rune(transcript)), nil
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
	llm, err := p.llmConfig(p.ctx)
	if err != nil {
		base.Error = err.Error()
		_ = p.store.saveTurnSummary(base)
		log.Printf("viewer-chat turn summary failed turn_id=%s: %v", turnID, err)
		return
	}
	ctx, cancel := context.WithTimeout(p.ctx, time.Duration(config.TimeoutSeconds)*time.Second)
	started := time.Now()
	result, err := summarizeTranscript(ctx, p.httpClient, llm, transcript)
	cancel()
	base.LatencyMS = int(time.Since(started).Milliseconds())
	if err != nil {
		base.Error = truncateText(err.Error(), 1000)
		_ = p.store.saveTurnSummary(base)
		log.Printf("viewer-chat turn summary failed turn_id=%s: %v", turnID, err)
		return
	}
	base.Status, base.Summary, base.Model, base.ProfileID = "completed", result.Content, result.Model, llm.Model
	_ = p.store.saveTurnSummary(base)
}

func summarizeTranscript(ctx context.Context, client *http.Client, llm LLMConfig, transcript string) (completionResult, error) {
	return chatCompletion(ctx, client, llm, []map[string]string{{"role": "system", "content": turnSummarySystemPrompt}, {"role": "user", "content": fmt.Sprintf(turnSummaryUserTemplate, transcript)}}, false)
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

func (p *Plugin) buildTurnSummariesSection(chatID string, before, after int64, charBudget int, excludeRoleID string) string {
	if charBudget <= 0 {
		return ""
	}
	summaries, err := p.store.recentTurnSummaries(chatID, before, after, excludeRoleID)
	if err != nil || len(summaries) == 0 {
		return ""
	}
	picked, used := []TurnSummary{}, 0
	for i := len(summaries) - 1; i >= 0; i-- {
		length := len([]rune(summaries[i].Summary))
		if used+length > charBudget {
			if len(picked) == 0 {
				item := summaries[i]
				item.Summary = truncateText(item.Summary, charBudget)
				picked = append(picked, item)
			}
			break
		}
		picked = append(picked, summaries[i])
		used += length
	}
	lines := []string{"Summaries of earlier work turns in this chat (most recent last):"}
	for i := len(picked) - 1; i >= 0; i-- {
		item := picked[i]
		label := fallback(item.RoleName, fallback(item.RoleID, "agent"))
		lines = append(lines, fmt.Sprintf("- [%s, role %q]\n%s", formatSummaryTime(item.OccurredAt), label, item.Summary))
	}
	return strings.Join(lines, "\n\n")
}

func renderHistory(messages []Message, heading string) string {
	if len(messages) == 0 {
		return ""
	}
	lines := []string{heading}
	for _, message := range messages {
		sender := "User"
		if message.RoleID != "" {
			sender = fallback(message.RoleName, "Agent")
		}
		lines = append(lines, fmt.Sprintf("%s: %s", sender, message.Text))
	}
	return strings.Join(lines, "\n")
}

func (p *Plugin) buildUnsummarizedTailSection(chatID string, before, after int64, wordBudget int) string {
	if wordBudget <= 0 {
		return ""
	}
	latest, _ := p.store.latestSummaryTime(chatID, before)
	floor := after
	if latest > floor {
		floor = latest
	}
	messages, err := p.store.historyAfter(chatID, floor, before, wordBudget)
	if err != nil {
		return ""
	}
	heading := "Recent activity not yet covered by a summary (raw messages):"
	if latest == 0 && after == 0 {
		heading = "Recent visible chat history before the current message:"
	}
	return renderHistory(messages, heading)
}

func (p *Plugin) buildNewSessionContext(chat Chat, query string, before int64) string {
	config := p.summaryConfig(p.ctx)
	if !config.ContextEnabled {
		return ""
	}
	sections := nonEmpty(p.buildTurnSummariesSection(chat.ID, before, 0, config.SummaryCharBudget, ""), p.buildUnsummarizedTailSection(chat.ID, before, 0, config.TailWordBudget))
	if recall := p.buildHindsightRecallSection(chat.ID, query, lastString(sections), before); recall != "" {
		sections = append(sections, recall)
	}
	return strings.Join(sections, "\n\n")
}

func (p *Plugin) buildRoleSwitchBridge(chat Chat, roleID, query string, before int64) string {
	config := p.summaryConfig(p.ctx)
	if !config.ContextEnabled {
		return ""
	}
	last, _ := p.store.roleLastActivity(chat.ID, roleID, before)
	if last > 0 {
		active, _ := p.store.hasActivityBetween(chat.ID, last, before)
		if !active {
			return ""
		}
	}
	sections := nonEmpty(p.buildTurnSummariesSection(chat.ID, before, last, config.SummaryCharBudget, roleID), p.buildUnsummarizedTailSection(chat.ID, before, last, config.TailWordBudget))
	if recall := p.buildHindsightRecallSection(chat.ID, query, "", before); recall != "" {
		sections = append(sections, recall)
	}
	if len(sections) == 0 {
		return ""
	}
	return "While you were away, other work happened in this chat that your session did not see. Catch up from this context:\n\n" + strings.Join(sections, "\n\n")
}

func lastString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}
