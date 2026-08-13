package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	Version           = 1
	MaxDepth          = 8
	DefaultFrameSize  = 1024 * 1024
	DefaultQueueSize  = 1000
	KernelPluginID    = "kernel"
	DefaultInstanceID = "_"
)

var (
	normalField   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	reservedField = regexp.MustCompile(`^_[a-z0-9_-]*$`)
	uuidV4        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
)

type Origin struct {
	Plugin   string `json:"plugin"`
	Instance string `json:"instance"`
}

type Manifest struct {
	ID      string          `json:"id"`
	Version string          `json:"version"`
	Slots   map[string]any  `json:"slots"`
	Emits   map[string]any  `json:"emits"`
	Raw     json.RawMessage `json:"-"`
}

func (m Manifest) MarshalJSON() ([]byte, error) {
	if len(m.Raw) != 0 {
		return m.Raw, nil
	}
	type wireManifest Manifest
	return json.Marshal(wireManifest(m))
}

type Hello struct {
	Type            string   `json:"type"`
	ProtocolVersion int      `json:"protocol_version"`
	Conn            string   `json:"conn"`
	Manifest        Manifest `json:"manifest"`
	Managed         bool     `json:"managed"`
	InstanceID      *string  `json:"instance_id,omitempty"`
}

type PublishFrame struct {
	Type    string          `json:"type"`
	Channel string          `json:"channel"`
	Value   json.RawMessage `json:"value"`
	TraceID string          `json:"trace_id,omitempty"`
	Depth   int             `json:"depth,omitempty"`
	TS      int64           `json:"ts,omitempty"`
	Origin  *Origin         `json:"origin,omitempty"`
}

type SetFrame struct {
	Type    string          `json:"type"`
	Channel string          `json:"channel"`
	Value   json.RawMessage `json:"value"`
	TraceID string          `json:"trace_id,omitempty"`
	Depth   int             `json:"depth,omitempty"`
	TS      int64           `json:"ts,omitempty"`
	Origin  *Origin         `json:"origin,omitempty"`
}

type SubscribeFrame struct {
	Type    string `json:"type"`
	Pattern string `json:"pattern"`
}

type UnsubscribeFrame struct {
	Type    string `json:"type"`
	Pattern string `json:"pattern"`
}

type ClientFrame struct {
	Type    string
	Channel string
	Pattern string
	Value   json.RawMessage
	TraceID string
	Depth   int
}

type Delivery struct {
	Type    string          `json:"type"`
	Channel string          `json:"channel"`
	Value   json.RawMessage `json:"value"`
	TraceID string          `json:"trace_id,omitempty"`
	Depth   int             `json:"depth"`
	TS      int64           `json:"ts"`
	Origin  Origin          `json:"origin"`
}

type Error struct {
	Code    string
	Message string
	Detail  any
}

func (e *Error) Error() string { return e.Message }

func validField(field string, first bool) bool {
	return normalField.MatchString(field) || field == "_" || first && reservedField.MatchString(field)
}

func ValidateChannel(channel string) error {
	fields := strings.Split(channel, ":")
	if len(fields) < 3 {
		return &Error{Code: "invalid_channel", Message: "channel must contain at least three fields"}
	}
	for i, field := range fields {
		if !validField(field, i == 0) {
			return &Error{Code: "invalid_channel", Message: fmt.Sprintf("invalid channel: %s", channel)}
		}
	}
	return nil
}

func ValidatePattern(pattern string) error {
	if pattern == ">" {
		return nil
	}
	fields := strings.Split(pattern, ":")
	for i, field := range fields {
		if field == "" {
			return &Error{Code: "invalid_pattern", Message: fmt.Sprintf("invalid pattern: %s", pattern)}
		}
		if field == "*" {
			continue
		}
		if strings.ContainsAny(field, "*>") || !validField(field, i == 0) {
			return &Error{Code: "invalid_pattern", Message: fmt.Sprintf("invalid pattern: %s", pattern)}
		}
	}
	return nil
}

func ChannelMatches(pattern, channel string) bool {
	if pattern == ">" {
		return true
	}
	pf := strings.Split(pattern, ":")
	cf := strings.Split(channel, ":")
	if len(pf) > len(cf) {
		return false
	}
	for i := range pf {
		if pf[i] != "*" && pf[i] != cf[i] {
			return false
		}
	}
	return true
}

func ParseHello(data []byte) (Hello, error) {
	object, err := rawObject(data)
	if err != nil {
		return Hello{}, errors.New("hello must be a JSON object")
	}
	required := []string{"type", "protocol_version", "conn", "manifest", "managed"}
	for _, field := range required {
		if _, ok := object[field]; !ok {
			return Hello{}, fmt.Errorf("hello missing required field: %s", field)
		}
	}
	var hello Hello
	if err := json.Unmarshal(data, &hello); err != nil {
		return Hello{}, errors.New("hello fields have invalid types")
	}
	if hello.Type != "hello" {
		return Hello{}, errors.New("frame type must be hello")
	}
	if !uuidV4.MatchString(hello.Conn) {
		return Hello{}, errors.New("conn must be a UUIDv4 string")
	}
	manifestRaw, ok := object["manifest"]
	if !ok {
		return Hello{}, errors.New("manifest must be an object")
	}
	manifestObject, err := rawObject(manifestRaw)
	if err != nil {
		return Hello{}, errors.New("manifest must be an object")
	}
	for _, field := range []string{"id", "version", "slots", "emits"} {
		if _, ok := manifestObject[field]; !ok {
			return Hello{}, fmt.Errorf("manifest missing required field: %s", field)
		}
	}
	if !normalField.MatchString(hello.Manifest.ID) {
		return Hello{}, errors.New("manifest.id must be a valid non-reserved channel field")
	}
	if hello.Manifest.Version == "" {
		return Hello{}, errors.New("manifest.version must be a non-empty string")
	}
	if hello.Manifest.Slots == nil || hello.Manifest.Emits == nil {
		return Hello{}, errors.New("manifest.slots and manifest.emits must be objects")
	}
	hello.Manifest.Raw = append(json.RawMessage(nil), manifestRaw...)
	if hello.InstanceID != nil && !validField(*hello.InstanceID, false) {
		return Hello{}, errors.New("instance_id must be a valid channel field")
	}
	return hello, nil
}

func ParseClientFrame(data []byte) (ClientFrame, error) {
	object, err := rawObject(data)
	if err != nil {
		return ClientFrame{}, &Error{Code: "malformed_frame", Message: "frame must be a JSON object"}
	}
	typeRaw, ok := object["type"]
	if !ok {
		return ClientFrame{}, &Error{Code: "malformed_frame", Message: "frame.type must be a string"}
	}
	var frameType string
	if err := json.Unmarshal(typeRaw, &frameType); err != nil {
		return ClientFrame{}, &Error{Code: "malformed_frame", Message: "frame.type must be a string"}
	}
	if frameType == "hello" {
		return ClientFrame{}, &Error{Code: "unexpected_hello", Message: "hello may only be sent as the first frame"}
	}
	if frameType != "publish" && frameType != "set" && frameType != "subscribe" && frameType != "unsubscribe" {
		return ClientFrame{}, &Error{Code: "unknown_type", Message: fmt.Sprintf("unknown frame type: %s", frameType)}
	}
	frame := ClientFrame{Type: frameType}
	if frameType == "subscribe" || frameType == "unsubscribe" {
		if err := decodeStringField(object, "pattern", &frame.Pattern); err != nil {
			return ClientFrame{}, &Error{Code: "invalid_pattern", Message: "pattern must be a string"}
		}
		if err := ValidatePattern(frame.Pattern); err != nil {
			return ClientFrame{}, err
		}
		return frame, nil
	}
	if err := decodeStringField(object, "channel", &frame.Channel); err != nil {
		return ClientFrame{}, &Error{Code: "invalid_channel", Message: "channel must be a string"}
	}
	if err := ValidateChannel(frame.Channel); err != nil {
		return ClientFrame{}, err
	}
	value, ok := object["value"]
	if !ok {
		return ClientFrame{}, &Error{Code: "malformed_frame", Message: fmt.Sprintf("%s frame missing value", frameType)}
	}
	frame.Value = append(json.RawMessage(nil), value...)
	if traceRaw, ok := object["trace_id"]; ok {
		if err := json.Unmarshal(traceRaw, &frame.TraceID); err != nil || frame.TraceID == "" {
			return ClientFrame{}, &Error{Code: "malformed_frame", Message: "trace_id must be a non-empty string"}
		}
	}
	if depthRaw, ok := object["depth"]; ok {
		decoder := json.NewDecoder(bytes.NewReader(depthRaw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return ClientFrame{}, &Error{Code: "malformed_frame", Message: "depth must be a non-negative integer"}
		}
		number, ok := value.(json.Number)
		if !ok {
			return ClientFrame{}, &Error{Code: "malformed_frame", Message: "depth must be a non-negative integer"}
		}
		depth, err := number.Int64()
		if err != nil || depth < 0 || int64(int(depth)) != depth {
			return ClientFrame{}, &Error{Code: "malformed_frame", Message: "depth must be a non-negative integer"}
		}
		frame.Depth = int(depth)
	}
	return frame, nil
}

func rawObject(data []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, errors.New("not an object")
	}
	return object, nil
}

func decodeStringField(object map[string]json.RawMessage, name string, target *string) error {
	raw, ok := object[name]
	if !ok {
		return errors.New("missing")
	}
	return json.Unmarshal(raw, target)
}
