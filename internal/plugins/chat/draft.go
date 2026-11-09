package chat

import (
	"errors"
	"log/slog"

	"viewer/sdk/go/busclient"
)

// Drafts belong to a fixed InputInstance and line. Two views of the same
// Chat/Task therefore keep independent unsent text while their canonical
// messages remain shared Chat/Task history.
const maxDraftBytes = 64 * 1024

func (p *Plugin) draftLine(chatID, taskID string) error {
	chat, err := p.store.chat(chatID)
	if err != nil {
		return err
	}
	if chat == nil {
		return errors.New("chat not found")
	}
	if taskID != "" {
		_, err = p.resolveTaskOwnership(*chat, taskID)
	}
	return err
}

func draftIDs(value map[string]any) (chatID, inputInstanceID, taskID string, ok bool) {
	chatID, _ = value["chat_id"].(string)
	inputInstanceID, _ = value["input_instance_id"].(string)
	taskID, _ = value["task_id"].(string)
	return chatID, inputInstanceID, taskID, chatID != "" && inputInstanceID != "" && len(inputInstanceID) <= 64
}

func (p *Plugin) handleDraftGet(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, inputInstanceID, taskID, ok := draftIDs(value)
	if err != nil || !ok {
		p.reply(frame, nil, errBadRequest)
		return
	}
	if err := p.draftLine(chatID, taskID); err != nil {
		p.reply(frame, nil, err)
		return
	}
	draft, err := p.store.draft(inputInstanceID, taskID)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	text, updatedAt := "", int64(0)
	if draft != nil {
		text, updatedAt = draft.Text, draft.UpdatedAt
	}
	p.reply(frame, map[string]any{
		"chat_id": chatID, "input_instance_id": inputInstanceID, "task_id": taskID,
		"text": text, "updated_at": updatedAt,
	}, nil)
}

func (p *Plugin) handleDraftSet(frame busclient.Frame) {
	value, err := frameObject(frame)
	chatID, inputInstanceID, taskID, ok := draftIDs(value)
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
	if err := p.draftLine(chatID, taskID); err != nil {
		p.reply(frame, nil, err)
		return
	}
	draft := &Draft{ChatID: chatID, InputInstanceID: inputInstanceID, TaskID: taskID, Text: text, UpdatedAt: nowMillis()}
	if err := p.store.saveDraft(draft); err != nil {
		p.reply(frame, nil, err)
		return
	}
	payload := map[string]any{
		"chat_id": chatID, "input_instance_id": inputInstanceID, "task_id": taskID,
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
func (p *Plugin) clearDraft(chatID, inputInstanceID, taskID string) {
	if inputInstanceID == "" {
		return
	}
	cleared, err := p.store.clearDraft(inputInstanceID, taskID)
	if err != nil {
		slog.Warn("chat draft clear failed", "chat_id", chatID, "input_instance_id", inputInstanceID, "task_id", taskID, "error", err)
		return
	}
	if cleared {
		p.publish("chat:_:draft:sync", map[string]any{
			"chat_id": chatID, "input_instance_id": inputInstanceID, "task_id": taskID,
			"text": "", "updated_at": nowMillis(), "source": "",
		})
	}
}
