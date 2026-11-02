package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
    const selector = "a[href],button,input,textarea,select,summary,[role='button'],[role='link'],[contenteditable='true']";
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
      case "fill": return fill(payload);
      case "scroll": return scroll(payload);
      case "wait": return wait(payload);
      default: fail("unknown_action", "Unsupported browser action " + action);
    }
  }
  globalThis.__viewerManagedBrowser = { execute };
  return globalThis.__viewerManagedBrowser;
})()`

func (p *chromePage) executeRuntime(ctx context.Context, action string, payload map[string]any) (map[string]any, error) {
	payloadJSON, _ := json.Marshal(payload)
	expression := fmt.Sprintf(`Promise.resolve((%s).execute(%s, %s)).then(
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
		point, err := p.executeRuntime(ctx, "prepare_click", payload)
		if err != nil {
			return nil, err
		}
		x, xOK := numberValue(point["x"])
		y, yOK := numberValue(point["y"])
		if !xOK || !yOK {
			return nil, errors.New("click coordinates unavailable")
		}
		for _, eventType := range []string{"mousePressed", "mouseReleased"} {
			if err := p.Input(ctx, map[string]any{"type": eventType, "x": x, "y": y, "button": "left", "buttons": 1, "clickCount": 1}); err != nil {
				return nil, err
			}
		}
		return map[string]any{"clicked": true, "element_id": stringField(point, "element_id")}, nil
	case "snapshot", "fill", "scroll", "wait":
		return p.executeRuntime(ctx, action, payload)
	default:
		return nil, errorf("unknown_action", "unsupported browser action %q", action)
	}
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
			params["clickCount"] = 1
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
