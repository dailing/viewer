package protocol

import (
	"encoding/json"
	"testing"
)

func TestChannelMatches(t *testing.T) {
	tests := []struct {
		pattern string
		channel string
		want    bool
	}{
		{"chat:42:status", "chat:42:status", true},
		{"*:42:status", "chat:42:status", true},
		{"chat:*:status", "chat:42:status", true},
		{"chat:42:*", "chat:42:status", true},
		{"*:42:*", "chat:42:status", true},
		{"chat:42", "chat:42:message", true},
		{"chat", "chat:42:message:detail", true},
		{"chat:42:message:detail", "chat:42:message", false},
		{">", "_inbox:c1:r1", true},
		{"chat:*:status", "chat:42:message", false},
		{"empty:*:tail", "empty:x:tail", true},
		{"plugins:_", "plugins:_:list", true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.channel, func(t *testing.T) {
			if got := ChannelMatches(tt.pattern, tt.channel); got != tt.want {
				t.Fatalf("ChannelMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidationTables(t *testing.T) {
	for _, channel := range []string{"Chat:42:status", "chat::status", "-chat:42:status", "chat:_bad:status", "chat:42"} {
		if err := ValidateChannel(channel); err == nil {
			t.Errorf("ValidateChannel(%q) unexpectedly succeeded", channel)
		}
	}
	for _, pattern := range []string{"", "Chat:*", "chat::status", "-chat:*", "chat:_bad", "chat:x*", "chat:>", ">:x"} {
		if err := ValidatePattern(pattern); err == nil {
			t.Errorf("ValidatePattern(%q) unexpectedly succeeded", pattern)
		}
	}
	for _, valid := range []string{"*", "chat", "chat:*:status", "viewer.agent-hermes:_:event", ">", "_inbox:*", "plugins:_"} {
		if err := ValidatePattern(valid); err != nil {
			t.Errorf("ValidatePattern(%q): %v", valid, err)
		}
	}
}

func TestParseHello(t *testing.T) {
	valid := []byte(`{"type":"hello","protocol_version":1,"conn":"123e4567-e89b-42d3-a456-426614174000","manifest":{"id":"chat","version":"1","slots":{},"emits":{}},"managed":false}`)
	if _, err := ParseHello(valid); err != nil {
		t.Fatalf("valid hello: %v", err)
	}
	var base map[string]any
	if err := json.Unmarshal(valid, &base); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing manifest emits", func(v map[string]any) { delete(v["manifest"].(map[string]any), "emits") }},
		{"bad uuid version", func(v map[string]any) { v["conn"] = "123e4567-e89b-12d3-a456-426614174000" }},
		{"managed wrong type", func(v map[string]any) { v["managed"] = "false" }},
		{"slots not object", func(v map[string]any) { v["manifest"].(map[string]any)["slots"] = []any{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var candidate map[string]any
			encoded, _ := json.Marshal(base)
			_ = json.Unmarshal(encoded, &candidate)
			tt.mutate(candidate)
			encoded, _ = json.Marshal(candidate)
			if _, err := ParseHello(encoded); err == nil {
				t.Fatal("invalid hello unexpectedly succeeded")
			}
		})
	}
}

func TestParseClientFramePayloadIsArbitraryJSON(t *testing.T) {
	for _, payload := range []string{"null", `{"free":[1,true,"x"]}`, "42", `"text"`} {
		data := []byte(`{"type":"publish","channel":"demo:_:event","value":` + payload + `}`)
		frame, err := ParseClientFrame(data)
		if err != nil {
			t.Fatalf("payload %s: %v", payload, err)
		}
		if string(frame.Value) != payload {
			t.Fatalf("value = %s, want %s", frame.Value, payload)
		}
	}
}
