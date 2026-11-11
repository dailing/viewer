package chat

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	decisionOpenTag       = "<viewer-decision>"
	decisionCloseTag      = "</viewer-decision>"
	decisionMarkerMaxByte = 16 * 1024
)

var decisionKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type decisionMarker struct {
	Question    string
	OptionsJSON string
	Display     string
	Kind        string
	Tone        string
	Warning     string
	Start       int
	End         int
}

type decisionSourceLine struct {
	text    string
	start   int
	end     int
	fullEnd int
}

func decisionSourceLines(text string) []decisionSourceLine {
	lines := make([]decisionSourceLine, 0, strings.Count(text, "\n")+1)
	for start := 0; start < len(text); {
		end := strings.IndexByte(text[start:], '\n')
		fullEnd := len(text)
		if end >= 0 {
			end += start
			fullEnd = end + 1
		} else {
			end = len(text)
		}
		contentEnd := end
		if contentEnd > start && text[contentEnd-1] == '\r' {
			contentEnd--
		}
		lines = append(lines, decisionSourceLine{text: text[start:contentEnd], start: start, end: contentEnd, fullEnd: fullEnd})
		start = fullEnd
	}
	return lines
}

// markdownFence describes a CommonMark-style fenced code delimiter. It is
// used only to exclude markers in code; it never rewrites the source text.
func markdownFence(line string) (marker byte, size int, rest string, ok bool) {
	indent := 0
	for indent < len(line) && indent < 4 && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent >= len(line) || line[indent] != '`' && line[indent] != '~' {
		return 0, 0, "", false
	}
	marker = line[indent]
	size = 0
	for indent+size < len(line) && line[indent+size] == marker {
		size++
	}
	if size < 3 {
		return 0, 0, "", false
	}
	rest = line[indent+size:]
	if marker == '`' && strings.ContainsRune(rest, '`') {
		return 0, 0, "", false
	}
	return marker, size, rest, true
}

func maskMarkdownInlineCode(line string) string {
	source := []byte(line)
	masked := append([]byte(nil), source...)
	for start := 0; start < len(source); {
		if source[start] != '`' {
			start++
			continue
		}
		backslashes := 0
		for index := start - 1; index >= 0 && source[index] == '\\'; index-- {
			backslashes++
		}
		if backslashes%2 != 0 {
			start++
			continue
		}
		openingEnd := start
		for openingEnd < len(source) && source[openingEnd] == '`' {
			openingEnd++
		}
		size := openingEnd - start
		closingStart, closingEnd := -1, -1
		for search := openingEnd; search < len(source); {
			if source[search] != '`' {
				search++
				continue
			}
			runEnd := search
			for runEnd < len(source) && source[runEnd] == '`' {
				runEnd++
			}
			if runEnd-search == size {
				closingStart, closingEnd = search, runEnd
				break
			}
			search = runEnd
		}
		if closingStart < 0 {
			start = openingEnd
			continue
		}
		for index := start; index < closingEnd; index++ {
			masked[index] = ' '
		}
		start = closingEnd
	}
	return string(masked)
}

func parseDecisionMarker(text string) *decisionMarker {
	lines := decisionSourceLines(text)
	var opening, closing *decisionSourceLine
	candidates := 0
	invalid := false
	var fenceMarker byte
	fenceSize := 0
	for index := range lines {
		line := &lines[index]
		if fenceMarker != 0 {
			marker, size, rest, ok := markdownFence(line.text)
			if ok && marker == fenceMarker && size >= fenceSize && strings.TrimSpace(rest) == "" {
				fenceMarker = 0
				fenceSize = 0
			}
			continue
		}
		if marker, size, _, ok := markdownFence(line.text); ok {
			fenceMarker = marker
			fenceSize = size
			continue
		}
		lineText := maskMarkdownInlineCode(line.text)
		if !strings.Contains(lineText, decisionOpenTag) && !strings.Contains(lineText, decisionCloseTag) {
			continue
		}
		candidates++
		switch line.text {
		case decisionOpenTag:
			if opening != nil || closing != nil {
				invalid = true
			} else {
				opening = line
			}
		case decisionCloseTag:
			if opening == nil || closing != nil {
				invalid = true
			} else {
				closing = line
			}
		default:
			invalid = true
		}
	}
	if invalid || candidates == 0 || opening == nil || closing == nil || candidates != 2 || fenceMarker != 0 {
		return nil
	}
	if strings.TrimSpace(text[closing.fullEnd:]) != "" {
		return nil
	}
	if closing.end-opening.start > decisionMarkerMaxByte {
		return nil
	}
	body := text[opening.fullEnd:closing.start]
	if !utf8.ValidString(body) {
		return nil
	}
	values := make(map[string][]string)
	optionActions := map[int]string{}
	optionCount, previousKey := 0, ""
	for _, line := range decisionSourceLines(body) {
		if strings.Trim(line.text, " 	\r") == "" {
			continue
		}
		colon := strings.IndexByte(line.text, ':')
		if colon <= 0 {
			return nil
		}
		key := line.text[:colon]
		if !decisionKeyPattern.MatchString(key) {
			return nil
		}
		value := strings.Trim(line.text[colon+1:], " 	\r")
		switch key {
		case "version", "kind", "question", "option", "display", "tone":
		case "option_action":
			if previousKey != "option" || value != "end_current_task" || len(optionActions) != 0 {
				return nil
			}
			optionActions[optionCount-1] = value
		default:
			return nil
		}
		if key == "option" {
			optionCount++
		}
		values[key] = append(values[key], value)
		previousKey = key
	}
	for key, entries := range values {
		if key != "option" && key != "option_action" && len(entries) != 1 {
			return nil
		}
	}
	if len(values["version"]) != 1 || values["version"][0] != "1" || len(values["kind"]) != 1 || values["kind"][0] != "choice" {
		return nil
	}
	question := ""
	if len(values["question"]) == 1 {
		question = values["question"][0]
	}
	if utf8.RuneCountInString(question) > 2000 || len(values["option"]) > 8 {
		return nil
	}
	options := make([]map[string]string, 0, len(values["option"]))
	for index, option := range values["option"] {
		if option == "" || utf8.RuneCountInString(option) > 500 {
			return nil
		}
		encoded := map[string]string{"id": fmt.Sprintf("option-%d", index+1), "label": option}
		if action := optionActions[index]; action != "" {
			encoded["action"] = action
		}
		options = append(options, encoded)
	}
	display, tone := "auto", "neutral"
	warnings := []string{}
	if entries := values["display"]; len(entries) == 1 {
		switch entries[0] {
		case "auto", "buttons", "list":
			display = entries[0]
		default:
			warnings = append(warnings, "invalid display; using auto")
		}
	}
	if entries := values["tone"]; len(entries) == 1 {
		switch entries[0] {
		case "neutral", "warning":
			tone = entries[0]
		default:
			warnings = append(warnings, "invalid tone; using neutral")
		}
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		return nil
	}
	return &decisionMarker{Question: question, OptionsJSON: string(encoded), Display: display, Kind: "choice", Tone: tone,
		Warning: strings.Join(warnings, "; "), Start: opening.start, End: closing.end}
}
