package chat

import (
	"strings"
	"testing"
)

func TestDecisionPostPromptAppendedAfterEveryNonRawPrompt(t *testing.T) {
	role := SuperRole{ID: "role-a", Name: "utility", Prompt: "Handle miscellaneous work."}
	if strings.Contains(initialRolePrompt(role), "<viewer-decision>") {
		t.Fatal("stable role prompt must not own the per-turn Decision protocol")
	}
	fresh := initialPrompt(Workspace{CommonPrompt: "Workspace rules"}, Chat{CommonPrompt: "Chat rules"}, role, "Earlier context", "fresh user request")
	bridge := "While you were away, missed work follows.\n\nCurrent routed message follows:\nbridged user request"
	for _, test := range []struct {
		name   string
		prompt string
	}{
		{name: "fresh", prompt: fresh},
		{name: "resumed", prompt: "resumed user request"},
		{name: "bridge", prompt: bridge},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := withDecisionPostPrompt(test.prompt, false)
			if strings.Count(got, "<viewer-decision>") != 1 || !strings.HasSuffix(got, decisionPostPrompt) {
				t.Fatalf("Decision protocol must occur exactly once at the end:\n%s", got)
			}
			if !strings.HasPrefix(got, test.prompt+"\n\n") {
				t.Fatalf("Decision protocol must follow the complete routed prompt:\n%s", got)
			}
			if !strings.Contains(decisionPostPrompt, "option_action: end_current_task") {
				t.Fatal("Decision protocol does not describe the Task-ending option")
			}
		})
	}
}

func TestDecisionPostPromptLeavesRawUntouched(t *testing.T) {
	const prompt = "raw user request\n"
	if got := withDecisionPostPrompt(prompt, true); got != prompt {
		t.Fatalf("raw prompt changed:\n%q", got)
	}
}
