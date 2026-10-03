package chat

import (
	"errors"
	"log/slog"

	"viewer/sdk/go/busclient"
)

// Composer drafts sync across devices through the bus: the server row is the
// single source of truth, every set overwrites it whole (last write wins by
// arrival order) and broadcasts the new state so other open panes follow.
// Drafts are scoped per chat line (branch_id, "" = mainline): the composer
// binds to the line the next send would land on, so panes viewing different
// branches of one chat hold independent drafts.
const maxDraftBytes = 64 * 1024

func (p *Plugin) handleDraftGet(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	branchID, _ := value["branch_id"].(string)
	if err != nil || chatID == "" {
		p.reply(frame, nil, errBadRequest)
		return
	}
	draft, err := p.store.draft(chatID, branchID)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	text, updatedAt := "", int64(0)
	if draft != nil {
		text, updatedAt = draft.Text, draft.UpdatedAt
	}
	p.reply(frame, map[string]any{"chat_id": chatID, "branch_id": branchID, "text": text, "updated_at": updatedAt}, nil)
}

func (p *Plugin) handleDraftSet(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	branchID, _ := value["branch_id"].(string)
	text, _ := value["text"].(string)
	source, _ := value["source"].(string)
	if err != nil || chatID == "" {
		p.reply(frame, nil, errBadRequest)
		return
	}
	if len(text) > maxDraftBytes {
		p.reply(frame, nil, errors.New("draft exceeds 64 KiB"))
		return
	}
	chat, err := p.store.chat(chatID)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	if chat == nil {
		p.reply(frame, nil, errors.New("chat not found"))
		return
	}
	draft := &Draft{ChatID: chatID, BranchID: branchID, Text: text, UpdatedAt: nowMillis()}
	if err := p.store.saveDraft(draft); err != nil {
		p.reply(frame, nil, err)
		return
	}
	payload := map[string]any{"chat_id": chatID, "branch_id": branchID, "text": text, "updated_at": draft.UpdatedAt, "source": source}
	// NB: the event channel must not be a prefix of the RPC channels — bus
	// subscriptions match by field prefix, so "chat:_:draft" would also
	// deliver draft:get/set RPC frames to event subscribers.
	p.publish("chat:_:draft:sync", payload)
	p.reply(frame, payload, nil)
}

// clearDraft removes a line's synced draft once a human message lands on that
// line, and broadcasts the empty state so every open input box bound to the
// line clears — not just the sender's. Automatic (loop) dispatches keep it.
func (p *Plugin) clearDraft(chatID, branchID string) {
	cleared, err := p.store.clearDraft(chatID, branchID)
	if err != nil {
		slog.Warn("chat draft clear failed", "chat_id", chatID, "branch_id", branchID, "error", err)
		return
	}
	if cleared {
		p.publish("chat:_:draft:sync", map[string]any{"chat_id": chatID, "branch_id": branchID, "text": "", "updated_at": nowMillis(), "source": ""})
	}
}
