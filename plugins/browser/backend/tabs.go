package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"
)

type chromeTab struct {
	id       string
	targetID string
	conn     *cdpConn
	index    int

	frameMu sync.Mutex
	frames  map[string]*frameContext
}

func (p *chromePage) ensureVisible(ctx context.Context) error {
	if p.activeTabID == "" || p.conn == nil {
		return errorf("active_tab_changed", "The active tab changed or closed; list tabs and choose explicitly")
	}
	raw, err := p.conn.call(ctx, "Runtime.evaluate", map[string]any{"expression": "document.visibilityState", "returnByValue": true}, nil)
	if err != nil {
		return errorf("active_tab_changed", "The active tab is unavailable; list tabs and choose explicitly")
	}
	var response struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Result.Value != "visible" {
		return errorf("active_tab_changed", "The user changed the active tab; do not operate a hidden tab")
	}
	return nil
}

func (p *chromePage) windowID(ctx context.Context, targetID string) (int, error) {
	raw, err := p.manager.browser.call(ctx, "Browser.getWindowForTarget", map[string]any{"targetId": targetID}, nil)
	if err != nil {
		return 0, err
	}
	var response struct {
		WindowID int `json:"windowId"`
	}
	return response.WindowID, json.Unmarshal(raw, &response)
}

func (m *ChromeManager) targetByID(ctx context.Context, targetID string) (*chromeTarget, error) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, m.debugURL+"/json/list", nil)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			var targets []chromeTarget
			err = json.NewDecoder(response.Body).Decode(&targets)
			_ = response.Body.Close()
			for _, target := range targets {
				if target.ID == targetID && target.WebSocketDebuggerURL != "" {
					return &target, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil, errors.New("Chrome tab target did not become available")
}

func (p *chromePage) attachTab(ctx context.Context, targetID string) (*chromeTab, error) {
	target, err := p.manager.targetByID(ctx, targetID)
	if err != nil {
		return nil, err
	}
	conn, err := dialCDP(ctx, target.WebSocketDebuggerURL)
	if err != nil {
		return nil, err
	}
	for _, method := range []string{"Page.enable", "Runtime.enable"} {
		if _, err := conn.call(ctx, method, map[string]any{}, nil); err != nil {
			conn.close()
			return nil, err
		}
	}
	id, err := randomToken("tab_", 8)
	if err != nil {
		conn.close()
		return nil, err
	}
	tab := &chromeTab{id: id, targetID: targetID, conn: conn}
	go p.runTabEvents(tab)
	if _, err := conn.call(ctx, "Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": false, "flatten": true}, nil); err != nil {
		conn.close()
		return nil, err
	}
	return tab, nil
}

func (p *chromePage) activateTab(ctx context.Context, tab *chromeTab) error {
	if _, err := p.manager.browser.call(ctx, "Target.activateTarget", map[string]any{"targetId": tab.targetID}, nil); err != nil {
		return err
	}
	if _, err := tab.conn.call(ctx, "Page.bringToFront", map[string]any{}, nil); err != nil {
		return err
	}
	p.tabMu.Lock()
	p.activeTabID, p.targetID, p.conn = tab.id, tab.targetID, tab.conn
	p.tabMu.Unlock()
	p.invalidateSnapshot()
	return p.ensureVisible(ctx)
}

func (p *chromePage) AcquireControl(ctx context.Context) error {
	p.tabMu.Lock()
	tabs := make([]*chromeTab, 0, len(p.tabs))
	for _, tab := range p.tabs {
		tabs = append(tabs, tab)
	}
	currentID := p.activeTabID
	p.tabMu.Unlock()
	var visible *chromeTab
	for _, tab := range tabs {
		value, err := evaluateOnConn(ctx, tab.conn, "({visible:document.visibilityState===\"visible\"})")
		if err != nil {
			continue
		}
		if visibleState, _ := value["visible"].(bool); visibleState {
			visible = tab
			break
		}
	}
	if visible == nil {
		return errorf("active_tab_changed", "No visible tab is available; ask the user to select a tab in this Page")
	}
	p.tabMu.Lock()
	p.activeTabID, p.targetID, p.conn = visible.id, visible.targetID, visible.conn
	p.tabMu.Unlock()
	if visible.id != currentID {
		p.invalidateSnapshot()
	}
	return nil
}

func (p *chromePage) newTab(ctx context.Context, rawURL string) (map[string]any, error) {
	if rawURL == "" {
		rawURL = "about:blank"
	} else if !validHTTPURL(rawURL) {
		return nil, errorf("invalid_url", "only HTTP(S) tab URLs are allowed")
	}
	windowID, err := p.windowID(ctx, p.targetID)
	if err != nil {
		return nil, err
	}
	raw, err := p.manager.browser.call(ctx, "Target.createTarget", map[string]any{"url": "about:blank", "windowId": windowID}, nil)
	if err != nil {
		return nil, err
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		return nil, err
	}
	tab, err := p.attachTab(ctx, created.TargetID)
	if err != nil {
		return nil, err
	}
	p.tabMu.Lock()
	if p.tabs == nil {
		p.tabs = map[string]*chromeTab{}
	}
	tab.index = len(p.tabs)
	p.tabs[tab.id] = tab
	p.tabMu.Unlock()
	if rawURL != "about:blank" {
		if _, err := tab.conn.call(ctx, "Page.navigate", map[string]any{"url": rawURL}, nil); err != nil {
			return nil, err
		}
	}
	if err := p.activateTab(ctx, tab); err != nil {
		return nil, err
	}
	return p.tabActionState(ctx, "created", tab.id)
}

func (p *chromePage) switchTab(ctx context.Context, tabID string) (map[string]any, error) {
	p.tabMu.Lock()
	tab := p.tabs[tabID]
	p.tabMu.Unlock()
	if tab == nil {
		return nil, errorf("tab_not_found", "Tab does not belong to this Page or has closed")
	}
	if err := p.activateTab(ctx, tab); err != nil {
		return nil, err
	}
	return p.tabActionState(ctx, "switched", tabID)
}

func (p *chromePage) closeTab(ctx context.Context, tabID string) (map[string]any, error) {
	p.tabMu.Lock()
	if tabID == "" {
		tabID = p.activeTabID
	}
	tab := p.tabs[tabID]
	count := len(p.tabs)
	p.tabMu.Unlock()
	if tab == nil {
		return nil, errorf("tab_not_found", "Tab does not belong to this Page or has closed")
	}
	if count <= 1 {
		return nil, errorf("last_tab", "The last tab belongs to the Viewer Page; close the Page explicitly instead")
	}
	if _, err := p.manager.browser.call(ctx, "Target.closeTarget", map[string]any{"targetId": tab.targetID}, nil); err != nil {
		return nil, err
	}
	tab.conn.close()
	p.tabMu.Lock()
	delete(p.tabs, tabID)
	wasActive := p.activeTabID == tabID
	needsNext := wasActive || p.activeTabID == ""
	if wasActive {
		p.activeTabID = ""
	}
	var next *chromeTab
	if needsNext {
		for _, candidate := range p.tabs {
			next = candidate
			break
		}
	}
	p.tabMu.Unlock()
	if next != nil {
		if err := p.activateTab(ctx, next); err != nil {
			return nil, err
		}
	}
	return p.tabActionState(ctx, "closed", tabID)
}

func (p *chromePage) tabActionState(ctx context.Context, action, tabID string) (map[string]any, error) {
	state, err := p.tabState(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"action": action, "tab_id": tabID, "tabs": state["tabs"], "tab_state": state["tab_state"]}, nil
}

func (p *chromePage) tabState(ctx context.Context) (map[string]any, error) {
	p.tabMu.Lock()
	tabs := make([]*chromeTab, 0, len(p.tabs))
	for _, tab := range p.tabs {
		tabs = append(tabs, tab)
	}
	sort.Slice(tabs, func(i, j int) bool { return tabs[i].index < tabs[j].index })
	activeID := p.activeTabID
	p.tabMu.Unlock()
	public := make([]map[string]any, 0, len(tabs))
	stored := make([]storedTab, 0, len(tabs))
	activeIndex := 0
	for index, tab := range tabs {
		url, title := "", ""
		value, err := evaluateOnConn(ctx, tab.conn, "({url:location.href,title:document.title})")
		if err == nil {
			url, title = stringField(value, "url"), stringField(value, "title")
		}
		active := tab.id == activeID
		if active {
			activeIndex = index
		}
		public = append(public, map[string]any{"tab_id": tab.id, "url": url, "title": title, "active": active})
		stored = append(stored, storedTab{URL: url, Title: title})
	}
	return map[string]any{"tabs": public, "tab_state": map[string]any{"tabs": stored, "active_tab": activeIndex}}, nil
}

func evaluateOnConn(ctx context.Context, conn *cdpConn, expression string) (map[string]any, error) {
	raw, err := conn.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true}, nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Result struct {
			Value map[string]any `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if response.Result.Value == nil {
		return nil, errors.New("tab metadata unavailable")
	}
	return response.Result.Value, nil
}

func (p *chromePage) RestoreTabs(ctx context.Context, stored []storedTab, active int) error {
	for _, tab := range stored[1:] {
		if tab.URL == "" || !validHTTPURL(tab.URL) {
			continue
		}
		if _, err := p.newTab(ctx, tab.URL); err != nil {
			return err
		}
	}
	state, err := p.tabState(ctx)
	if err != nil {
		return err
	}
	tabs := state["tabs"].([]map[string]any)
	if active >= 0 && active < len(tabs) {
		_, err = p.switchTab(ctx, stringField(tabs[active], "tab_id"))
	}
	return err
}

func (m *ChromeManager) findTab(targetID string) (*chromePage, *chromeTab) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, page := range m.pages {
		page.tabMu.Lock()
		for _, tab := range page.tabs {
			if tab.targetID == targetID {
				page.tabMu.Unlock()
				return page, tab
			}
		}
		page.tabMu.Unlock()
	}
	return nil, nil
}

func (m *ChromeManager) adoptPopup(ctx context.Context, params map[string]any) {
	info, _ := params["targetInfo"].(map[string]any)
	targetID := stringField(info, "targetId")
	openerID := stringField(info, "openerId")
	if targetID == "" || openerID == "" || stringField(info, "type") != "page" {
		return
	}
	page, opener := m.findTab(openerID)
	if page == nil || opener == nil {
		return
	}
	if existing, _ := m.findTab(targetID); existing != nil {
		return
	}
	openerWindow, err := page.windowID(ctx, openerID)
	if err != nil {
		return
	}
	popupWindow, err := page.windowID(ctx, targetID)
	if err != nil {
		return
	}
	if openerWindow != popupWindow {
		page.setHumanRequired("popup", "A separate popup window requires user handling")
		return
	}
	tab, err := page.attachTab(ctx, targetID)
	if err != nil {
		page.setHumanRequired("popup", "A popup could not be attached to the visible Page")
		return
	}
	page.tabMu.Lock()
	tab.index = len(page.tabs)
	page.tabs[tab.id] = tab
	page.tabMu.Unlock()
	if err := page.activateTab(ctx, tab); err != nil {
		page.setHumanRequired("popup", "A popup is not visible in the managed Page")
	}
}

func (m *ChromeManager) removeTarget(targetID string) {
	page, tab := m.findTab(targetID)
	if page == nil || tab == nil {
		return
	}
	page.tabMu.Lock()
	delete(page.tabs, tab.id)
	if page.activeTabID == tab.id {
		page.activeTabID = ""
	}
	page.tabMu.Unlock()
	tab.conn.close()
}

func (p *chromePage) HumanRequired() map[string]any {
	p.tabMu.Lock()
	defer p.tabMu.Unlock()
	if p.humanRequired == nil {
		return nil
	}
	return map[string]any{"kind": stringField(p.humanRequired, "kind"), "detail": stringField(p.humanRequired, "detail")}
}

func (p *chromePage) humanRequiredError() error {
	state := p.HumanRequired()
	if state == nil {
		return nil
	}
	detail := stringField(state, "detail")
	if detail == "" {
		detail = "Browser requires user handling before automation can continue"
	}
	return errorf("human_required", "%s", detail)
}

func (p *chromePage) humanActionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	p.tabMu.Lock()
	if p.humanSignal == nil {
		p.humanSignal = make(chan struct{})
	}
	signal := p.humanSignal
	p.tabMu.Unlock()
	actionCtx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-signal:
			cancel()
		case <-actionCtx.Done():
		}
	}()
	return actionCtx, cancel
}

func (p *chromePage) setHumanRequired(kind, detail string) {
	p.tabMu.Lock()
	p.humanRequired = map[string]any{"kind": kind, "detail": detail}
	if p.humanSignal == nil {
		p.humanSignal = make(chan struct{})
	}
	select {
	case <-p.humanSignal:
	default:
		close(p.humanSignal)
	}
	p.tabMu.Unlock()
}

func (p *chromePage) clearHumanRequired(kind string) {
	p.tabMu.Lock()
	if stringField(p.humanRequired, "kind") == kind {
		p.humanRequired = nil
		p.humanSignal = make(chan struct{})
	}
	p.tabMu.Unlock()
}
