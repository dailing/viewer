package chat

import (
	"errors"
	"log/slog"

	"viewer/sdk/go/busclient"
)

// Composer drafts sync across devices through the bus: the server row is the
// single source of truth, every set overwrites it whole (last write wins by
// arrival order) and broadcasts the new state so other open panes follow.
const maxDraftBytes = 64 * 1024

func (p *Plugin) handleDraftGet(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
	if err != nil || chatID == "" {
		p.reply(frame, nil, errBadRequest)
		return
	}
	draft, err := p.store.draft(chatID)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	text, updatedAt := "", int64(0)
	if draft != nil {
		text, updatedAt = draft.Text, draft.UpdatedAt
	}
	p.reply(frame, map[string]any{"chat_id": chatID, "text": text, "updated_at": updatedAt}, nil)
}

func (p *Plugin) handleDraftSet(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, _ := value["chat_id"].(string)
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
	draft := &Draft{ChatID: chatID, Text: text, UpdatedAt: nowMillis()}
	if err := p.store.saveDraft(draft); err != nil {
		p.reply(frame, nil, err)
		return
	}
	payload := map[string]any{"chat_id": chatID, "text": text, "updated_at": draft.UpdatedAt, "source": source}
	// NB: the event channel must not be a prefix of the RPC channels — bus
	// subscriptions match by field prefix, so "chat:_:draft" would also
	// deliver draft:get/set RPC frames to event subscribers.
	p.publish("chat:_:draft:sync", payload)
	p.reply(frame, payload, nil)
}

// clearDraft removes a chat's synced draft once a human message from that chat
// landed in the timeline, and broadcasts the empty state so every open input
// box clears — not just the sender's. Automatic (loop) dispatches keep it.
func (p *Plugin) clearDraft(chatID string) {
	cleared, err := p.store.clearDraft(chatID)
	if err != nil {
		slog.Warn("chat draft clear failed", "chat_id", chatID, "error", err)
		return
	}
	if cleared {
		p.publish("chat:_:draft:sync", map[string]any{"chat_id": chatID, "text": "", "updated_at": nowMillis(), "source": ""})
	}
}
