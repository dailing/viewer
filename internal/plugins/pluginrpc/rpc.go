// Package pluginrpc contains the small inbox-RPC helpers shared by core plugins.
package pluginrpc

import (
	"context"

	"viewer/sdk/go/busclient"
)

// Object returns the JSON object carried by a frame.
func Object(frame busclient.Frame) (map[string]any, bool) {
	value, ok := frame.Value.(map[string]any)
	return value, ok
}

// Cancelled reports whether a frame is the best-effort cancellation form.
func Cancelled(frame busclient.Frame) bool {
	value, ok := Object(frame)
	return ok && value["_cancel"] == true
}

// Respond sends an inbox-convention success response. Non-RPC frames are ignored.
func Respond(client *busclient.Client, frame busclient.Frame, result any) error {
	return respond(client, frame, map[string]any{"ok": true, "result": result})
}

// RespondError sends an inbox-convention error response. Non-RPC frames are ignored.
func RespondError(client *busclient.Client, frame busclient.Frame, code, message string) error {
	return respond(client, frame, map[string]any{
		"ok":    false,
		"error": map[string]any{"code": code, "message": message},
	})
}

func respond(client *busclient.Client, frame busclient.Frame, response map[string]any) error {
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
