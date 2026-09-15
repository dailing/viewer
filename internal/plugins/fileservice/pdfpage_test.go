package fileservice

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const minimalPDF = "%PDF-1.4\n" +
	"1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
	"2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
	"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 100]>>endobj\n" +
	"trailer<</Root 1 0 R>>\n"

// marginalPDF draws a centered 100x50pt black rectangle on a 200x100pt page,
// leaving uniform 50pt/25pt white margins for the trim step to remove.
const marginalPDF = "%PDF-1.4\n" +
	"1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
	"2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
	"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 100]/Contents 4 0 R/Resources<<>>>>endobj\n" +
	"4 0 obj<</Length 27>>stream\n" +
	"0 0 0 rg\n50 25 100 50 re f\n" +
	"endstream\nendobj\n" +
	"trailer<</Root 1 0 R>>\n"

func requirePDFTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"mutool", "convert", "identify"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

func writePDF(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.pdf")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPDFDocInfo(t *testing.T) {
	requirePDFTools(t)
	path := writePDF(t, minimalPDF)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	renderer := newPDFRenderer(t.TempDir())
	doc, err := renderer.docInfo(path, info)
	if err != nil {
		t.Fatal(err)
	}
	if doc.pages != 1 {
		t.Fatalf("pages = %d, want 1", doc.pages)
	}
	if got := doc.pageSizes[0]; got.width != 200 || got.height != 100 {
		t.Fatalf("page size = %v, want 200x100", got)
	}
	// Cached path must hit without re-running mutool.
	cached, err := renderer.docInfo(path, info)
	if err != nil || cached != doc {
		t.Fatalf("cached docInfo = %v, %v", cached, err)
	}
}

func TestPDFRenderCachesAndDedupes(t *testing.T) {
	requirePDFTools(t)
	path := writePDF(t, minimalPDF)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	renderer := newPDFRenderer(t.TempDir())

	first, width, height, err := renderer.render(path, info, 1, 144)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("empty render")
	}
	// Blank page: the trim guard must fall back to the full 400x200 render.
	if width != 400 || height != 200 {
		t.Fatalf("blank page dims = %dx%d, want 400x200", width, height)
	}
	second, width2, height2, err := renderer.render(path, info, 1, 144)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || width2 != width || height2 != height {
		t.Fatal("cached render differs")
	}
	entries, err := os.ReadDir(renderer.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("cache entries = %d, want 1", len(entries))
	}
	// A changed file must render under a different cache key.
	if err := os.WriteFile(path, []byte(minimalPDF+"%changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := renderer.render(path, changed, 1, 144); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(renderer.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("cache entries after change = %d, want 2", len(entries))
	}
}

func TestPDFTrimMargins(t *testing.T) {
	requirePDFTools(t)
	path := writePDF(t, marginalPDF)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	renderer := newPDFRenderer(t.TempDir())
	data, width, height, err := renderer.render(path, info, 1, 144)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("empty render")
	}
	// The 100x50pt rect at 144 dpi trims to 200x100px; allow raster slack.
	if width < 180 || width > 220 || height < 90 || height > 110 {
		t.Fatalf("trimmed dims = %dx%d, want ~200x100", width, height)
	}
}

func TestScanDimensions(t *testing.T) {
	w1, h1, w2, h2, ok := scanDimensions("400 200\n200 100\n")
	if !ok || w1 != 400 || h1 != 200 || w2 != 200 || h2 != 100 {
		t.Fatalf("scanDimensions = %d %d %d %d %v", w1, h1, w2, h2, ok)
	}
	if _, _, _, _, ok := scanDimensions("400 200\n"); ok {
		t.Fatal("single line must be rejected")
	}
	if _, _, _, _, ok := scanDimensions("garbage\ngarbage\n"); ok {
		t.Fatal("non-numeric lines must be rejected")
	}
}

func TestRequestIntAndFloat(t *testing.T) {
	if value, ok := requestInt(map[string]any{"page": float64(3)}, "page"); !ok || value != 3 {
		t.Fatalf("requestInt float64 = %d, %v", value, ok)
	}
	if _, ok := requestInt(map[string]any{"page": 1.5}, "page"); ok {
		t.Fatal("fractional page must be rejected")
	}
	if value, ok := requestInt(map[string]any{"page": "2"}, "page"); !ok || value != 2 {
		t.Fatalf("requestInt string = %d, %v", value, ok)
	}
	if value, ok := requestFloat(map[string]any{"scale": 2.0}, "scale"); !ok || value != 2.0 {
		t.Fatalf("requestFloat float64 = %v, %v", value, ok)
	}
	if value, ok := requestFloat(map[string]any{"scale": "1.5"}, "scale"); !ok || value != 1.5 {
		t.Fatalf("requestFloat string = %v, %v", value, ok)
	}
}
