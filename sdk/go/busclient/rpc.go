package busclient

import "context"

// Inbox-RPC helpers (framework section 5.3): a request is an ordinary
// publish whose payload carries _reply_to + _corr; the responder publishes
// the response to _reply_to echoing _corr. These helpers mirror
// internal/plugins/pluginrpc so external Go plugins get the same semantics
// without importing viewer internals.

// Object returns the JSON object carried by a frame.
func Object(frame Frame) (map[string]any, bool) {
	value, ok := frame.Value.(map[string]any)
	return value, ok
}

// Cancelled reports whether a frame is the best-effort cancellation form.
func Cancelled(frame Frame) bool {
	value, ok := Object(frame)
	return ok && value["_cancel"] == true
}

// Respond sends an inbox-convention success response. Non-RPC frames are ignored.
func Respond(client *Client, frame Frame, result any) error {
	return respond(client, frame, map[string]any{"ok": true, "result": result})
}

// RespondError sends an inbox-convention error response. Non-RPC frames are ignored.
func RespondError(client *Client, frame Frame, code, message string) error {
	return respond(client, frame, map[string]any{
		"ok":    false,
		"error": map[string]any{"code": code, "message": message},
	})
}

func respond(client *Client, frame Frame, response map[string]any) error {
	value, ok := Object(frame)
	if !ok {
		return nil
	}
	replyTo, replyOK := value["_reply_to"].(string)
	corr, corrOK := value["_corr"].(string)
	if !replyOK || replyTo == "" || !corrOK || corr == "" {
		return nil
	}
	response["_corr"] = corr
	return client.Publish(context.Background(), replyTo, response)
}
