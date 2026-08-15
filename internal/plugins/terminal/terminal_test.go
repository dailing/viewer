package terminal

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestManifestContract(t *testing.T) {
	for _, slot := range []string{"create", "list", "write", "resize", "kill", "snapshot"} {
		channel := "terminal:*:" + slot
		if slot == "create" || slot == "list" {
			channel = "terminal:_:" + slot
		}
		if _, ok := Manifest.Slots[channel]; !ok {
			t.Errorf("manifest is missing %s", channel)
		}
	}
	for _, emit := range []string{"terminal:*:output", "terminal:*:status"} {
		if _, ok := Manifest.Emits[emit]; !ok {
			t.Errorf("manifest is missing %s", emit)
		}
	}
}

func TestShellCommandUsesLoginSemantics(t *testing.T) {
	_, command := shellCommand()
	if !strings.HasSuffix(command, " -l") || strings.Contains(command, " -f") || strings.Contains(command, "--noprofile") || strings.Contains(command, "--norc") {
		t.Fatalf("shell command is not a login shell: %q", command)
	}
}

func TestDecodeUTF8AcrossReadsAndInvalidInput(t *testing.T) {
	encoded := []byte("A界B")
	first, tail := decodeUTF8(encoded[:2], false)
	second, tail := decodeUTF8(append(tail, encoded[2:]...), false)
	if first+second != "A界B" || len(tail) != 0 {
		t.Fatalf("split decode = %q + %q, tail %x", first, second, tail)
	}
	invalid, tail := decodeUTF8([]byte{0xff, 'x'}, true)
	if invalid != "\uFFFDx" || len(tail) != 0 || !utf8.ValidString(invalid) {
		t.Fatalf("invalid decode = %q, tail %x", invalid, tail)
	}
}

func TestSnapshotNewestFirstBudgetAndOrder(t *testing.T) {
	s := &session{ring: make([]Entry, 0, 10)}
	for seq := int64(1); seq <= 10; seq++ {
		s.ring = append(s.ring, Entry{Seq: seq, TS: seq, Data: strings.Repeat("x", 100_000)})
	}
	entries := snapshotEntries(s, 10, 0, false)
	if len(entries) == 0 || len(entries) >= len(s.ring) {
		t.Fatalf("budget returned %d of %d entries", len(entries), len(s.ring))
	}
	if entries[len(entries)-1].Seq != 10 {
		t.Fatalf("newest sequence = %d, want 10", entries[len(entries)-1].Seq)
	}
	for index := 1; index < len(entries); index++ {
		if entries[index-1].Seq >= entries[index].Seq {
			t.Fatalf("entries are not ascending: %#v", entries)
		}
	}
	encoded, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) >= SnapshotBudget+4096 {
		t.Fatalf("snapshot wire size = %d", len(encoded))
	}
}
