// PDF page rasterization: the file:_:pdfpage RPC renders one page of a PDF to
// a WebP image via mutool + ImageMagick, cached on disk under the data
// directory. Uniform white margins are cropped server-side down to a
// client-chosen percentage per axis (100 = trim to the content box, 0 = keep
// the full page); client-chosen scales (progressive tiers) keep the wire
// payload well under the 1 MiB bus frame cap.
package fileservice

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

const (
	pdfMinScale          = 0.25
	pdfMaxScale          = 3.0
	pdfMaxPixels         = 24_000_000
	pdfCacheBudgetBytes  = 1 << 30 // 1 GiB
	pdfRenderConcurrency = 2
	pdfRenderTimeout     = 120 * time.Second
	pdfMaxPayloadBytes   = 700_000 // raw WebP cap so base64 stays under the frame limit
	pdfDefaultPageWidth  = 612.0
	pdfDefaultPageHeight = 792.0
	pdfTrimFuzz          = "5%" // ImageMagick fuzz for white-margin detection
	pdfTrimMinDim        = 32   // reject trims leaving a near-empty sliver
	pdfTrimMinArea       = 0.05 // reject trims below this fraction of the original area
)

type pdfPageSize struct {
	width  float64 // points
	height float64 // points
}

type pdfDocInfo struct {
	size      int64
	mtimeNs   int64
	pages     int
	pageSizes []pdfPageSize
}

// pdfCall deduplicates concurrent renders of the same cache key.
type pdfCall struct {
	done chan struct{}
	err  error
}

type pdfRenderer struct {
	dir      string
	sem      chan struct{}
	mu       sync.Mutex
	docs     map[string]*pdfDocInfo
	inflight map[string]*pdfCall
}

func newPDFRenderer(dataDir string) *pdfRenderer {
	return &pdfRenderer{
		dir:      filepath.Join(dataDir, "pdf-pages"),
		sem:      make(chan struct{}, pdfRenderConcurrency),
		docs:     map[string]*pdfDocInfo{},
		inflight: map[string]*pdfCall{},
	}
}

var mediaboxPattern = regexp.MustCompile(
	`^\s*(\d+)\s+\([^)]*\):\s*\[\s*(-?[\d.]+)\s+(-?[\d.]+)\s+(-?[\d.]+)\s+(-?[\d.]+)\s*\]`)

// docInfo returns cached metadata for the PDF, refreshing when the file changed.
func (r *pdfRenderer) docInfo(path string, info os.FileInfo) (*pdfDocInfo, error) {
	size, mtimeNs := info.Size(), info.ModTime().UnixNano()
	r.mu.Lock()
	if cached, ok := r.docs[path]; ok && cached.size == size && cached.mtimeNs == mtimeNs {
		r.mu.Unlock()
		return cached, nil
	}
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "mutool", "info", "--", path).Output()
	if err != nil {
		return nil, fmt.Errorf("mutool info failed: %w", err)
	}
	doc := &pdfDocInfo{size: size, mtimeNs: mtimeNs, pageSizes: []pdfPageSize{}}
	known := map[int]pdfPageSize{}
	for _, line := range strings.Split(string(output), "\n") {
		if rest, found := strings.CutPrefix(line, "Pages: "); found {
			if pages, parseErr := strconv.Atoi(strings.TrimSpace(rest)); parseErr == nil {
				doc.pages = pages
			}
			continue
		}
		if match := mediaboxPattern.FindStringSubmatch(line); match != nil {
			page, _ := strconv.Atoi(match[1])
			x0, _ := strconv.ParseFloat(match[2], 64)
			y0, _ := strconv.ParseFloat(match[3], 64)
			x1, _ := strconv.ParseFloat(match[4], 64)
			y1, _ := strconv.ParseFloat(match[5], 64)
			if x1 > x0 && y1 > y0 {
				known[page] = pdfPageSize{width: x1 - x0, height: y1 - y0}
			}
		}
	}
	if doc.pages <= 0 {
		return nil, fmt.Errorf("no pages found in %s", path)
	}
	doc.pageSizes = make([]pdfPageSize, doc.pages)
	last := pdfPageSize{width: pdfDefaultPageWidth, height: pdfDefaultPageHeight}
	// mutool deduplicates identical mediaboxes, so missing pages inherit the
	// nearest known box (aspect only matters as a placeholder estimate).
	for index := 0; index < doc.pages; index++ {
		if box, ok := known[index+1]; ok {
			last = box
		}
		doc.pageSizes[index] = last
	}

	r.mu.Lock()
	if len(r.docs) >= 64 {
		r.docs = map[string]*pdfDocInfo{}
	}
	r.docs[path] = doc
	r.mu.Unlock()
	return doc, nil
}

// cachePrefix is the stable identity of one (file generation, page, dpi,
// trim) render; the cached file name appends the pixel dimensions it
// produced, so the reader learns the aspect ratio without decoding the image.
func (r *pdfRenderer) cachePrefix(path string, info os.FileInfo, page, dpi, trimX, trimY int) string {
	key := fmt.Sprintf("%s|%d|%d|%d|%d|%d|%d", path, info.Size(), info.ModTime().UnixNano(), page, dpi, trimX, trimY)
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", sum[:16])
}

var cacheNamePattern = regexp.MustCompile(`^([0-9a-f]{32})-([1-9][0-9]*)x([1-9][0-9]*)\.webp$`)

// lookupCache finds the cached file for a prefix, parsing dimensions from its
// name. Zero or ambiguous matches count as a miss.
func (r *pdfRenderer) lookupCache(prefix string) (path string, width, height int, ok bool) {
	matches, err := filepath.Glob(filepath.Join(r.dir, prefix+"-*.webp"))
	if err != nil || len(matches) != 1 {
		return "", 0, 0, false
	}
	name := cacheNamePattern.FindStringSubmatch(filepath.Base(matches[0]))
	if name == nil {
		return "", 0, 0, false
	}
	width, _ = strconv.Atoi(name[2])
	height, _ = strconv.Atoi(name[3])
	return matches[0], width, height, true
}

// render produces (or fetches) the cached WebP for one page, deduplicating
// concurrent identical renders. Returns the WebP bytes and pixel dimensions.
func (r *pdfRenderer) render(path string, info os.FileInfo, page, dpi, trimX, trimY int) ([]byte, int, int, error) {
	prefix := r.cachePrefix(path, info, page, dpi, trimX, trimY)
	if cached, width, height, ok := r.lookupCache(prefix); ok {
		now := time.Now()
		_ = os.Chtimes(cached, now, now) // refresh LRU clock
		if data, err := os.ReadFile(cached); err == nil {
			return data, width, height, nil
		}
	}

	r.mu.Lock()
	if call, ok := r.inflight[prefix]; ok {
		r.mu.Unlock()
		<-call.done
		if call.err != nil {
			return nil, 0, 0, call.err
		}
		cached, width, height, ok := r.lookupCache(prefix)
		if !ok {
			return nil, 0, 0, fmt.Errorf("render finished but cache entry is missing")
		}
		data, err := os.ReadFile(cached)
		return data, width, height, err
	}
	call := &pdfCall{done: make(chan struct{})}
	r.inflight[prefix] = call
	r.mu.Unlock()

	data, width, height, err := r.renderFresh(path, page, dpi, trimX, trimY, prefix)

	r.mu.Lock()
	delete(r.inflight, prefix)
	r.mu.Unlock()
	call.err = err
	close(call.done)
	if err != nil {
		return nil, 0, 0, err
	}
	return data, width, height, nil
}

// renderFresh rasterizes one page, crops uniform white margins down to the
// requested percentage per axis, re-encodes to WebP under the payload cap,
// and atomically installs the cache entry.
func (r *pdfRenderer) renderFresh(path string, page, dpi, trimX, trimY int, prefix string) ([]byte, int, int, error) {
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return nil, 0, 0, err
	}
	r.sem <- struct{}{}
	defer func() { <-r.sem }()

	tmp, err := os.MkdirTemp(r.dir, "render-")
	if err != nil {
		return nil, 0, 0, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	ctx, cancel := context.WithTimeout(context.Background(), pdfRenderTimeout)
	defer cancel()
	pngPath := filepath.Join(tmp, "page.png")
	draw := exec.CommandContext(ctx, "mutool", "draw", "-o", pngPath, "-r", strconv.Itoa(dpi), "--", path, strconv.Itoa(page))
	if output, drawErr := draw.CombinedOutput(); drawErr != nil {
		return nil, 0, 0, fmt.Errorf("mutool draw failed: %v: %s", drawErr, strings.TrimSpace(string(output)))
	}

	// Measure the page and the content bounding box (flattening alpha first
	// so transparent backgrounds count as white). A pathological box —
	// near-empty result — falls back to the full page so blank pages stay
	// full-size.
	identify := exec.CommandContext(ctx, "identify", "-format", "%w %h", pngPath)
	identifyOutput, err := identify.Output()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("identify failed: %w", err)
	}
	var origW, origH int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(identifyOutput)), "%d %d", &origW, &origH); err != nil || origW <= 0 || origH <= 0 {
		return nil, 0, 0, fmt.Errorf("unexpected identify output: %q", strings.TrimSpace(string(identifyOutput)))
	}
	// `%@` is the bounding box a trim WOULD cut to; asking for it without
	// applying -trim leaves the page intact for the percentage crop below.
	trimProbe := exec.CommandContext(ctx, "convert", pngPath,
		"-background", "white", "-alpha", "remove", "-alpha", "off",
		"-fuzz", pdfTrimFuzz, "-format", "%@", "info:")
	probeOutput, err := trimProbe.Output()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("trim probe failed: %w", err)
	}
	box, ok := parseTrimBox(string(probeOutput))
	if !ok {
		return nil, 0, 0, fmt.Errorf("unexpected trim box: %q", strings.TrimSpace(string(probeOutput)))
	}
	if box.w < pdfTrimMinDim || box.h < pdfTrimMinDim ||
		float64(box.w*box.h) < pdfTrimMinArea*float64(origW*origH) {
		box = pdfRect{x: 0, y: 0, w: origW, h: origH}
	}
	crop := cropBox(origW, origH, box, trimX, trimY)
	width, height := crop.w, crop.h

	// Flatten, crop (unless the whole page is kept), and re-encode at lower
	// quality if the payload would approach the frame cap.
	convertArgs := []string{pngPath, "-background", "white", "-alpha", "remove", "-alpha", "off"}
	if crop.x != 0 || crop.y != 0 || crop.w != origW || crop.h != origH {
		convertArgs = append(convertArgs, "-crop",
			fmt.Sprintf("%dx%d+%d+%d", crop.w, crop.h, crop.x, crop.y), "+repage")
	}
	var data []byte
	for _, quality := range []string{"80", "60", "40"} {
		webpPath := filepath.Join(tmp, "page.webp")
		args := append(append([]string{}, convertArgs...), "-strip", "-quality", quality, webpPath)
		convert := exec.CommandContext(ctx, "convert", args...)
		if output, convertErr := convert.CombinedOutput(); convertErr != nil {
			return nil, 0, 0, fmt.Errorf("webp conversion failed: %v: %s", convertErr, strings.TrimSpace(string(output)))
		}
		data, err = os.ReadFile(webpPath)
		if err != nil {
			return nil, 0, 0, err
		}
		if len(data) <= pdfMaxPayloadBytes {
			break
		}
	}
	if len(data) > pdfMaxPayloadBytes {
		return nil, 0, 0, fmt.Errorf("rendered page is %d bytes even at lowest quality", len(data))
	}

	final := filepath.Join(r.dir, fmt.Sprintf("%s-%dx%d.webp", prefix, width, height))
	staged := filepath.Join(tmp, "staged.webp")
	if err := os.WriteFile(staged, data, 0o600); err != nil {
		return nil, 0, 0, err
	}
	if err := os.Rename(staged, final); err != nil {
		return nil, 0, 0, err
	}
	// Defensive: a prefix maps to deterministic dimensions, so any sibling
	// entry with the same prefix is stale.
	if others, globErr := filepath.Glob(filepath.Join(r.dir, prefix+"-*.webp")); globErr == nil {
		for _, other := range others {
			if other != final {
				_ = os.Remove(other)
			}
		}
	}
	r.evict()
	return data, width, height, nil
}

// pdfRect is a pixel rectangle inside a rendered page.
type pdfRect struct{ x, y, w, h int }

var trimBoxPattern = regexp.MustCompile(`^([0-9]+)x([0-9]+)\+([0-9]+)\+([0-9]+)$`)

// parseTrimBox parses ImageMagick's `%@` trim bounding box ("WxH+X+Y"). A
// blank page yields a degenerate zero-size box, which the caller's
// pathological-trim guard turns into a full-page fallback.
func parseTrimBox(output string) (pdfRect, bool) {
	match := trimBoxPattern.FindStringSubmatch(strings.TrimSpace(output))
	if match == nil {
		return pdfRect{}, false
	}
	w, _ := strconv.Atoi(match[1])
	h, _ := strconv.Atoi(match[2])
	x, _ := strconv.Atoi(match[3])
	y, _ := strconv.Atoi(match[4])
	return pdfRect{x: x, y: y, w: w, h: h}, true
}

// cropBox interpolates between the full page (trim percentage 0) and the
// content bounding box (percentage 100) independently per axis: a percentage
// cuts that fraction of each side's margin. Rounding may overshoot by a
// pixel, so the result is clamped back inside the page.
func cropBox(origW, origH int, box pdfRect, trimX, trimY int) pdfRect {
	qx := float64(trimX) / 100
	qy := float64(trimY) / 100
	crop := pdfRect{
		x: int(math.Round(float64(box.x) * qx)),
		y: int(math.Round(float64(box.y) * qy)),
		w: int(math.Round(float64(box.w) + float64(origW-box.w)*(1-qx))),
		h: int(math.Round(float64(box.h) + float64(origH-box.h)*(1-qy))),
	}
	if crop.w > origW-crop.x {
		crop.w = origW - crop.x
	}
	if crop.h > origH-crop.y {
		crop.h = origH - crop.y
	}
	if crop.w < 1 {
		crop.w = 1
	}
	if crop.h < 1 {
		crop.h = 1
	}
	return crop
}

// evict keeps the cache under budget by deleting oldest-touch files.
func (r *pdfRenderer) evict() {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return
	}
	type cachedFile struct {
		path    string
		size    int64
		modTime time.Time
	}
	files := []cachedFile{}
	var total int64
	for _, entry := range entries {
		info, statErr := entry.Info()
		if statErr != nil || !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".webp") {
			continue
		}
		files = append(files, cachedFile{
			path: filepath.Join(r.dir, entry.Name()), size: info.Size(), modTime: info.ModTime(),
		})
		total += info.Size()
	}
	if total <= pdfCacheBudgetBytes {
		return
	}
	sort.Slice(files, func(left, right int) bool { return files[left].modTime.Before(files[right].modTime) })
	target := int64(pdfCacheBudgetBytes) * 4 / 5
	for _, file := range files {
		if total <= target {
			break
		}
		if removeErr := os.Remove(file.path); removeErr == nil {
			total -= file.size
		}
	}
	slog.Info("file-service pdf cache evicted", "remaining_bytes", total)
}

func (p *Plugin) pdfpage(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	value, ok := pluginrpc.Object(frame)
	if !ok {
		p.replyError(frame, "invalid_request", "missing required field: path")
		return
	}
	rawPath, ok := value["path"].(string)
	if !ok || rawPath == "" {
		p.replyError(frame, "invalid_request", "missing required field: path")
		return
	}
	path, ok := resolveRequestPath(rawPath)
	if !ok {
		p.replyError(frame, "invalid_request", "invalid path")
		return
	}
	page, ok := requestInt(value, "page")
	if !ok || page < 1 {
		p.replyError(frame, "invalid_request", "page must be a positive integer")
		return
	}
	scale, ok := requestFloat(value, "scale")
	if !ok || scale < pdfMinScale || scale > pdfMaxScale {
		p.replyError(frame, "invalid_request",
			fmt.Sprintf("scale must be a number in [%.2f, %.2f]", pdfMinScale, pdfMaxScale))
		return
	}
	trimX, ok := requestTrim(value, "trim_x")
	if !ok {
		p.replyError(frame, "invalid_request", "trim_x must be an integer in [0, 100]")
		return
	}
	trimY, ok := requestTrim(value, "trim_y")
	if !ok {
		p.replyError(frame, "invalid_request", "trim_y must be an integer in [0, 100]")
		return
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) || err == nil && !info.Mode().IsRegular() {
		p.replyError(frame, "not_found", "no such file: "+path)
		return
	}
	if err != nil {
		p.replyError(frame, "read_error", err.Error())
		return
	}

	doc, err := p.pdf.docInfo(path, info)
	if err != nil {
		p.replyError(frame, "render_error", err.Error())
		return
	}
	if page > doc.pages {
		p.replyError(frame, "invalid_request",
			fmt.Sprintf("page %d out of range (document has %d pages)", page, doc.pages))
		return
	}
	pageSize := doc.pageSizes[page-1]
	dpi := int(scale * 72)
	if pixels := int64(pageSize.width*float64(dpi)/72) * int64(pageSize.height*float64(dpi)/72); pixels > pdfMaxPixels {
		p.replyError(frame, "invalid_request",
			fmt.Sprintf("scale %.2f renders %d pixels, above the %d-pixel cap", scale, pixels, pdfMaxPixels))
		return
	}

	data, width, height, err := p.pdf.render(path, info, page, dpi, trimX, trimY)
	if err != nil {
		p.replyError(frame, "render_error", err.Error())
		return
	}
	p.reply(frame, map[string]any{
		"path":         path,
		"page":         page,
		"pages":        doc.pages,
		"mtime":        info.ModTime().Unix(),
		"page_width":   pageSize.width,
		"page_height":  pageSize.height,
		"image_width":  width,
		"image_height": height,
		"dpi":          dpi,
		"mime":         "image/webp",
		"encoding":     "base64",
		"content":      base64.StdEncoding.EncodeToString(data),
	})
}

// requestTrim reads an optional margin-trim percentage: absent means 100
// (trim to the content box), present must be an integer in [0, 100].
func requestTrim(value map[string]any, field string) (int, bool) {
	if _, exists := value[field]; !exists {
		return 100, true
	}
	parsed, ok := requestInt(value, field)
	if !ok || parsed < 0 || parsed > 100 {
		return 0, false
	}
	return parsed, true
}

func requestInt(value map[string]any, field string) (int, bool) {
	raw, exists := value[field]
	if !exists {
		return 0, false
	}
	switch number := raw.(type) {
	case float64:
		if number != float64(int(number)) {
			return 0, false
		}
		return int(number), true
	case string:
		parsed, err := strconv.Atoi(number)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func requestFloat(value map[string]any, field string) (float64, bool) {
	raw, exists := value[field]
	if !exists {
		return 0, false
	}
	switch number := raw.(type) {
	case float64:
		return number, true
	case string:
		parsed, err := strconv.ParseFloat(number, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
