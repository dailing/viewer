package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type frameContext struct {
	FrameID       string
	ParentFrameID string
	URL           string
	SessionID     string
	ContextID     int64
}

type pageSnapshot struct {
	ID       string
	TabID    string
	Elements map[string]string
	Contexts map[string]int64
}

func (p *chromePage) activeTab() (*chromeTab, string, error) {
	p.tabMu.Lock()
	defer p.tabMu.Unlock()
	tab := p.tabs[p.activeTabID]
	if tab == nil {
		return nil, "", errorf("active_tab_changed", "The active tab changed or closed; list tabs and choose explicitly")
	}
	return tab, p.activeTabID, nil
}

func (p *chromePage) invalidateSnapshot() {
	p.snapshotMu.Lock()
	p.currentSnapshot = nil
	p.snapshotMu.Unlock()
}

func (p *chromePage) runTabEvents(tab *chromeTab) {
	for {
		event, err := tab.conn.nextEvent(context.Background())
		if err != nil {
			return
		}
		var params map[string]any
		if json.Unmarshal(event.Params, &params) != nil {
			continue
		}
		switch event.Method {
		case "Runtime.executionContextCreated":
			contextValue, _ := params["context"].(map[string]any)
			aux, _ := contextValue["auxData"].(map[string]any)
			frameID := stringField(aux, "frameId")
			if frameID == "" || aux["isDefault"] != true {
				continue
			}
			tab.frameMu.Lock()
			if tab.frames == nil {
				tab.frames = make(map[string]*frameContext)
			}
			frame := tab.frames[frameID]
			if frame == nil {
				frame = &frameContext{FrameID: frameID}
				tab.frames[frameID] = frame
			}
			frame.ContextID = int64(numberField(contextValue, "id"))
			frame.SessionID = event.SessionID
			tab.frameMu.Unlock()
		case "Runtime.executionContextDestroyed":
			contextID := int64(numberField(params, "executionContextId"))
			tab.frameMu.Lock()
			for id, frame := range tab.frames {
				if frame.ContextID == contextID && frame.SessionID == event.SessionID {
					frame.ContextID = 0
					_ = id
				}
			}
			tab.frameMu.Unlock()
			p.invalidateSnapshot()
		case "Runtime.executionContextsCleared":
			tab.frameMu.Lock()
			for _, frame := range tab.frames {
				if frame.SessionID == event.SessionID {
					frame.ContextID = 0
				}
			}
			tab.frameMu.Unlock()
			p.invalidateSnapshot()
		case "Target.attachedToTarget":
			sessionID := stringField(params, "sessionId")
			if sessionID == "" {
				continue
			}
			for _, method := range []string{"Page.enable", "Runtime.enable", "Target.setAutoAttach"} {
				arguments := map[string]any{}
				if method == "Target.setAutoAttach" {
					arguments = map[string]any{"autoAttach": true, "waitForDebuggerOnStart": false, "flatten": true}
				}
				callCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_, _ = tab.conn.callSession(callCtx, sessionID, method, arguments)
				cancel()
			}
		case "Target.detachedFromTarget":
			sessionID := stringField(params, "sessionId")
			tab.frameMu.Lock()
			for id, frame := range tab.frames {
				if frame.SessionID == sessionID {
					delete(tab.frames, id)
				}
			}
			tab.frameMu.Unlock()
			p.invalidateSnapshot()
		case "Page.frameNavigated", "Page.navigatedWithinDocument", "Page.frameDetached":
			p.invalidateSnapshot()
		case "Page.javascriptDialogOpening":
			p.setHumanRequired("dialog", stringField(params, "message"))
		case "Page.javascriptDialogClosed":
			p.clearHumanRequired("dialog")
		}
	}
}

func (p *chromePage) refreshFrames(ctx context.Context, tab *chromeTab) ([]string, error) {
	raw, err := tab.conn.call(ctx, "Page.getFrameTree", map[string]any{}, nil)
	if err != nil {
		return nil, err
	}
	type frameNode struct {
		Frame struct {
			ID       string `json:"id"`
			ParentID string `json:"parentId"`
			URL      string `json:"url"`
		} `json:"frame"`
		ChildFrames []*frameNode `json:"childFrames"`
	}
	var response struct {
		FrameTree *frameNode `json:"frameTree"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || response.FrameTree == nil {
		return nil, errors.New("frame tree unavailable")
	}
	ordered := make([]string, 0)
	seen := make(map[string]bool)
	var visit func(*frameNode)
	visit = func(node *frameNode) {
		id := node.Frame.ID
		if id == "" {
			return
		}
		ordered = append(ordered, id)
		seen[id] = true
		tab.frameMu.Lock()
		if tab.frames == nil {
			tab.frames = make(map[string]*frameContext)
		}
		frame := tab.frames[id]
		if frame == nil {
			frame = &frameContext{FrameID: id}
			tab.frames[id] = frame
		}
		frame.ParentFrameID, frame.URL = node.Frame.ParentID, node.Frame.URL
		tab.frameMu.Unlock()
		for _, child := range node.ChildFrames {
			visit(child)
		}
	}
	visit(response.FrameTree)
	tab.frameMu.Lock()
	for id := range tab.frames {
		if !seen[id] {
			delete(tab.frames, id)
		}
	}
	tab.frameMu.Unlock()
	return ordered, nil
}

func (p *chromePage) awaitFrames(ctx context.Context, tab *chromeTab) ([]string, error) {
	deadline := time.Now().Add(3 * time.Second)
	for {
		ordered, err := p.refreshFrames(ctx, tab)
		if err != nil {
			return nil, err
		}
		ready := true
		tab.frameMu.Lock()
		for _, id := range ordered {
			if tab.frames[id] == nil || tab.frames[id].ContextID == 0 {
				ready = false
				break
			}
		}
		tab.frameMu.Unlock()
		if ready || time.Now().After(deadline) {
			return ordered, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (p *chromePage) frameByID(tab *chromeTab, frameID string) (*frameContext, error) {
	tab.frameMu.Lock()
	defer tab.frameMu.Unlock()
	if frameID == "" {
		for _, frame := range tab.frames {
			if frame.ParentFrameID == "" && frame.ContextID != 0 {
				copy := *frame
				return &copy, nil
			}
		}
		return nil, errorf("stale_frame", "Main frame execution context is unavailable")
	}
	frame := tab.frames[frameID]
	if frame == nil || frame.ContextID == 0 {
		return nil, errorf("stale_frame", "Frame closed or navigated; take a fresh snapshot")
	}
	copy := *frame
	return &copy, nil
}

func evaluateMapInFrame(ctx context.Context, tab *chromeTab, frame *frameContext, expression string) (map[string]any, error) {
	raw, err := tab.conn.callSession(ctx, frame.SessionID, "Runtime.evaluate", map[string]any{
		"expression": expression, "contextId": frame.ContextID, "returnByValue": true, "awaitPromise": true,
	})
	if err != nil {
		return nil, err
	}
	var response struct {
		Result struct {
			Value map[string]any `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if response.ExceptionDetails != nil {
		if description := response.ExceptionDetails.Exception.Description; description != "" {
			return nil, errors.New(description)
		}
		return nil, errors.New(response.ExceptionDetails.Text)
	}
	if response.Result.Value == nil {
		return nil, errors.New("frame evaluation returned no object")
	}
	return response.Result.Value, nil
}

func (p *chromePage) executeRuntimeInFrame(ctx context.Context, frameID, action string, payload map[string]any) (map[string]any, error) {
	tab, _, err := p.activeTab()
	if err != nil {
		return nil, err
	}
	if frameID == "" {
		if _, err := p.refreshFrames(ctx, tab); err != nil {
			return nil, err
		}
	}
	frame, err := p.frameByID(tab, frameID)
	if err != nil {
		return nil, err
	}
	payloadJSON, _ := json.Marshal(payload)
	expression := fmt.Sprintf(`Promise.resolve().then(() => (%s).execute(%s, %s)).then(
      result => ({ok:true,result}),
      error => ({ok:false,error:{code:error.code || "page_action_failed",message:String(error.message || error)}})
    )`, browserRuntimeSource, strconvQuote(action), string(payloadJSON))
	response, err := evaluateMapInFrame(ctx, tab, frame, expression)
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

func (p *chromePage) snapshotElement(payload map[string]any) (*chromeTab, string, error) {
	snapshotID, elementID := stringField(payload, "snapshot_id"), stringField(payload, "element_id")
	p.snapshotMu.Lock()
	snapshot := p.currentSnapshot
	p.snapshotMu.Unlock()
	if snapshot == nil || snapshotID == "" || snapshot.ID != snapshotID {
		return nil, "", errorf("stale_snapshot", "Snapshot changed; call browser_snapshot again")
	}
	_, activeID, err := p.activeTab()
	if err != nil {
		return nil, "", err
	}
	if snapshot.TabID != activeID {
		return nil, "", errorf("stale_snapshot", "Active tab changed; call browser_snapshot again")
	}
	frameID := snapshot.Elements[elementID]
	if frameID == "" {
		return nil, "", errorf("stale_element", "Element is not present in this snapshot")
	}
	tab, _, _ := p.activeTab()
	frame, err := p.frameByID(tab, frameID)
	if err != nil {
		return nil, "", err
	}
	if snapshot.Contexts[frameID] != frame.ContextID {
		return nil, "", errorf("stale_frame", "Frame navigated; call browser_snapshot again")
	}
	return tab, frameID, nil
}

func (p *chromePage) executeRuntimeForElement(ctx context.Context, action string, payload map[string]any) (map[string]any, error) {
	_, frameID, err := p.snapshotElement(payload)
	if err != nil {
		return nil, err
	}
	result, err := p.executeRuntimeInFrame(ctx, frameID, action, payload)
	if result != nil {
		result["frame_id"] = frameID
	}
	return result, err
}

func (p *chromePage) snapshot(ctx context.Context, payload map[string]any) (map[string]any, error) {
	tab, tabID, err := p.activeTab()
	if err != nil {
		return nil, err
	}
	ordered, err := p.awaitFrames(ctx, tab)
	if err != nil {
		return nil, err
	}
	snapshotID, err := randomToken("snap_", 16)
	if err != nil {
		return nil, err
	}
	maxChars := intField(payload, "max_chars")
	if maxChars == 0 {
		maxChars = 20000
	}
	maxElements := intField(payload, "max_elements")
	if maxElements == 0 {
		maxElements = 200
	}
	maxChars = max(1000, min(50000, maxChars))
	maxElements = max(10, min(500, maxElements))
	binding := &pageSnapshot{ID: snapshotID, TabID: tabID, Elements: make(map[string]string), Contexts: make(map[string]int64)}
	result := map[string]any{"snapshot_id": snapshotID, "tab_id": tabID, "elements": []any{}, "frames": []map[string]any{}, "frame_warnings": []string{}}
	textParts := make([]string, 0, len(ordered))
	elements := make([]any, 0)
	framesMetadata := make([]map[string]any, 0, len(ordered))
	warnings := make([]string, 0)
	textTruncated, elementsTruncated := false, false
	nextIndex := 0
	for index, frameID := range ordered {
		frame, frameErr := p.frameByID(tab, frameID)
		if frameErr != nil {
			warnings = append(warnings, "frame "+frameID+" has no active execution context")
			continue
		}
		framesMetadata = append(framesMetadata, map[string]any{"frame_id": frame.FrameID, "parent_frame_id": frame.ParentFrameID, "url": frame.URL})
		binding.Contexts[frameID] = frame.ContextID
		if len(elements) >= maxElements || len(strings.Join(textParts, "\n")) >= maxChars {
			continue
		}
		local := map[string]any{"snapshot_id": snapshotID, "start_index": nextIndex, "max_elements": maxElements - len(elements), "max_chars": maxChars - len(strings.Join(textParts, "\n"))}
		frameResult, executeErr := p.executeRuntimeInFrame(ctx, frameID, "snapshot", local)
		if executeErr != nil {
			warnings = append(warnings, "frame "+frameID+": "+executeErr.Error())
			continue
		}
		if index == 0 {
			result["url"], result["title"], result["revision"] = frameResult["url"], frameResult["title"], frameResult["revision"]
		}
		if text := stringField(frameResult, "text"); text != "" {
			textParts = append(textParts, text)
		}
		textTruncated = textTruncated || frameResult["text_truncated"] == true
		elementsTruncated = elementsTruncated || frameResult["elements_truncated"] == true
		nextIndex = intField(frameResult, "next_index")
		frameElements, _ := frameResult["elements"].([]any)
		for _, rawElement := range frameElements {
			element, _ := rawElement.(map[string]any)
			elementID := stringField(element, "id")
			if elementID == "" {
				continue
			}
			element["frame_id"], element["frame_url"] = frameID, frame.URL
			binding.Elements[elementID] = frameID
			elements = append(elements, element)
		}
	}
	result["text"] = strings.Join(textParts, "\n")
	result["text_truncated"] = textTruncated || len(result["text"].(string)) > maxChars
	result["elements"], result["elements_truncated"] = elements, elementsTruncated || len(elements) >= maxElements
	result["frames"], result["frame_warnings"] = framesMetadata, warnings
	p.snapshotMu.Lock()
	p.currentSnapshot = binding
	p.snapshotMu.Unlock()
	return result, nil
}

func (p *chromePage) evaluateAnyFrame(ctx context.Context, frameID, expression string) (any, string, error) {
	if strings.TrimSpace(expression) == "" {
		return nil, "", errors.New("expression is required")
	}
	tab, _, err := p.activeTab()
	if err != nil {
		return nil, "", err
	}
	if frameID == "" {
		if _, err := p.refreshFrames(ctx, tab); err != nil {
			return nil, "", err
		}
	}
	frame, err := p.frameByID(tab, frameID)
	if err != nil {
		return nil, "", err
	}
	raw, err := tab.conn.callSession(ctx, frame.SessionID, "Runtime.evaluate", map[string]any{
		"expression": expression, "contextId": frame.ContextID, "returnByValue": true, "awaitPromise": true,
	})
	if err != nil {
		return nil, "", err
	}
	var response struct {
		Result struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, "", err
	}
	if response.ExceptionDetails != nil {
		if description := response.ExceptionDetails.Exception.Description; description != "" {
			return nil, "", errors.New(description)
		}
		return nil, "", errors.New(response.ExceptionDetails.Text)
	}
	if len(response.Result.Value) == 0 {
		if response.Result.Type == "undefined" {
			return nil, "undefined", nil
		}
		return nil, "", errors.New("JavaScript result is not JSON serializable")
	}
	var value any
	if err := json.Unmarshal(response.Result.Value, &value); err != nil {
		return nil, "", fmt.Errorf("decode JavaScript result: %w", err)
	}
	return value, response.Result.Type, nil
}

func (p *chromePage) translatePoint(ctx context.Context, tab *chromeTab, frameID string, x, y float64) (float64, float64, error) {
	frame, err := p.frameByID(tab, frameID)
	if err != nil {
		return 0, 0, err
	}
	if frame.ParentFrameID == "" {
		return x, y, nil
	}
	parent, err := p.frameByID(tab, frame.ParentFrameID)
	if err != nil {
		return 0, 0, err
	}
	raw, err := tab.conn.callSession(ctx, parent.SessionID, "DOM.getFrameOwner", map[string]any{"frameId": frameID})
	if err != nil {
		return 0, 0, errorf("stale_frame", "Iframe owner is unavailable; take a fresh snapshot")
	}
	var owner struct {
		BackendNodeID int `json:"backendNodeId"`
	}
	if json.Unmarshal(raw, &owner) != nil || owner.BackendNodeID == 0 {
		return 0, 0, errorf("stale_frame", "Iframe owner is unavailable")
	}
	raw, err = tab.conn.callSession(ctx, parent.SessionID, "DOM.resolveNode", map[string]any{"backendNodeId": owner.BackendNodeID, "executionContextId": parent.ContextID})
	if err != nil {
		return 0, 0, err
	}
	var resolved struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	if json.Unmarshal(raw, &resolved) != nil || resolved.Object.ObjectID == "" {
		return 0, 0, errorf("stale_frame", "Iframe owner could not be resolved")
	}
	raw, err = tab.conn.callSession(ctx, parent.SessionID, "Runtime.callFunctionOn", map[string]any{
		"objectId": resolved.Object.ObjectID, "returnByValue": true,
		"functionDeclaration": `function(){const r=this.getBoundingClientRect();return {x:r.left+this.clientLeft,y:r.top+this.clientTop}}`,
	})
	_, _ = tab.conn.callSession(ctx, parent.SessionID, "Runtime.releaseObject", map[string]any{"objectId": resolved.Object.ObjectID})
	if err != nil {
		return 0, 0, err
	}
	var rect struct {
		Result struct {
			Value struct {
				X float64 `json:"x"`
				Y float64 `json:"y"`
			} `json:"value"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &rect) != nil {
		return 0, 0, errors.New("iframe coordinates unavailable")
	}
	return p.translatePoint(ctx, tab, parent.FrameID, x+rect.Result.Value.X, y+rect.Result.Value.Y)
}
