package chat

import (
	"fmt"
	"strings"
	"testing"
)

func TestCiteIDsOrderDedupeCap(t *testing.T) {
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	ids := citeIDs("see @" + b + " and @" + a + " then @" + b + " again, not @xyz, not @" + strings.Repeat("G", 32))
	if len(ids) != 2 || ids[0] != b || ids[1] != a {
		t.Fatalf("ids=%v, want [%s %s] (first-appearance order, deduped, non-hex ignored)", ids, b, a)
	}
	var tokens []string
	for i := 0; i < citeMaxPerPrompt+3; i++ {
		tokens = append(tokens, "@"+fmt.Sprintf("%032x", i+1))
	}
	if got := citeIDs(strings.Join(tokens, " ")); len(got) != citeMaxPerPrompt {
		t.Fatalf("cap: got %d ids, want %d", len(got), citeMaxPerPrompt)
	}
	if got := citeIDs("no citations here"); len(got) != 0 {
		t.Fatalf("plain text: got %v", got)
	}
}

func TestBuildCitedSection(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.store.close() })
	sameChat := strings.Repeat("1", 32)
	otherChat := strings.Repeat("2", 32)
	missing := strings.Repeat("3", 32)
	longID := strings.Repeat("4", 32)
	rows := []*Message{
		{ID: sameChat, ChatID: "chat-a", TurnID: "t1", Role: "user", Text: "原始问题内容", CreatedAt: 1758000000000},
		{ID: otherChat, ChatID: "chat-b", TurnID: "t2", Role: "assistant", RoleName: "utility", Text: "另一个 chat 的回答", CreatedAt: 1758000060000},
		{ID: longID, ChatID: "chat-a", TurnID: "t3", Role: "assistant", RoleName: "utility", Text: strings.Repeat("长", citeContentByteBudget), CreatedAt: 1758000120000},
	}
	for _, row := range rows {
		if err := p.store.addMessage(row); err != nil {
			t.Fatal(err)
		}
	}
	section := p.buildCitedSection("chat-a", "汇总一下 @"+sameChat+" 和 @"+otherChat+" 以及 @"+missing+" 的结论")
	for _, want := range []string{
		"The user cited the following message(s)",
		"[user cited message id " + sameChat + " — user, at ",
		"content:\n原始问题内容",
		"[user cited message id " + otherChat + " — utility, at ",
		"from a different chat (chat-b)",
		"content:\n另一个 chat 的回答",
		"[user cited message id " + missing + " — content unavailable: message not found]",
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("section missing %q:\n%s", want, section)
		}
	}
	// Same-chat citations carry no cross-chat marker.
	if strings.Count(section, "from a different chat") != 1 {
		t.Fatalf("cross-chat marker count wrong:\n%s", section)
	}
	// Truncation: over-budget cited content is cut UTF-8-safely and marked.
	truncated := p.buildCitedSection("chat-a", "@"+longID)
	if !strings.Contains(truncated, "…[cited content truncated]") {
		t.Fatalf("truncation marker missing:\n%.200s", truncated)
	}
	// No tokens → no section.
	if got := p.buildCitedSection("chat-a", "plain message"); got != "" {
		t.Fatalf("plain message produced section: %q", got)
	}
}

func TestWithCitedSection(t *testing.T) {
	if got := withCitedSection("", "query"); got != "query" {
		t.Fatalf("empty section: %q", got)
	}
	if got := withCitedSection("CITED", "query"); got != "CITED\n\nquery" {
		t.Fatalf("joined: %q", got)
	}
}
