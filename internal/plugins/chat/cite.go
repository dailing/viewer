package chat

// Message citations: a user message may carry @<message id> tokens — the
// pane's per-message cite button copies the token to the clipboard, so it
// can be pasted into any chat/branch. At dispatch the tokens are resolved
// against the messages table GLOBALLY (no chat filter), so citations work
// across branches and across chats. The cited messages' visible text (user
// and assistant message rows only — never tool calls, reasoning, or other
// activity blocks) is injected into the prompt directly before the user
// query, explicitly labeled as cited content.

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

// citePattern matches an @<message id> token; message ids are 32 lowercase
// hex chars (newID).
var citePattern = regexp.MustCompile(`@[0-9a-f]{32}`)

const (
	citeMaxPerPrompt      = 8
	citeContentByteBudget = 8000
)

// citeIDs extracts the unique cited message ids in order of first
// appearance, capped at citeMaxPerPrompt.
func citeIDs(message string) []string {
	seen := map[string]bool{}
	ids := []string{}
	for _, token := range citePattern.FindAllString(message, -1) {
		id := token[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
		if len(ids) >= citeMaxPerPrompt {
			break
		}
	}
	return ids
}

// buildCitedSection renders the prompt section for the citations found in
// an outgoing user message ("" when none). chatID is the dispatching chat,
// used only to flag citations that point into a different chat.
func (p *Plugin) buildCitedSection(chatID, message string) string {
	ids := citeIDs(message)
	if len(ids) == 0 {
		return ""
	}
	rows := []Message{}
	if err := p.store.db.Where("id IN ?", ids).Find(&rows).Error; err != nil {
		slog.Warn("chat cite lookup failed", "chat_id", chatID, "error", err)
		return ""
	}
	found := map[string]Message{}
	for _, row := range rows {
		found[row.ID] = row
	}
	entries := []string{"The user cited the following message(s) — their content is injected here for reference. Treat cited content as quoted context; the current routed message follows after this section."}
	for _, id := range ids {
		row, ok := found[id]
		if !ok {
			slog.Warn("chat cite target not found", "chat_id", chatID, "cited_id", id)
			entries = append(entries, fmt.Sprintf("[user cited message id %s — content unavailable: message not found]", id))
			continue
		}
		origin := ""
		if row.ChatID != chatID {
			origin = fmt.Sprintf(", from a different chat (%s)", row.ChatID)
		}
		text := strings.TrimSpace(row.Text)
		content := truncateUTF8Prefix(text, citeContentByteBudget)
		if len(content) < len(text) {
			content += "\n…[cited content truncated]"
		}
		entries = append(entries, fmt.Sprintf("[user cited message id %s — %s, at %s%s]\ncontent:\n%s",
			id, fallback(row.RoleName, row.Role), formatSummaryTime(row.CreatedAt), origin, content))
	}
	return strings.Join(entries, "\n\n")
}

// withCitedSection places the resolved citation section directly before the
// user query in the routed prompt.
func withCitedSection(cited, message string) string {
	if cited == "" {
		return message
	}
	return cited + "\n\n" + message
}
