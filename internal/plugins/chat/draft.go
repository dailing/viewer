package chat

import (
	"errors"
	"log/slog"

	"viewer/sdk/go/busclient"
)

// Drafts belong to a fixed InputInstance and line. Two views of the same
// Chat/Branch therefore keep independent unsent text while their canonical
// messages remain shared Chat/Branch history.
const maxDraftBytes = 64 * 1024

func (p *Plugin) draftLine(chatID, branchID string) error {
	chat, err := p.store.chat(chatID)
	if err != nil {
		return err
	}
	if chat == nil {
		return errors.New("chat not found")
	}
	if branchID != "" {
		_, err = p.resolveBranch(*chat, branchID)
	}
	return err
}

func draftIDs(value map[string]any) (chatID, inputInstanceID, branchID string, ok bool) {
	chatID, _ = value["chat_id"].(string)
	inputInstanceID, _ = value["input_instance_id"].(string)
	branchID, _ = value["branch_id"].(string)
	return chatID, inputInstanceID, branchID, chatID != "" && inputInstanceID != "" && len(inputInstanceID) <= 64
}

func (p *Plugin) handleDraftGet(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, inputInstanceID, branchID, ok := draftIDs(value)
	if err != nil || !ok {
		p.reply(frame, nil, errBadRequest)
		return
	}
	if err := p.draftLine(chatID, branchID); err != nil {
		p.reply(frame, nil, err)
		return
	}
	draft, err := p.store.draft(inputInstanceID, branchID)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	text, updatedAt := "", int64(0)
	if draft != nil {
		text, updatedAt = draft.Text, draft.UpdatedAt
	}
	p.reply(frame, map[string]any{
		"chat_id": chatID, "input_instance_id": inputInstanceID, "branch_id": branchID,
		"text": text, "updated_at": updatedAt,
	}, nil)
}

func (p *Plugin) handleDraftSet(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, inputInstanceID, branchID, ok := draftIDs(value)
	text, _ := value["text"].(string)
	source, _ := value["source"].(string)
	if err != nil || !ok {
		p.reply(frame, nil, errBadRequest)
		return
	}
	if len(text) > maxDraftBytes {
		p.reply(frame, nil, errors.New("draft exceeds 64 KiB"))
		return
	}
	if err := p.draftLine(chatID, branchID); err != nil {
		p.reply(frame, nil, err)
		return
	}
	draft := &Draft{ChatID: chatID, InputInstanceID: inputInstanceID, BranchID: branchID, Text: text, UpdatedAt: nowMillis()}
	if err := p.store.saveDraft(draft); err != nil {
		p.reply(frame, nil, err)
		return
	}
	payload := map[string]any{
		"chat_id": chatID, "input_instance_id": inputInstanceID, "branch_id": branchID,
		"text": text, "updated_at": draft.UpdatedAt, "source": source,
	}
	// NB: the event channel must not be a prefix of the RPC channels — bus
	// subscriptions match by field prefix, so "chat:_:draft" would also
	// deliver draft:get/set RPC frames to event subscribers.
	p.publish("chat:_:draft:sync", payload)
	p.reply(frame, payload, nil)
}

// clearDraft clears only the input that actually dispatched. Automatic (loop)
// dispatches have no InputInstance and therefore never clear a human draft.
func (p *Plugin) clearDraft(chatID, inputInstanceID, branchID string) {
	if inputInstanceID == "" {
		return
	}
	cleared, err := p.store.clearDraft(inputInstanceID, branchID)
	if err != nil {
		slog.Warn("chat draft clear failed", "chat_id", chatID, "input_instance_id", inputInstanceID, "branch_id", branchID, "error", err)
		return
	}
	if cleared {
		p.publish("chat:_:draft:sync", map[string]any{
			"chat_id": chatID, "input_instance_id": inputInstanceID, "branch_id": branchID,
			"text": "", "updated_at": nowMillis(), "source": "",
		})
	}
}
