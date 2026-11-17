package main

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxDownloadsPerPage = 50

type downloadRecord struct {
	ID               string `json:"id"`
	PageID           string `json:"page_id"`
	URL              string `json:"url"`
	SuggestedName    string `json:"suggested_filename"`
	Path             string `json:"path,omitempty"`
	State            string `json:"state"`
	ReceivedBytes    int64  `json:"received_bytes"`
	TotalBytes       int64  `json:"total_bytes"`
	CreatedAt        string `json:"created_at"`
}

type downloadTracker struct {
	mu      sync.Mutex
	root    string
	records map[string]*downloadRecord
}

func newDownloadTracker(root string) *downloadTracker {
	return &downloadTracker{root: root, records: map[string]*downloadRecord{}}
}

func (t *downloadTracker) begin(pageID string, params map[string]any) string {
	id := stringField(params, "guid")
	if id == "" || pageID == "" {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.records[id] = &downloadRecord{
		ID: id, PageID: pageID, URL: stringField(params, "url"),
		SuggestedName: stringField(params, "suggestedFilename"), State: "in_progress",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	t.pruneLocked(pageID)
	return id
}

func (t *downloadTracker) progress(params map[string]any) {
	id := stringField(params, "guid")
	t.mu.Lock()
	defer t.mu.Unlock()
	record := t.records[id]
	if record == nil {
		return
	}
	record.ReceivedBytes = int64(numberField(params, "receivedBytes"))
	record.TotalBytes = int64(numberField(params, "totalBytes"))
	switch stringField(params, "state") {
	case "completed":
		record.State = "completed"
		record.Path = filepath.Join(t.root, record.PageID, id)
	case "canceled":
		record.State = "canceled"
	default:
		record.State = "in_progress"
	}
	t.pruneLocked(record.PageID)
}

func (t *downloadTracker) pruneLocked(pageID string) {
	var records []*downloadRecord
	for _, record := range t.records {
		if record.PageID == pageID {
			records = append(records, record)
		}
	}
	if len(records) <= maxDownloadsPerPage {
		return
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt < records[j].CreatedAt })
	for _, record := range records[:len(records)-maxDownloadsPerPage] {
		if record.State != "in_progress" {
			delete(t.records, record.ID)
		}
	}
}

func (t *downloadTracker) list(pageID string) []map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	var records []*downloadRecord
	for _, record := range t.records {
		if record.PageID == pageID {
			copyRecord := *record
			records = append(records, &copyRecord)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt < records[j].CreatedAt })
	result := make([]map[string]any, 0, len(records))
	for _, record := range records {
		result = append(result, downloadMap(record))
	}
	return result
}

func (t *downloadTracker) wait(ctx context.Context, pageID, downloadID string) (map[string]any, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		t.mu.Lock()
		record := t.records[downloadID]
		if record != nil && record.PageID != pageID {
			record = nil
		}
		var copyRecord *downloadRecord
		if record != nil {
			value := *record
			copyRecord = &value
		}
		t.mu.Unlock()
		if copyRecord == nil {
			return nil, errorf("download_not_found", "Download does not belong to this Page or has expired")
		}
		switch copyRecord.State {
		case "completed":
			result := downloadMap(copyRecord)
			result["found"] = true
			return result, nil
		case "canceled":
			return nil, errorf("download_failed", "Download was canceled")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func downloadMap(record *downloadRecord) map[string]any {
	return map[string]any{
		"id": record.ID, "page_id": record.PageID, "url": record.URL,
		"suggested_filename": record.SuggestedName, "path": record.Path,
		"state": record.State, "received_bytes": record.ReceivedBytes,
		"total_bytes": record.TotalBytes, "created_at": record.CreatedAt,
	}
}

func (m *ChromeManager) browserEventLoop(conn *cdpConn) {
	for {
		event, err := conn.nextEvent(context.Background())
		if err != nil {
			return
		}
		switch event.Method {
		case "Browser.downloadWillBegin":
			m.downloads.begin(m.pageIDForFrame(stringField(event.Params, "frameId")), event.Params)
		case "Browser.downloadProgress":
			m.downloads.progress(event.Params)
		}
	}
}

func (m *ChromeManager) pageIDForFrame(frameID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for pageID, page := range m.pages {
		if page.targetID == frameID {
			return pageID
		}
	}
	return ""
}

func (m *ChromeManager) downloadsForPage(pageID string) []map[string]any {
	if m.downloads == nil {
		return []map[string]any{}
	}
	return m.downloads.list(pageID)
}

func (m *ChromeManager) waitDownload(ctx context.Context, pageID, downloadID string) (map[string]any, error) {
	if m.downloads == nil {
		return nil, errors.New("download tracking is unavailable")
	}
	return m.downloads.wait(ctx, pageID, downloadID)
}

func validateDownloadID(value string) bool {
	return value != "" && !strings.ContainsAny(value, "/\\") && value != "." && value != ".."
}
