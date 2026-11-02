package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	stateChannel      = "viewer.browser:_:state"
	defaultLeaseTTL   = 15 * time.Minute
	humanLeaseTTL     = 45 * time.Second
	watcherTTL        = 45 * time.Second
	maxManagedPages   = 9
	defaultPageZoom   = 1.0
	defaultFPS        = 15
	defaultBitrate    = 4000
	defaultPageWidth  = 1280
	defaultPageHeight = 720
)

type Bus interface {
	Publish(context.Context, string, any) error
	Set(context.Context, string, any) error
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

type VideoSettings struct {
	FPS         int `json:"fps"`
	BitrateKbps int `json:"bitrate_kbps"`
}

type Viewport struct {
	CSSWidth    int     `json:"css_width"`
	CSSHeight   int     `json:"css_height"`
	PixelWidth  int     `json:"pixel_width"`
	PixelHeight int     `json:"pixel_height"`
	PageZoom    float64 `json:"page_zoom"`
	RenderScale float64 `json:"render_scale"`
}

type Lease struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	ViewerID  string    `json:"viewer_id,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Watcher struct {
	ID       string    `json:"id"`
	LastSeen time.Time `json:"last_seen"`
}

type PageDriver interface {
	Execute(context.Context, string, map[string]any) (any, error)
	Input(context.Context, map[string]any) error
	SetViewport(context.Context, Viewport) error
	Navigate(context.Context, string) error
	Metadata(context.Context) (string, string, error)
	StreamTarget() (string, uint32)
	Close(context.Context) error
}

type PageBackend interface {
	OpenPage(context.Context, string, string, Viewport) (PageDriver, error)
}

type Page struct {
	ID            string              `json:"page_id"`
	URL           string              `json:"url"`
	Title         string              `json:"title"`
	Viewport      Viewport            `json:"viewport"`
	Video         VideoSettings       `json:"video"`
	Online        bool                `json:"online"`
	Generation    string              `json:"generation"`
	LastError     string              `json:"last_error,omitempty"`
	Lease         *Lease              `json:"lease,omitempty"`
	Watchers      map[string]*Watcher `json:"-"`
	ViewportOwner string              `json:"-"`

	driver PageDriver
	stream StreamController
	opMu   sync.Mutex
}

type storedPage struct {
	PageID   string        `json:"page_id"`
	URL      string        `json:"url"`
	Title    string        `json:"title"`
	Viewport Viewport      `json:"viewport"`
	Video    VideoSettings `json:"video"`
}

type storedState struct {
	Pages []storedPage `json:"pages"`
}

type Service struct {
	bus           Bus
	stateFile     string
	backend       PageBackend
	streamFactory func(string) StreamController
	now           func() time.Time
	leaseTTL      time.Duration

	mu             sync.Mutex
	pages          map[string]*Page
	closed         bool
	statePublishMu sync.Mutex
}

func NewService(bus Bus, stateFile string, backend PageBackend, streamFactory func(string) StreamController) (*Service, error) {
	service := &Service{bus: bus, stateFile: stateFile, backend: backend, streamFactory: streamFactory, now: time.Now, leaseTTL: defaultLeaseTTL, pages: make(map[string]*Page)}
	if err := service.load(); err != nil {
		return nil, err
	}
	return service, nil
}

func defaultViewport() Viewport {
	return makeViewport(defaultPageWidth, defaultPageHeight, defaultPageZoom, 1)
}

func makeViewport(paneWidth, paneHeight int, zoom, renderScale float64) Viewport {
	if paneWidth <= 0 {
		paneWidth = defaultPageWidth
	}
	if paneHeight <= 0 {
		paneHeight = defaultPageHeight
	}
	zoom = clamp(zoom, 0.25, 4, defaultPageZoom)
	renderScale = clamp(renderScale, 0.25, 3, 1)
	cssWidth := clampInt(int(math.Round(float64(paneWidth)/zoom)), 100, 4096)
	cssHeight := clampInt(int(math.Round(float64(paneHeight)/zoom)), 100, 4096)
	pixelWidth := even(clampInt(int(math.Round(float64(cssWidth)*renderScale)), 64, 2560))
	pixelHeight := even(clampInt(int(math.Round(float64(cssHeight)*renderScale)), 64, 2560))
	return Viewport{CSSWidth: cssWidth, CSSHeight: cssHeight, PixelWidth: pixelWidth, PixelHeight: pixelHeight, PageZoom: zoom, RenderScale: renderScale}
}

func normalizeVideo(video VideoSettings) VideoSettings {
	if video.FPS <= 0 {
		video.FPS = defaultFPS
	}
	if video.BitrateKbps <= 0 {
		video.BitrateKbps = defaultBitrate
	}
	video.FPS = clampInt(video.FPS, 1, 60)
	video.BitrateKbps = clampInt(video.BitrateKbps, 100, 50000)
	return video
}

func clamp(value, min, max, fallback float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value == 0 {
		return fallback
	}
	return math.Max(min, math.Min(max, value))
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func even(value int) int {
	if value%2 != 0 {
		return value - 1
	}
	return value
}

func randomToken(prefix string, size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return prefix + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(data), nil
}

func randomSecret() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "sec_" + base64.RawURLEncoding.EncodeToString(data), nil
}

func (s *Service) load() error {
	data, err := os.ReadFile(s.stateFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read browser pages: %w", err)
	}
	var stored storedState
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("decode browser pages: %w", err)
	}
	for _, item := range stored.Pages {
		if !strings.HasPrefix(item.PageID, "pg_") || len(item.PageID) != 29 {
			continue
		}
		viewport := item.Viewport
		if viewport.CSSWidth == 0 {
			viewport = defaultViewport()
		}
		s.pages[item.PageID] = &Page{ID: item.PageID, URL: item.URL, Title: item.Title, Viewport: viewport, Video: normalizeVideo(item.Video), Watchers: make(map[string]*Watcher)}
	}
	return nil
}

func (s *Service) saveLocked() error {
	stored := storedState{Pages: make([]storedPage, 0, len(s.pages))}
	for _, page := range s.pages {
		stored.Pages = append(stored.Pages, storedPage{PageID: page.ID, URL: page.URL, Title: page.Title, Viewport: page.Viewport, Video: page.Video})
	}
	sort.Slice(stored.Pages, func(i, j int) bool { return stored.Pages[i].PageID < stored.Pages[j].PageID })
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.stateFile), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.stateFile), ".browser-pages-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.stateFile)
}

func (s *Service) Restore(ctx context.Context) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.pages))
	for id := range s.pages {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		s.mu.Lock()
		page := s.pages[id]
		s.mu.Unlock()
		if page == nil {
			continue
		}
		page.opMu.Lock()
		driver, err := s.backend.OpenPage(ctx, id, page.URL, page.Viewport)
		s.mu.Lock()
		if err != nil {
			page.Online, page.LastError = false, err.Error()
		} else {
			generation, randomErr := randomToken("gen_", 16)
			if randomErr != nil {
				page.LastError = randomErr.Error()
				_ = driver.Close(ctx)
			} else {
				page.driver, page.Generation, page.Online, page.LastError = driver, generation, true, ""
				page.stream = s.streamFactory(id)
			}
		}
		s.mu.Unlock()
		page.opMu.Unlock()
		s.publishState()
	}
}

func (s *Service) CreatePage(ctx context.Context, rawURL string) (map[string]any, error) {
	if rawURL == "" {
		rawURL = "about:blank"
	}
	if !validPageURL(rawURL) {
		return nil, errorf("invalid_url", "only HTTP(S) or about:blank is allowed")
	}
	s.mu.Lock()
	if len(s.pages) >= maxManagedPages {
		s.mu.Unlock()
		return nil, errorf("too_many_pages", "managed page limit reached")
	}
	s.mu.Unlock()
	pageID, err := randomToken("pg_", 16)
	if err != nil {
		return nil, err
	}
	viewport := defaultViewport()
	driver, err := s.backend.OpenPage(ctx, pageID, rawURL, viewport)
	if err != nil {
		return nil, errorf("chrome_error", "cannot create managed Chrome page: %v", err)
	}
	generation, err := randomToken("gen_", 16)
	if err != nil {
		_ = driver.Close(ctx)
		return nil, err
	}
	page := &Page{ID: pageID, URL: rawURL, Viewport: viewport, Video: normalizeVideo(VideoSettings{}), Online: true, Generation: generation, Watchers: make(map[string]*Watcher), driver: driver, stream: s.streamFactory(pageID)}
	if url, title, metadataErr := driver.Metadata(ctx); metadataErr == nil {
		page.URL, page.Title = url, title
	}
	s.mu.Lock()
	s.pages[pageID] = page
	err = s.saveLocked()
	if err != nil {
		delete(s.pages, pageID)
	}
	s.mu.Unlock()
	if err != nil {
		_ = driver.Close(ctx)
		return nil, err
	}
	s.publishState()
	return pageState(page), nil
}

func validPageURL(raw string) bool {
	if raw == "about:blank" {
		return true
	}
	return validHTTPURL(raw)
}

func validHTTPURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func (s *Service) getPage(pageID string) *Page {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pages[pageID]
}

func (s *Service) Require(pageID, leaseID string) (map[string]any, error) {
	page := s.getPage(pageID)
	if page == nil {
		return nil, errorf("page_not_found", "managed page does not exist")
	}
	page.opMu.Lock()
	defer page.opMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !page.Online {
		return nil, errorf("page_offline", "managed Chrome page is offline")
	}
	s.expireLeaseLocked(page)
	if page.Lease != nil && (page.Lease.Owner != "agent" || leaseID != page.Lease.ID) {
		return nil, errorf("page_busy", "page is controlled by another owner")
	}
	if page.Lease == nil {
		id, err := randomToken("ls_", 16)
		if err != nil {
			return nil, err
		}
		page.Lease = &Lease{ID: id, Owner: "agent"}
	}
	page.Lease.ExpiresAt = s.now().Add(s.leaseTTL)
	result := map[string]any{"page_id": pageID, "lease_id": page.Lease.ID, "expires_at": page.Lease.ExpiresAt}
	go s.publishState()
	return result, nil
}

func (s *Service) Release(pageID, leaseID string) error {
	page := s.getPage(pageID)
	if page == nil {
		return errorf("page_not_found", "managed page does not exist")
	}
	page.opMu.Lock()
	s.mu.Lock()
	if page.Lease == nil {
		s.mu.Unlock()
		page.opMu.Unlock()
		return nil
	}
	if page.Lease.Owner != "agent" || page.Lease.ID != leaseID {
		s.mu.Unlock()
		page.opMu.Unlock()
		return errorf("invalid_lease", "lease does not own this page")
	}
	page.Lease = nil
	s.mu.Unlock()
	page.opMu.Unlock()
	s.publishState()
	return nil
}

func (s *Service) ForceRelease(pageID string) error {
	page := s.getPage(pageID)
	if page == nil {
		return errorf("page_not_found", "managed page does not exist")
	}
	page.opMu.Lock()
	s.mu.Lock()
	page.Lease = nil
	s.mu.Unlock()
	page.opMu.Unlock()
	s.publishState()
	return nil
}

func (s *Service) Status(pageID string) (map[string]any, error) {
	page := s.getPage(pageID)
	if page == nil {
		return nil, errorf("page_not_found", "managed page does not exist")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return pageState(page), nil
}

func (s *Service) Execute(ctx context.Context, pageID, leaseID, action string, payload map[string]any, timeout time.Duration) (any, error) {
	page := s.getPage(pageID)
	if page == nil {
		return nil, errorf("page_not_found", "managed page does not exist")
	}
	page.opMu.Lock()
	defer page.opMu.Unlock()
	s.mu.Lock()
	if !page.Online {
		s.mu.Unlock()
		return nil, errorf("page_offline", "managed Chrome page is offline")
	}
	s.expireLeaseLocked(page)
	if page.Lease == nil || page.Lease.Owner != "agent" || page.Lease.ID != leaseID {
		s.mu.Unlock()
		return nil, errorf("invalid_lease", "a valid agent require lease is required")
	}
	page.Lease.ExpiresAt = s.now().Add(s.leaseTTL)
	s.mu.Unlock()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	result, err := page.driver.Execute(ctx, action, payload)
	if object, ok := result.(map[string]any); ok {
		s.updatePageMetadata(page, stringField(object, "url"), stringField(object, "title"))
	}
	return result, err
}

func (s *Service) updatePageMetadata(page *Page, rawURL, title string) {
	s.mu.Lock()
	changed := false
	if rawURL != "" && page.URL != rawURL {
		page.URL, changed = rawURL, true
	}
	if title != "" && page.Title != title {
		page.Title, changed = title, true
	}
	if changed {
		_ = s.saveLocked()
	}
	s.mu.Unlock()
	if changed {
		s.publishState()
	}
}

func (s *Service) Watch(ctx context.Context, pageID, viewerID string, value map[string]any) (map[string]any, error) {
	page := s.getPage(pageID)
	if page == nil {
		return nil, errorf("page_not_found", "managed page does not exist")
	}
	if viewerID == "" || len(viewerID) > 128 {
		return nil, errorf("invalid_request", "viewer_id is required")
	}
	page.opMu.Lock()
	defer page.opMu.Unlock()
	s.mu.Lock()
	if !page.Online {
		s.mu.Unlock()
		return nil, errorf("page_offline", "managed Chrome page is offline")
	}
	page.Watchers[viewerID] = &Watcher{ID: viewerID, LastSeen: s.now()}
	if page.ViewportOwner == "" {
		page.ViewportOwner = viewerID
	}
	owner := page.ViewportOwner == viewerID
	viewport, video := page.Viewport, page.Video
	if owner && intField(value, "pane_width") > 0 && intField(value, "pane_height") > 0 {
		viewport = makeViewport(intField(value, "pane_width"), intField(value, "pane_height"), numberField(value, "page_zoom"), numberField(value, "render_scale"))
		video = normalizeVideo(VideoSettings{FPS: intField(value, "fps"), BitrateKbps: intField(value, "bitrate_kbps")})
	}
	s.mu.Unlock()
	if viewport != page.Viewport {
		if err := page.driver.SetViewport(ctx, viewport); err != nil {
			return nil, err
		}
	}
	display, xid := page.driver.StreamTarget()
	if err := page.stream.Ensure(ctx, display, xid, viewport, video); err != nil {
		return nil, errorf("stream_error", "cannot start browser video: %v", err)
	}
	ticket, err := page.stream.Ticket(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	page.Viewport, page.Video = viewport, video
	_ = s.saveLocked()
	state := pageState(page)
	s.mu.Unlock()
	result := map[string]any{"page": state, "stream": ticket, "viewport_owner": owner}
	s.publishState()
	return result, nil
}

func (s *Service) Unwatch(pageID, viewerID string) {
	page := s.getPage(pageID)
	if page == nil {
		return
	}
	page.opMu.Lock()
	s.mu.Lock()
	delete(page.Watchers, viewerID)
	if page.ViewportOwner == viewerID {
		page.ViewportOwner = ""
	}
	if page.Lease != nil && page.Lease.Owner == "human" && page.Lease.ViewerID == viewerID {
		page.Lease = nil
	}
	noWatchers := len(page.Watchers) == 0
	s.mu.Unlock()
	if noWatchers && page.stream != nil {
		page.stream.Stop()
	}
	page.opMu.Unlock()
	s.publishState()
}

func (s *Service) HumanControl(pageID, viewerID string, takeover bool) (map[string]any, error) {
	page := s.getPage(pageID)
	if page == nil {
		return nil, errorf("page_not_found", "managed page does not exist")
	}
	page.opMu.Lock()
	defer page.opMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if page.Watchers[viewerID] == nil {
		return nil, errorf("not_watching", "open the page video before taking control")
	}
	s.expireLeaseLocked(page)
	if !takeover && page.Lease != nil && (page.Lease.Owner != "human" || page.Lease.ViewerID != viewerID) {
		return nil, errorf("page_busy", "page is controlled by another owner")
	}
	if takeover || page.Lease == nil {
		id, err := randomToken("hu_", 16)
		if err != nil {
			return nil, err
		}
		page.Lease = &Lease{ID: id, Owner: "human", ViewerID: viewerID}
	}
	page.Lease.ExpiresAt = s.now().Add(humanLeaseTTL)
	result := map[string]any{"page_id": pageID, "lease_id": page.Lease.ID, "expires_at": page.Lease.ExpiresAt}
	go s.publishState()
	return result, nil
}

func (s *Service) Input(ctx context.Context, pageID, viewerID string, event map[string]any) error {
	page := s.getPage(pageID)
	if page == nil {
		return errorf("page_not_found", "managed page does not exist")
	}
	page.opMu.Lock()
	defer page.opMu.Unlock()
	s.mu.Lock()
	s.expireLeaseLocked(page)
	if page.Lease == nil || page.Lease.Owner != "human" || page.Lease.ViewerID != viewerID {
		s.mu.Unlock()
		return errorf("invalid_lease", "human input control is required")
	}
	page.Lease.ExpiresAt = s.now().Add(humanLeaseTTL)
	s.mu.Unlock()
	ctx, cancel := inputContext(ctx)
	defer cancel()
	return page.driver.Input(ctx, event)
}

func (s *Service) HumanNavigate(ctx context.Context, pageID, viewerID, rawURL string) (map[string]any, error) {
	if !validPageURL(rawURL) {
		return nil, errorf("invalid_url", "only HTTP(S) or about:blank is allowed")
	}
	if err := s.Input(ctx, pageID, viewerID, map[string]any{"type": "insertText", "text": ""}); err != nil {
		return nil, err
	}
	page := s.getPage(pageID)
	page.opMu.Lock()
	defer page.opMu.Unlock()
	s.mu.Lock()
	s.expireLeaseLocked(page)
	validControl := page.Online && page.Lease != nil && page.Lease.Owner == "human" && page.Lease.ViewerID == viewerID
	if validControl {
		page.Lease.ExpiresAt = s.now().Add(humanLeaseTTL)
	}
	s.mu.Unlock()
	if !validControl {
		return nil, errorf("invalid_lease", "human input control is required")
	}
	if err := page.driver.Navigate(ctx, rawURL); err != nil {
		return nil, err
	}
	url, title, _ := page.driver.Metadata(ctx)
	s.updatePageMetadata(page, url, title)
	return map[string]any{"navigated": true, "url": url, "title": title}, nil
}

func (s *Service) ClosePage(ctx context.Context, pageID string) error {
	page := s.getPage(pageID)
	if page == nil {
		return errorf("page_not_found", "managed page does not exist")
	}
	page.opMu.Lock()
	if page.stream != nil {
		page.stream.Stop()
	}
	var err error
	if page.driver != nil {
		err = page.driver.Close(ctx)
	}
	s.mu.Lock()
	delete(s.pages, pageID)
	saveErr := s.saveLocked()
	s.mu.Unlock()
	page.opMu.Unlock()
	s.publishState()
	if err != nil {
		return err
	}
	return saveErr
}

func (s *Service) expireLeaseLocked(page *Page) {
	if page.Lease != nil && !s.now().Before(page.Lease.ExpiresAt) {
		page.Lease = nil
	}
}

func (s *Service) Maintain() {
	s.mu.Lock()
	pages := make([]*Page, 0, len(s.pages))
	for _, page := range s.pages {
		pages = append(pages, page)
	}
	s.mu.Unlock()
	for _, page := range pages {
		page.opMu.Lock()
		changed := false
		s.mu.Lock()
		now := s.now()
		for id, watcher := range page.Watchers {
			if now.Sub(watcher.LastSeen) > watcherTTL {
				delete(page.Watchers, id)
				if page.ViewportOwner == id {
					page.ViewportOwner = ""
				}
				if page.Lease != nil && page.Lease.Owner == "human" && page.Lease.ViewerID == id {
					page.Lease = nil
				}
				changed = true
			}
		}
		before := page.Lease
		s.expireLeaseLocked(page)
		changed = changed || before != page.Lease
		online := page.Online
		s.mu.Unlock()
		if len(page.Watchers) == 0 && page.stream != nil {
			page.stream.Stop()
		}
		if online {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			url, title, err := page.driver.Metadata(ctx)
			cancel()
			s.mu.Lock()
			if err != nil {
				page.Online, page.LastError = false, err.Error()
				changed = true
			} else {
				if page.URL != url || page.Title != title {
					page.URL, page.Title, changed = url, title, true
				}
			}
			if changed {
				_ = s.saveLocked()
			}
			s.mu.Unlock()
		}
		page.opMu.Unlock()
		if changed {
			s.publishState()
		}
	}
}

func (s *Service) State() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	pages := make([]map[string]any, 0, len(s.pages))
	for _, page := range s.pages {
		pages = append(pages, pageState(page))
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i]["page_id"].(string) < pages[j]["page_id"].(string) })
	return map[string]any{"backend": "managed_chrome", "pages": pages}
}

func pageState(page *Page) map[string]any {
	state := map[string]any{
		"page_id": page.ID, "url": page.URL, "title": page.Title, "online": page.Online,
		"viewport": page.Viewport, "video": page.Video, "watchers": len(page.Watchers),
		"busy": page.Lease != nil, "viewport_owner": page.ViewportOwner,
	}
	if page.LastError != "" {
		state["last_error"] = page.LastError
	}
	if page.Lease != nil {
		state["control_owner"] = page.Lease.Owner
		state["lease_expires_at"] = page.Lease.ExpiresAt
	} else {
		state["control_owner"] = ""
	}
	return state
}

func (s *Service) publishState() {
	if s.bus == nil {
		return
	}
	s.statePublishMu.Lock()
	defer s.statePublishMu.Unlock()
	state := s.State()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.bus.Set(ctx, stateChannel, state)
}

func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	pages := make([]*Page, 0, len(s.pages))
	for _, page := range s.pages {
		pages = append(pages, page)
	}
	s.mu.Unlock()
	for _, page := range pages {
		if page.stream != nil {
			page.stream.Stop()
		}
	}
}
