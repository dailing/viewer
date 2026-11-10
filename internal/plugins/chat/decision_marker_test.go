package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecisionMarkerFlatGrammar(t *testing.T) {
	text := "请选择：\n<viewer-decision>\nversion: 1\nkind: choice\nquestion: 接下来按哪个方向继续？\noption: 按推荐方案：先修改引言\noption: 同时修改引言和摘要 # 不是注释\ndisplay: buttons\ntone: warning\n</viewer-decision>"
	marker := parseDecisionMarker(text)
	if marker == nil {
		t.Fatal("valid marker was rejected")
	}
	if marker.Question != "接下来按哪个方向继续？" || marker.Kind != "choice" || marker.Display != "buttons" || marker.Tone != "warning" {
		t.Fatalf("unexpected marker: %#v", marker)
	}
	var options []map[string]string
	if err := json.Unmarshal([]byte(marker.OptionsJSON), &options); err != nil {
		t.Fatal(err)
	}
	if len(options) != 2 || options[0]["label"] != "按推荐方案：先修改引言" || options[1]["label"] != "同时修改引言和摘要 # 不是注释" {
		t.Fatalf("options were not preserved: %#v", options)
	}
	if got := text[marker.Start:marker.End]; !strings.HasPrefix(got, decisionOpenTag) || !strings.HasSuffix(got, decisionCloseTag) {
		t.Fatalf("invalid byte range %d..%d: %q", marker.Start, marker.End, got)
	}
}

func TestDecisionMarkerEndCurrentTaskOption(t *testing.T) {
	marker := parseDecisionMarker("工作已完成。\n<viewer-decision>\nversion: 1\nkind: choice\nquestion: What should happen next?\noption: Continue checking\noption: End current task\noption_action: end_current_task\ndisplay: buttons\ntone: neutral\n</viewer-decision>")
	if marker == nil {
		t.Fatal("valid Task-ending option was rejected")
	}
	options, err := parseDecisionOptions(marker.OptionsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 2 || options[0].Action != "" || options[1].ID != "option-2" || options[1].Label != "End current task" || options[1].Action != "end_current_task" {
		t.Fatalf("Task-ending option mismatch: %#v", options)
	}
}

func TestDecisionMarkerDisplayFallbackOnly(t *testing.T) {
	marker := parseDecisionMarker("<viewer-decision>\nversion: 1\nkind: choice\nquestion: Continue?\ndisplay: modal\ntone: dangerous\n</viewer-decision>")
	if marker == nil || marker.Display != "auto" || marker.Tone != "neutral" || marker.Warning == "" {
		t.Fatalf("display fallback mismatch: %#v", marker)
	}
	withoutOptions := parseDecisionMarker("<viewer-decision>\nversion: 1\nkind: choice\nquestion: Free text?\n</viewer-decision>")
	if withoutOptions == nil || withoutOptions.OptionsJSON != "[]" || withoutOptions.Display != "auto" {
		t.Fatalf("free-text decision mismatch: %#v", withoutOptions)
	}
	withoutQuestion := parseDecisionMarker("<viewer-decision>\nversion: 1\nkind: choice\n</viewer-decision>")
	if withoutQuestion == nil || withoutQuestion.Question != "" || withoutQuestion.OptionsJSON != "[]" {
		t.Fatalf("optional question mismatch: %#v", withoutQuestion)
	}
}

func TestDecisionMarkerRejectsInvalidCore(t *testing.T) {
	valid := func(body string) string { return "<viewer-decision>\n" + body + "\n</viewer-decision>" }
	cases := map[string]string{
		"old fenced JSON":      "```decision\n{\"question\":\"old\"}\n```",
		"missing version":      valid("kind: choice\nquestion: Continue?"),
		"unknown version":      valid("version: 2\nkind: choice\nquestion: Continue?"),
		"unknown kind":         valid("version: 1\nkind: action\nquestion: Continue?"),
		"action before option": valid("version: 1\nkind: choice\noption_action: end_current_task\noption: End current task"),
		"unknown action":       valid("version: 1\nkind: choice\noption: Do something\noption_action: run_arbitrary_code"),
		"detached action":      valid("version: 1\nkind: choice\noption: End current task\nquestion: Continue?\noption_action: end_current_task"),
		"multiple end actions": valid("version: 1\nkind: choice\noption: End current task\noption_action: end_current_task\noption: End again\noption_action: end_current_task"),
		"unknown key":          valid("version: 1\nkind: choice\nquestion: Continue?\ntask: other"),
		"duplicate singleton":  valid("version: 1\nkind: choice\nquestion: One\nquestion: Two"),
		"empty option":         valid("version: 1\nkind: choice\nquestion: Continue?\noption:  "),
		"missing colon":        valid("version: 1\nkind: choice\nquestion Continue?"),
		"full width colon":     valid("version: 1\nkind: choice\nquestion：Continue?"),
		"indented key":         valid("version: 1\nkind: choice\n question: Continue?"),
		"multiline YAML":       valid("version: 1\nkind: choice\nquestion: |\n  Continue?"),
		"inline opening":       "prefix <viewer-decision>\nversion: 1\nkind: choice\nquestion: Continue?\n</viewer-decision>",
		"missing close":        "<viewer-decision>\nversion: 1\nkind: choice\nquestion: Continue?",
		"text after close":     valid("version: 1\nkind: choice\nquestion: Continue?") + "\nafter",
		"multiple markers":     valid("version: 1\nkind: choice\nquestion: One") + "\n" + valid("version: 1\nkind: choice\nquestion: Two"),
		"nested marker":        "<viewer-decision>\n<viewer-decision>\nversion: 1\nkind: choice\nquestion: Continue?\n</viewer-decision>\n</viewer-decision>",
		"too many options":     valid("version: 1\nkind: choice\nquestion: Continue?\n" + strings.Repeat("option: x\n", 9)),
		"long option":          valid("version: 1\nkind: choice\nquestion: Continue?\noption: " + strings.Repeat("x", 501)),
		"long question":        valid("version: 1\nkind: choice\nquestion: " + strings.Repeat("x", 2001)),
		"oversized marker":     valid("version: 1\nkind: choice\nquestion: " + strings.Repeat("界", 2000) + "\n" + strings.Repeat("option: "+strings.Repeat("界", 500)+"\n", 8)),
		"markdown fenced JSON": "```json\n<viewer-decision>\nversion: 1\nkind: choice\nquestion: Continue?\n</viewer-decision>\n```",
		"unclosed fence":       "```text\n<viewer-decision>\nversion: 1\nkind: choice\nquestion: Continue?\n</viewer-decision>",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if marker := parseDecisionMarker(text); marker != nil {
				t.Fatalf("invalid marker accepted: %#v", marker)
			}
		})
	}
	invalidUTF8 := "<viewer-decision>\nversion: 1\nkind: choice\nquestion: " + string([]byte{0xff}) + "\n</viewer-decision>"
	if marker := parseDecisionMarker(invalidUTF8); marker != nil {
		t.Fatal("non-UTF-8 marker accepted")
	}
}

func TestDecisionMarkerCRLFAndCodeFenceBoundaries(t *testing.T) {
	text := "回答\r\n<viewer-decision>\r\nversion: 1\r\nkind: choice\r\nquestion: Continue?\r\n</viewer-decision>\r\n"
	marker := parseDecisionMarker(text)
	if marker == nil {
		t.Fatal("CRLF marker was rejected")
	}
	if got := text[marker.Start:marker.End]; got != "<viewer-decision>\r\nversion: 1\r\nkind: choice\r\nquestion: Continue?\r\n</viewer-decision>" {
		t.Fatalf("wrong CRLF offsets: %q", got)
	}
	fenced := "~~~markdown\n<viewer-decision>\nversion: 1\nkind: choice\nquestion: Continue?\n</viewer-decision>\n~~~~\n"
	if parseDecisionMarker(fenced) != nil {
		t.Fatal("marker inside tilde fence was parsed")
	}
}
