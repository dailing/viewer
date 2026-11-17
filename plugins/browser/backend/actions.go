package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const browserRuntimeSource = `(() => {
  if (globalThis.__viewerManagedBrowser) return globalThis.__viewerManagedBrowser;
  let latestSnapshot = null;
  let revision = 0;
  function fail(code, message) { const error = new Error(message); error.code = code; throw error; }
  function visible(element) { const style = getComputedStyle(element); return style.display !== "none" && style.visibility !== "hidden" && element.getClientRects().length > 0; }
  function textOf(element) { return String(element.getAttribute("aria-label") || element.innerText || element.getAttribute("title") || element.getAttribute("placeholder") || "").replace(/\s+/g, " ").trim().slice(0, 300); }
  function roleOf(element) {
    if (element.getAttribute("role")) return element.getAttribute("role");
    const tag = element.tagName.toLowerCase();
    if (tag === "input") { if (["button", "submit", "reset"].includes(element.type)) return "button"; if (["checkbox", "radio"].includes(element.type)) return element.type; return "textbox"; }
    if (tag === "textarea" || element.isContentEditable) return "textbox";
    if (tag === "select") return "combobox";
    if (tag === "a") return "link";
    if (tag === "button" || tag === "summary") return "button";
    return tag;
  }
  function describe(element, id) {
    const item = { id, role: roleOf(element), text: textOf(element), disabled: Boolean(element.disabled) };
    if (element.href) item.href = String(element.href).slice(0, 2000);
    if (["INPUT", "TEXTAREA", "SELECT"].includes(element.tagName)) {
      item.input_type = element.type || element.tagName.toLowerCase();
      if (element.type === "password") item.sensitive = true;
      else if (typeof element.value === "string") item.value = element.value.slice(0, 500);
    }
    return item;
  }
  function snapshot(payload) {
    const maxChars = Math.max(1000, Math.min(50000, Number(payload.max_chars) || 20000));
    const maxElements = Math.max(10, Math.min(500, Number(payload.max_elements) || 200));
    const selector = "a[href],button,input,textarea,select,summary,[role='button'],[role='link'],[contenteditable='true'],[tabindex]";
    const references = new Map(); const elements = [];
    for (const element of document.querySelectorAll(selector)) {
      if (elements.length >= maxElements) break;
      if (!visible(element)) continue;
      const id = "e" + (elements.length + 1); references.set(id, element); elements.push(describe(element, id));
    }
    const snapshotID = "snap_" + crypto.randomUUID(); latestSnapshot = { id: snapshotID, references }; revision += 1;
    const bodyText = (document.body?.innerText || "").replace(/\u0000/g, "");
    return { snapshot_id: snapshotID, revision, url: location.href, title: document.title, text: bodyText.slice(0, maxChars), text_truncated: bodyText.length > maxChars, elements, elements_truncated: elements.length >= maxElements };
  }
  function elementFrom(snapshotID, elementID) {
    if (!latestSnapshot || latestSnapshot.id !== snapshotID) fail("stale_snapshot", "Snapshot changed; call browser_snapshot again");
    const element = latestSnapshot.references.get(elementID);
    if (!element || !element.isConnected) fail("stale_element", "Element is no longer attached; call browser_snapshot again");
    return element;
  }
  function prepareClick(payload) {
    const element = elementFrom(payload.snapshot_id, payload.element_id);
    element.scrollIntoView({ block: "center", inline: "center" });
    const rect = element.getBoundingClientRect();
    if (!rect.width || !rect.height) fail("stale_element", "Element has no clickable area");
    return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2, element_id: payload.element_id };
  }
  function prepareType(payload) {
    const element = elementFrom(payload.snapshot_id, payload.element_id);
    if (element.type === "password") fail("sensitive_field", "Password fields must be filled by the user");
    if (element.type === "file" || element instanceof HTMLSelectElement) fail("unsupported_field", "Element cannot be typed into");
    if (!(element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement || element.isContentEditable)) fail("unsupported_field", "Element cannot be typed into");
    element.scrollIntoView({ block: "center", inline: "center" }); element.focus();
    if (payload.clear) {
      if (element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement) setNativeValue(element, "");
      else element.textContent = "";
      element.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "deleteContentBackward" }));
    }
    return { focused: true };
  }
  function assertActiveSafe() {
    const element = document.activeElement;
    if (element?.type === "password") fail("sensitive_field", "Keyboard input in password fields must be entered by the user");
    if (element?.type === "file") fail("unsupported_field", "Use browser_upload for file inputs");
    return { ok: true };
  }
  function dropdownOptions(payload) {
    const element = elementFrom(payload.snapshot_id, payload.element_id);
    if (!(element instanceof HTMLSelectElement)) fail("unsupported_field", "Element is not a select");
    return { options: Array.from(element.options).map(option => ({ value: option.value, label: option.label, selected: option.selected, disabled: option.disabled })) };
  }
  function selectOptions(payload) {
    const element = elementFrom(payload.snapshot_id, payload.element_id);
    if (!(element instanceof HTMLSelectElement)) fail("unsupported_field", "Element is not a select");
    const values = Array.isArray(payload.values) ? payload.values.map(String) : [];
    if (!element.multiple && values.length !== 1) fail("invalid_request", "Single select requires exactly one value");
    const available = new Set(Array.from(element.options).filter(option => !option.disabled).map(option => option.value));
    for (const value of values) if (!available.has(value)) fail("invalid_option", "Option does not exist or is disabled: " + value);
    for (const option of element.options) option.selected = values.includes(option.value);
    element.dispatchEvent(new InputEvent("input", { bubbles: true }));
    element.dispatchEvent(new Event("change", { bubbles: true }));
    return { selected: values };
  }
  function findText(payload) {
    const needle = String(payload.text || ""); if (!needle) fail("invalid_request", "text is required");
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      if (!node.textContent.includes(needle)) continue;
      const element = node.parentElement;
      if (!element || !visible(element)) continue;
      element.scrollIntoView({ block: "center", inline: "center" });
      return { found: true, text: needle };
    }
    return { found: false, text: needle };
  }
  function validateUpload(payload) {
    const element = elementFrom(payload.snapshot_id, payload.element_id);
    if (!(element instanceof HTMLInputElement) || element.type !== "file") fail("unsupported_field", "Element is not a file input");
    if (element.disabled) fail("stale_element", "File input is disabled");
    if (!element.multiple && Array.isArray(payload.paths) && payload.paths.length !== 1) fail("invalid_request", "File input accepts exactly one file");
    return { valid: true };
  }
  function setNativeValue(element, value) {
    const prototype = element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    const descriptor = Object.getOwnPropertyDescriptor(prototype, "value");
    if (descriptor?.set) descriptor.set.call(element, value); else element.value = value;
  }
  function fill(payload) {
    const element = elementFrom(payload.snapshot_id, payload.element_id);
    if (element.type === "password") fail("sensitive_field", "Password fields must be filled by the user");
    const text = String(payload.text ?? "");
    if (element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement) {
      if (["checkbox", "radio", "button", "submit", "file", "reset"].includes(element.type)) fail("unsupported_field", "Unsupported input type " + element.type);
      setNativeValue(element, text);
    } else if (element instanceof HTMLSelectElement) {
      element.value = text; if (element.value !== text) fail("invalid_option", "Option does not exist");
    } else if (element.isContentEditable) element.textContent = text;
    else fail("unsupported_field", "Element cannot be filled");
    element.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertText", data: text }));
    element.dispatchEvent(new Event("change", { bubbles: true }));
    return { filled: true, element_id: payload.element_id };
  }
  function scroll(payload) {
    if (payload.element_id) { const element = elementFrom(payload.snapshot_id, payload.element_id); element.scrollIntoView({ block: "center", inline: "center" }); return { scrolled: true, element_id: payload.element_id }; }
    const delta = Number(payload.delta_y); window.scrollBy({ top: Number.isFinite(delta) && delta !== 0 ? delta : 600, behavior: "auto" });
    return { scrolled: true, x: window.scrollX, y: window.scrollY };
  }
  function wait(payload) {
    const needle = String(payload.text || ""); if (!needle) fail("invalid_request", "text is required");
    const timeout = Math.max(100, Math.min(30000, Number(payload.timeout_ms) || 10000));
    if (document.body?.innerText.includes(needle)) return Promise.resolve({ found: true, text: needle, waited_ms: 0 });
    return new Promise((resolve, reject) => {
      const started = Date.now();
      const observer = new MutationObserver(() => { if (document.body?.innerText.includes(needle)) { cleanup(); resolve({ found: true, text: needle, waited_ms: Date.now() - started }); } });
      const timer = setTimeout(() => { cleanup(); reject(Object.assign(new Error("Text wait timed out; page state may have changed"), { code: "wait_timeout" })); }, timeout);
      function cleanup() { observer.disconnect(); clearTimeout(timer); }
      observer.observe(document.documentElement, { childList: true, subtree: true, characterData: true });
    });
  }
  function execute(action, payload) {
    switch (action) {
      case "snapshot": return snapshot(payload);
      case "prepare_click": return prepareClick(payload);
      case "prepare_type": return prepareType(payload);
      case "assert_active_safe": return assertActiveSafe();
      case "dropdown_options": return dropdownOptions(payload);
      case "select": return selectOptions(payload);
      case "find_text": return findText(payload);
      case "validate_upload": return validateUpload(payload);
      case "fill": return fill(payload);
      case "scroll": return scroll(payload);
      case "wait": return wait(payload);
      default: fail("unknown_action", "Unsupported browser action " + action);
    }
  }
  globalThis.__viewerManagedBrowser = { execute, resolveElement: elementFrom };
  return globalThis.__viewerManagedBrowser;
})()`

func (p *chromePage) executeRuntime(ctx context.Context, action string, payload map[string]any) (map[string]any, error) {
	payloadJSON, _ := json.Marshal(payload)
	expression := fmt.Sprintf(`Promise.resolve().then(() => (%s).execute(%s, %s)).then(
      result => ({ok:true,result}),
      error => ({ok:false,error:{code:error.code || "page_action_failed",message:String(error.message || error)}})
    )`, browserRuntimeSource, strconvQuote(action), string(payloadJSON))
	response, err := p.evaluate(ctx, expression)
	if err != nil {
		return nil, err
	}
	if ok, _ := response["ok"].(bool); !ok {
		errorValue, _ := response["error"].(map[string]any)
		code := stringField(errorValue, "code")
		if code == "" {
			code = "page_action_failed"
		}
		return nil, errorf(code, "%s", stringField(errorValue, "message"))
	}
	result, _ := response["result"].(map[string]any)
	if result == nil {
		result = make(map[string]any)
	}
	return result, nil
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func (p *chromePage) Execute(ctx context.Context, action string, payload map[string]any) (any, error) {
	switch action {
	case "navigate":
		rawURL := stringField(payload, "url")
		if !validHTTPURL(rawURL) {
			return nil, errorf("invalid_url", "only HTTP(S) navigation is allowed")
		}
		if err := p.Navigate(ctx, rawURL); err != nil {
			return nil, err
		}
		url, title, _ := p.Metadata(ctx)
		return map[string]any{"navigated": true, "url": url, "title": title}, nil
	case "click":
		return p.click(ctx, payload, "left", 1)
	case "double_click":
		return p.click(ctx, payload, "left", 2)
	case "right_click":
		return p.click(ctx, payload, "right", 1)
	case "hover":
		point, err := p.elementPoint(ctx, payload)
		if err != nil {
			return nil, err
		}
		if err := p.Input(ctx, map[string]any{"type": "mouseMoved", "x": point["x"], "y": point["y"]}); err != nil {
			return nil, err
		}
		return map[string]any{"hovered": true, "element_id": point["element_id"]}, nil
	case "type":
		if _, err := p.executeRuntime(ctx, "prepare_type", payload); err != nil {
			return nil, err
		}
		text := stringField(payload, "text")
		if text != "" {
			if err := p.Input(ctx, map[string]any{"type": "insertText", "text": text}); err != nil {
				return nil, err
			}
		}
		return map[string]any{"typed": true, "element_id": stringField(payload, "element_id")}, nil
	case "send_keys":
		return p.sendKeys(ctx, stringField(payload, "keys"))
	case "evaluate":
		value, valueType, err := p.evaluateAny(ctx, stringField(payload, "expression"))
		if err != nil {
			return nil, errorf("evaluation_error", "%v", err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, errorf("evaluation_error", "result is not JSON serializable: %v", err)
		}
		if len(encoded) > 256<<10 {
			return nil, errorf("result_too_large", "JavaScript result exceeds 256 KiB; narrow the query")
		}
		return map[string]any{"value": value, "value_type": valueType}, nil
	case "screenshot":
		return p.screenshot(ctx, payload)
	case "upload":
		return p.upload(ctx, payload)
	case "downloads":
		return map[string]any{"downloads": p.manager.downloadsForPage(p.pageID)}, nil
	case "snapshot", "fill", "scroll", "dropdown_options", "select", "find_text":
		return p.executeRuntime(ctx, action, payload)
	case "wait":
		return p.wait(ctx, payload)
	case "go_back", "go_forward":
		return p.historyNavigate(ctx, action == "go_forward")
	case "reload":
		if _, err := p.conn.call(ctx, "Page.reload", map[string]any{"ignoreCache": payload["ignore_cache"] == true}, nil); err != nil {
			return nil, err
		}
		return p.actionState(ctx, "reloaded", true)
	default:
		return nil, errorf("unknown_action", "unsupported browser action %q", action)
	}
}

func (p *chromePage) elementPoint(ctx context.Context, payload map[string]any) (map[string]any, error) {
	point, err := p.executeRuntime(ctx, "prepare_click", payload)
	if err != nil {
		return nil, err
	}
	if _, ok := numberValue(point["x"]); !ok {
		return nil, errors.New("click coordinates unavailable")
	}
	if _, ok := numberValue(point["y"]); !ok {
		return nil, errors.New("click coordinates unavailable")
	}
	return point, nil
}

func (p *chromePage) click(ctx context.Context, payload map[string]any, button string, count int) (map[string]any, error) {
	point, err := p.elementPoint(ctx, payload)
	if err != nil {
		return nil, err
	}
	buttons := 1
	if button == "right" {
		buttons = 2
	}
	for clickCount := 1; clickCount <= count; clickCount++ {
		for _, eventType := range []string{"mousePressed", "mouseReleased"} {
			if err := p.Input(ctx, map[string]any{
				"type": eventType, "x": point["x"], "y": point["y"],
				"button": button, "buttons": buttons, "clickCount": clickCount,
			}); err != nil {
				return nil, err
			}
		}
	}
	return map[string]any{"clicked": true, "element_id": point["element_id"], "click_count": count, "button": button}, nil
}

type keyStroke struct {
	Key       string
	Code      string
	KeyCode   int
	Modifiers int
	Text      string
}

func parseKeyChord(chord string) (keyStroke, error) {
	parts := strings.Split(chord, "+")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return keyStroke{}, errorf("invalid_request", "invalid key chord %q", chord)
	}
	stroke := keyStroke{}
	for _, modifier := range parts[:len(parts)-1] {
		switch strings.ToLower(modifier) {
		case "alt":
			stroke.Modifiers |= 1
		case "control", "ctrl":
			stroke.Modifiers |= 2
		case "meta", "cmd", "command":
			stroke.Modifiers |= 4
		case "shift":
			stroke.Modifiers |= 8
		default:
			return keyStroke{}, errorf("invalid_request", "unknown key modifier %q", modifier)
		}
	}
	key := parts[len(parts)-1]
	special := map[string]keyStroke{
		"enter":  {Key: "Enter", Code: "Enter", KeyCode: 13, Text: "\r"},
		"escape": {Key: "Escape", Code: "Escape", KeyCode: 27}, "esc": {Key: "Escape", Code: "Escape", KeyCode: 27},
		"tab": {Key: "Tab", Code: "Tab", KeyCode: 9}, "backspace": {Key: "Backspace", Code: "Backspace", KeyCode: 8},
		"delete": {Key: "Delete", Code: "Delete", KeyCode: 46}, "arrowleft": {Key: "ArrowLeft", Code: "ArrowLeft", KeyCode: 37},
		"arrowup": {Key: "ArrowUp", Code: "ArrowUp", KeyCode: 38}, "arrowright": {Key: "ArrowRight", Code: "ArrowRight", KeyCode: 39},
		"arrowdown": {Key: "ArrowDown", Code: "ArrowDown", KeyCode: 40}, "home": {Key: "Home", Code: "Home", KeyCode: 36},
		"end": {Key: "End", Code: "End", KeyCode: 35}, "pageup": {Key: "PageUp", Code: "PageUp", KeyCode: 33},
		"pagedown": {Key: "PageDown", Code: "PageDown", KeyCode: 34}, "space": {Key: " ", Code: "Space", KeyCode: 32, Text: " "},
	}
	if mapped, ok := special[strings.ToLower(key)]; ok {
		stroke.Key, stroke.Code, stroke.KeyCode, stroke.Text = mapped.Key, mapped.Code, mapped.KeyCode, mapped.Text
	} else if runes := []rune(key); len(runes) == 1 {
		stroke.Key = key
		stroke.Text = key
		if (runes[0] >= 'a' && runes[0] <= 'z') || (runes[0] >= 'A' && runes[0] <= 'Z') {
			upper := strings.ToUpper(key)
			stroke.Code = "Key" + upper
			stroke.KeyCode = int(upper[0])
		} else if runes[0] >= '0' && runes[0] <= '9' {
			stroke.Code = "Digit" + key
			stroke.KeyCode = int(runes[0])
		} else {
			stroke.Code = key
		}
	} else {
		return keyStroke{}, errorf("invalid_request", "unknown key %q", key)
	}
	if stroke.Modifiers&3 != 0 || stroke.Modifiers&4 != 0 {
		stroke.Text = ""
	}
	return stroke, nil
}

func (p *chromePage) sendKeys(ctx context.Context, chord string) (map[string]any, error) {
	stroke, err := parseKeyChord(chord)
	if err != nil {
		return nil, err
	}
	if _, err := p.executeRuntime(ctx, "assert_active_safe", map[string]any{}); err != nil {
		return nil, err
	}
	event := map[string]any{
		"type": "keyDown", "key": stroke.Key, "code": stroke.Code,
		"keyCode": stroke.KeyCode, "modifiers": stroke.Modifiers, "text": stroke.Text,
	}
	if err := p.Input(ctx, event); err != nil {
		return nil, err
	}
	event["type"] = "keyUp"
	event["text"] = ""
	if err := p.Input(ctx, event); err != nil {
		return nil, err
	}
	return map[string]any{"sent": true, "keys": chord}, nil
}

func (p *chromePage) Input(ctx context.Context, event map[string]any) error {
	eventType := stringField(event, "type")
	switch eventType {
	case "mousePressed", "mouseReleased", "mouseMoved", "mouseWheel":
		params := map[string]any{
			"type": eventType, "x": numberField(event, "x"), "y": numberField(event, "y"),
			"button": stringField(event, "button"), "buttons": intField(event, "buttons"),
		}
		if eventType == "mouseWheel" {
			params["deltaX"], params["deltaY"] = numberField(event, "deltaX"), numberField(event, "deltaY")
			params["button"], params["buttons"] = "none", 0
		} else if eventType != "mouseMoved" {
			params["clickCount"] = max(1, intField(event, "clickCount"))
		}
		_, err := p.conn.call(ctx, "Input.dispatchMouseEvent", params, nil)
		return err
	case "keyDown", "keyUp":
		params := map[string]any{
			"type": eventType, "key": stringField(event, "key"), "code": stringField(event, "code"),
			"windowsVirtualKeyCode": intField(event, "keyCode"), "nativeVirtualKeyCode": intField(event, "keyCode"),
			"modifiers": intField(event, "modifiers"),
		}
		if text := stringField(event, "text"); text != "" && eventType == "keyDown" {
			params["text"] = text
		}
		_, err := p.conn.call(ctx, "Input.dispatchKeyEvent", params, nil)
		return err
	case "insertText":
		_, err := p.conn.call(ctx, "Input.insertText", map[string]any{"text": stringField(event, "text")}, nil)
		return err
	default:
		return errorf("invalid_input", "unsupported input event %q", eventType)
	}
}

type waitOptions struct {
	predicate string
	value     string
	timeout   time.Duration
}

func parseWaitPayload(payload map[string]any) (waitOptions, error) {
	options := waitOptions{timeout: time.Second}
	for _, predicate := range []string{"text", "url_contains", "ready_state", "download_id"} {
		value := strings.TrimSpace(stringField(payload, predicate))
		if value == "" {
			continue
		}
		if options.predicate != "" {
			return options, errorf("invalid_request", "only one wait predicate is allowed")
		}
		options.predicate, options.value = predicate, value
	}
	if options.predicate == "ready_state" {
		switch options.value {
		case "loading", "interactive", "complete":
		default:
			return options, errorf("invalid_request", "ready_state must be loading, interactive, or complete")
		}
	}
	if options.predicate == "download_id" && !validateDownloadID(options.value) {
		return options, errorf("invalid_request", "download_id is invalid")
	}
	if raw, exists := payload["text"]; exists && options.predicate == "" && strings.TrimSpace(stringField(payload, "text")) == "" && raw != nil && raw != "" {
		return options, errorf("invalid_request", "text must not be empty")
	}
	if options.predicate != "" {
		options.timeout = 10 * time.Second
	}
	if raw, exists := payload["timeout_ms"]; exists {
		milliseconds, ok := numberValue(raw)
		if !ok || milliseconds < 0 {
			return options, errorf("invalid_request", "timeout_ms must be a non-negative number")
		}
		milliseconds = max(100, min(30000, milliseconds))
		options.timeout = time.Duration(milliseconds) * time.Millisecond
	}
	return options, nil
}

func (p *chromePage) wait(ctx context.Context, payload map[string]any) (map[string]any, error) {
	options, err := parseWaitPayload(payload)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	waitCtx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	if options.predicate == "download_id" {
		result, err := p.manager.waitDownload(waitCtx, p.pageID, options.value)
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errorf("wait_timeout", "download wait timed out; call browser_downloads before retrying")
		}
		if err != nil {
			return nil, err
		}
		result["waited_ms"] = time.Since(started).Milliseconds()
		return result, nil
	}
	predicate := "true"
	switch options.predicate {
	case "text":
		predicate = "Boolean(document.body && document.body.innerText.includes(" + strconvQuote(options.value) + "))"
	case "url_contains":
		predicate = "location.href.includes(" + strconvQuote(options.value) + ")"
	case "ready_state":
		predicate = "document.readyState === " + strconvQuote(options.value)
	}
	expression := fmt.Sprintf(`({url:location.href,title:document.title,ready_state:document.readyState,found:%s})`, predicate)
	for {
		state, evalErr := p.evaluate(waitCtx, expression)
		if evalErr == nil {
			if found, _ := state["found"].(bool); found {
				state["waited_ms"] = time.Since(started).Milliseconds()
				return state, nil
			}
		}
		select {
		case <-waitCtx.Done():
			return nil, errorf("wait_timeout", "browser wait timed out; take a fresh snapshot")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (p *chromePage) actionState(ctx context.Context, key string, value any) (map[string]any, error) {
	state, err := p.evaluate(ctx, `({url:location.href,title:document.title,ready_state:document.readyState})`)
	if err != nil {
		return nil, err
	}
	state[key] = value
	return state, nil
}

func (p *chromePage) historyNavigate(ctx context.Context, forward bool) (map[string]any, error) {
	raw, err := p.conn.call(ctx, "Page.getNavigationHistory", map[string]any{}, nil)
	if err != nil {
		return nil, err
	}
	var history struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &history); err != nil {
		return nil, err
	}
	index := history.CurrentIndex - 1
	if forward {
		index = history.CurrentIndex + 1
	}
	if index < 0 || index >= len(history.Entries) {
		return nil, errorf("navigation_boundary", "no browser history entry in that direction")
	}
	if _, err := p.conn.call(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": history.Entries[index].ID}, nil); err != nil {
		return nil, err
	}
	return p.actionState(ctx, "navigated", true)
}

func numberValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	default:
		return 0, false
	}
}

func numberField(value map[string]any, key string) float64 {
	number, _ := numberValue(value[key])
	return number
}

func inputContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 5*time.Second)
}
