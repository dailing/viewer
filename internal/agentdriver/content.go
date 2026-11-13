package agentdriver

import (
	"encoding/json"
	"fmt"
)

// StringField returns the first string value among keys in a decoded object.
func StringField(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := object[key].(string); ok {
			return text
		}
	}
	return ""
}

// Stringify preserves strings and represents structured provider payloads as
// JSON, falling back to Go's printable form for non-marshallable values.
func Stringify(value any) string {
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

// SelectedPayload copies the requested non-empty provider payload fields.
func SelectedPayload(object map[string]any, keys ...string) map[string]any {
	payload := map[string]any{}
	for _, key := range keys {
		if value, ok := object[key]; ok && value != nil && Stringify(value) != "" {
			payload[key] = value
		}
	}
	return payload
}
