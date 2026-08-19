// Package pluginrpc contains the small inbox-RPC helpers shared by core plugins.
// The implementation lives in the bus SDK (busclient.Object/Respond/...) so
// external Go plugins use the identical semantics.
package pluginrpc

import (
	"viewer/sdk/go/busclient"
)

// Object returns the JSON object carried by a frame.
func Object(frame busclient.Frame) (map[string]any, bool) {
	return busclient.Object(frame)
}

// Cancelled reports whether a frame is the best-effort cancellation form.
func Cancelled(frame busclient.Frame) bool {
	return busclient.Cancelled(frame)
}

// Respond sends an inbox-convention success response. Non-RPC frames are ignored.
func Respond(client *busclient.Client, frame busclient.Frame, result any) error {
	return busclient.Respond(client, frame, result)
}

// RespondError sends an inbox-convention error response. Non-RPC frames are ignored.
func RespondError(client *busclient.Client, frame busclient.Frame, code, message string) error {
	return busclient.RespondError(client, frame, code, message)
}
