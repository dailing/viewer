package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ChromeConfig struct {
	ChromePath string
	XvfbPath   string
	ProfileDir string
}

type chromeTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	Title                string `json:"title"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type ChromeManager struct {
	config ChromeConfig

	mu            sync.Mutex
	display       string
	xvfb          *exec.Cmd
	chrome        *exec.Cmd
	chromeDone    chan struct{}
	browser       *cdpConn
	debugURL      string
	nextSlot      int
	pages         map[string]*chromePage
	bootstrapBase string
	bootstrapHTTP *http.Server
	closed        bool
}

func NewChromeManager(config ChromeConfig) (*ChromeManager, error) {
	if config.ProfileDir == "" {
		return nil, errors.New("Chrome profile directory is required")
	}
	if config.XvfbPath == "" {
		config.XvfbPath = "Xvfb"
	}
	if _, err := exec.LookPath(config.XvfbPath); err != nil {
		return nil, fmt.Errorf("Xvfb is required for raw browser video: %w", err)
	}
	path, err := findChrome(config.ChromePath)
	if err != nil {
		return nil, err
	}
	config.ChromePath = path
	if err := os.MkdirAll(config.ProfileDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(config.ProfileDir, "downloads"), 0o700); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for Chrome bootstrap pages: %w", err)
	}
	manager := &ChromeManager{config: config, pages: make(map[string]*chromePage), bootstrapBase: "http://" + listener.Addr().String()}
	mux := http.NewServeMux()
	mux.HandleFunc("/new/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "<!doctype html><meta name=viewport content='width=device-width,initial-scale=1'><title>Viewer Browser</title>")
	})
	manager.bootstrapHTTP = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = manager.bootstrapHTTP.Serve(listener) }()
	return manager, nil
}

func findChrome(configured string) (string, error) {
	if configured != "" {
		return exec.LookPath(configured)
	}
	if fromEnv := os.Getenv("CHROME_BIN"); fromEnv != "" {
		return exec.LookPath(fromEnv)
	}
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser", "chrome"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	home, _ := os.UserHomeDir()
	matches, _ := filepath.Glob(filepath.Join(home, ".cache", "ms-playwright", "chromium-*", "chrome-linux64", "chrome"))
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	for _, path := range matches {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, nil
		}
	}
	return "", errors.New("Chrome not found; set --chrome-path or CHROME_BIN")
}

func (m *ChromeManager) OpenPage(ctx context.Context, pageID, rawURL string, viewport Viewport) (PageDriver, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("Chrome manager is closed")
	}
	started, err := m.ensureBrowserLocked(ctx, pageID)
	if err != nil {
		return nil, err
	}
	bootstrap := m.bootstrapURL(pageID)
	if !started {
		command := exec.CommandContext(ctx, m.config.ChromePath, m.chromeArgs(false, pageID)...)
		command.Env = append(os.Environ(), "DISPLAY="+m.display)
		if output, err := command.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("open Chrome app window: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}
	target, err := m.waitTargetLocked(ctx, bootstrap)
	if err != nil {
		return nil, err
	}
	conn, err := dialCDP(ctx, target.WebSocketDebuggerURL)
	if err != nil {
		return nil, err
	}
	page := &chromePage{manager: m, pageID: pageID, targetID: target.ID, conn: conn, slot: m.nextSlot % 15}
	m.nextSlot++
	m.pages[pageID] = page
	for _, method := range []string{"Page.enable", "Runtime.enable"} {
		if _, err := conn.call(ctx, method, map[string]any{}, nil); err != nil {
			conn.close()
			delete(m.pages, pageID)
			return nil, err
		}
	}
	readyDeadline := time.Now().Add(10 * time.Second)
	for {
		ready, evalErr := page.evaluate(ctx, "({url:location.href,ready:document.readyState})")
		if evalErr == nil && stringField(ready, "url") == bootstrap && stringField(ready, "ready") == "complete" {
			break
		}
		if time.Now().After(readyDeadline) {
			conn.close()
			delete(m.pages, pageID)
			return nil, errors.New("Chrome bootstrap document did not finish loading")
		}
		time.Sleep(100 * time.Millisecond)
	}
	expression := fmt.Sprintf("document.title = %s", strconv.Quote("Viewer "+pageID))
	if _, err := conn.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression}, nil); err != nil {
		conn.close()
		delete(m.pages, pageID)
		return nil, err
	}
	xid, err := m.waitXWindowLocked(ctx, pageID)
	if err != nil {
		conn.close()
		delete(m.pages, pageID)
		return nil, err
	}
	page.xid = xid
	if err := page.SetViewport(ctx, viewport); err != nil {
		conn.close()
		delete(m.pages, pageID)
		return nil, err
	}
	if rawURL == "" {
		rawURL = "about:blank"
	}
	if rawURL != bootstrap {
		if err := page.Navigate(ctx, rawURL); err != nil {
			conn.close()
			delete(m.pages, pageID)
			return nil, err
		}
	}
	return page, nil
}

func (m *ChromeManager) bootstrapURL(pageID string) string {
	return m.bootstrapBase + "/new/" + pageID
}

func (m *ChromeManager) chromeArgs(withDebug bool, pageID string) []string {
	args := []string{
		"--user-data-dir=" + m.config.ProfileDir,
		"--no-first-run", "--no-default-browser-check", "--disable-dev-shm-usage",
		"--disable-session-crashed-bubble", "--hide-crash-restore-bubble",
		"--disable-infobars", "--test-type",
	}
	if withDebug {
		args = append(args, "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--remote-allow-origins=*")
	}
	return append(args, "--app="+m.bootstrapURL(pageID))
}

func (m *ChromeManager) ensureBrowserLocked(ctx context.Context, pageID string) (bool, error) {
	if m.chrome != nil && m.browser != nil {
		select {
		case <-m.chromeDone:
		default:
			return false, nil
		}
	}
	if m.display == "" {
		if err := m.startXvfbLocked(ctx); err != nil {
			return false, err
		}
	}
	command := exec.Command(m.config.ChromePath, m.chromeArgs(true, pageID)...)
	command.Env = append(os.Environ(), "DISPLAY="+m.display)
	logFile, err := os.OpenFile(filepath.Join(m.config.ProfileDir, "chrome.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return false, err
	}
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return false, err
	}
	_ = logFile.Close()
	m.chrome = command
	m.chromeDone = make(chan struct{})
	done := m.chromeDone
	go func() {
		_ = command.Wait()
		m.mu.Lock()
		if m.chrome == command {
			m.chrome = nil
			if m.browser != nil {
				m.browser.close()
				m.browser = nil
			}
		}
		close(done)
		m.mu.Unlock()
	}()
	if err := m.connectBrowserLocked(ctx); err != nil {
		_ = command.Process.Kill()
		return false, err
	}
	_, _ = m.browser.call(ctx, "Browser.setDownloadBehavior", map[string]any{"behavior": "allow", "downloadPath": filepath.Join(m.config.ProfileDir, "downloads")}, nil)
	return true, nil
}

func (m *ChromeManager) startXvfbLocked(ctx context.Context) error {
	read, write, err := os.Pipe()
	if err != nil {
		return err
	}
	command := exec.Command(m.config.XvfbPath, "-displayfd", "3", "-screen", "0", "8192x8192x24", "-nolisten", "tcp")
	command.ExtraFiles = []*os.File{write}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		read.Close()
		write.Close()
		return err
	}
	_ = write.Close()
	type displayResult struct {
		value string
		err   error
	}
	result := make(chan displayResult, 1)
	go func() {
		line, err := bufio.NewReader(read).ReadString('\n')
		_ = read.Close()
		result <- displayResult{strings.TrimSpace(line), err}
	}()
	select {
	case outcome := <-result:
		if outcome.err != nil || outcome.value == "" {
			_ = command.Process.Kill()
			return fmt.Errorf("read Xvfb display: %w", outcome.err)
		}
		m.display = ":" + outcome.value
		m.xvfb = command
		return nil
	case <-ctx.Done():
		_ = command.Process.Kill()
		return ctx.Err()
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		return errors.New("Xvfb display allocation timed out")
	}
}

func (m *ChromeManager) connectBrowserLocked(ctx context.Context) error {
	portFile := filepath.Join(m.config.ProfileDir, "DevToolsActivePort")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(portFile)
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) >= 2 {
				base := "http://127.0.0.1:" + lines[0]
				var version struct {
					WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
				}
				request, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/json/version", nil)
				response, err := http.DefaultClient.Do(request)
				if err == nil {
					err = json.NewDecoder(response.Body).Decode(&version)
					_ = response.Body.Close()
				}
				if err == nil && version.WebSocketDebuggerURL != "" {
					conn, err := dialCDP(ctx, version.WebSocketDebuggerURL)
					if err != nil {
						return err
					}
					m.browser = conn
					m.debugURL = base
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return errors.New("Chrome DevTools endpoint did not become ready")
}

func (m *ChromeManager) waitTargetLocked(ctx context.Context, rawURL string) (*chromeTarget, error) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, m.debugURL+"/json/list", nil)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			var targets []chromeTarget
			err = json.NewDecoder(response.Body).Decode(&targets)
			_ = response.Body.Close()
			for _, target := range targets {
				if target.Type == "page" && target.URL == rawURL && target.WebSocketDebuggerURL != "" {
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
	return nil, fmt.Errorf("Chrome page target %s did not appear", rawURL)
}

func (m *ChromeManager) waitXWindowLocked(ctx context.Context, pageID string) (uint32, error) {
	pattern := regexp.MustCompile(`^\s*(0x[0-9a-fA-F]+)\s+"Viewer ` + regexp.QuoteMeta(pageID) + `"`)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		command := exec.CommandContext(ctx, "xwininfo", "-root", "-tree")
		command.Env = append(os.Environ(), "DISPLAY="+m.display)
		output, err := command.Output()
		if err == nil {
			for _, line := range strings.Split(string(output), "\n") {
				match := pattern.FindStringSubmatch(line)
				if len(match) == 2 {
					value, err := strconv.ParseUint(strings.TrimPrefix(match[1], "0x"), 16, 32)
					if err == nil {
						return uint32(value), nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return 0, errors.New("Chrome X11 window did not appear")
}

func (m *ChromeManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	if m.bootstrapHTTP != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = m.bootstrapHTTP.Shutdown(ctx)
		cancel()
	}
	for _, page := range m.pages {
		page.conn.close()
	}
	m.pages = make(map[string]*chromePage)
	if m.browser != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = m.browser.call(ctx, "Browser.close", map[string]any{}, nil)
		cancel()
		m.browser.close()
	}
	if m.chrome != nil {
		_ = m.chrome.Process.Kill()
	}
	if m.xvfb != nil {
		_ = m.xvfb.Process.Kill()
		_, _ = m.xvfb.Process.Wait()
	}
	m.mu.Unlock()
}

type chromePage struct {
	manager  *ChromeManager
	pageID   string
	targetID string
	xid      uint32
	slot     int
	conn     *cdpConn
}

func (p *chromePage) StreamTarget() (string, uint32) {
	return p.manager.display, p.xid
}

func (p *chromePage) SetViewport(ctx context.Context, viewport Viewport) error {
	window, err := p.manager.browser.call(ctx, "Browser.getWindowForTarget", map[string]any{"targetId": p.targetID}, nil)
	if err != nil {
		return err
	}
	var bounds struct {
		WindowID int `json:"windowId"`
	}
	if err := json.Unmarshal(window, &bounds); err != nil {
		return err
	}
	left := (p.slot % 3) * 2600
	top := (p.slot / 3) * 1500
	if _, err := p.manager.browser.call(ctx, "Browser.setWindowBounds", map[string]any{"windowId": bounds.WindowID, "bounds": map[string]any{"windowState": "normal"}}, nil); err != nil {
		return err
	}
	if _, err := p.manager.browser.call(ctx, "Browser.setWindowBounds", map[string]any{"windowId": bounds.WindowID, "bounds": map[string]any{"left": left, "top": top, "width": viewport.PixelWidth, "height": viewport.PixelHeight}}, nil); err != nil {
		return err
	}
	_, err = p.conn.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width": viewport.CSSWidth, "height": viewport.CSSHeight,
		"deviceScaleFactor": viewport.RenderScale, "mobile": false,
		"screenWidth": viewport.CSSWidth, "screenHeight": viewport.CSSHeight,
	}, nil)
	return err
}

func (p *chromePage) Navigate(ctx context.Context, rawURL string) error {
	_, err := p.conn.call(ctx, "Page.navigate", map[string]any{"url": rawURL}, nil)
	return err
}

func (p *chromePage) Metadata(ctx context.Context) (string, string, error) {
	value, err := p.evaluate(ctx, "({url:location.href,title:document.title})")
	if err != nil {
		return "", "", err
	}
	return stringField(value, "url"), stringField(value, "title"), nil
}

func (p *chromePage) Close(ctx context.Context) error {
	_, err := p.manager.browser.call(ctx, "Target.closeTarget", map[string]any{"targetId": p.targetID}, nil)
	p.conn.close()
	p.manager.mu.Lock()
	delete(p.manager.pages, p.pageID)
	p.manager.mu.Unlock()
	return err
}

func (p *chromePage) evaluate(ctx context.Context, expression string) (map[string]any, error) {
	raw, err := p.conn.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true}, nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Result struct {
			Value map[string]any `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if response.ExceptionDetails != nil {
		return nil, errors.New(response.ExceptionDetails.Text)
	}
	return response.Result.Value, nil
}
