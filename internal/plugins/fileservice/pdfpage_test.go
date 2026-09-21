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

	first, width, height, err := renderer.render(path, info, 1, 144, 100, 100)
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
	second, width2, height2, err := renderer.render(path, info, 1, 144, 100, 100)
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
	if _, _, _, err := renderer.render(path, changed, 1, 144, 100, 100); err != nil {
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
	data, width, height, err := renderer.render(path, info, 1, 144, 100, 100)
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

func TestPDFTrimPercentages(t *testing.T) {
	requirePDFTools(t)
	path := writePDF(t, marginalPDF)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	renderer := newPDFRenderer(t.TempDir())

	// 0/0 keeps the full 400x200px page.
	_, width, height, err := renderer.render(path, info, 1, 144, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if width != 400 || height != 200 {
		t.Fatalf("trim 0/0 dims = %dx%d, want 400x200", width, height)
	}
	// 100/0 trims only horizontally: ~200x200.
	_, width, height, err = renderer.render(path, info, 1, 144, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if width < 180 || width > 220 || height != 200 {
		t.Fatalf("trim 100/0 dims = %dx%d, want ~200x200", width, height)
	}
	// 50/50 cuts half of each margin: ~300x150.
	_, width, height, err = renderer.render(path, info, 1, 144, 50, 50)
	if err != nil {
		t.Fatal(err)
	}
	if width < 280 || width > 320 || height < 135 || height > 165 {
		t.Fatalf("trim 50/50 dims = %dx%d, want ~300x150", width, height)
	}
	// Distinct trim settings render under distinct cache entries.
	entries, err := os.ReadDir(renderer.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("cache entries = %d, want 3", len(entries))
	}
}

func TestParseTrimBox(t *testing.T) {
	box, ok := parseTrimBox("200x100+50+25\n")
	if !ok || box != (pdfRect{x: 50, y: 25, w: 200, h: 100}) {
		t.Fatalf("parseTrimBox = %+v, %v", box, ok)
	}
	if _, ok := parseTrimBox("garbage"); ok {
		t.Fatal("non-geometry output must be rejected")
	}
	// A blank page reports a degenerate box; the caller falls back to the
	// full page, so parsing must accept it.
	if box, ok := parseTrimBox("0x0+400+200"); !ok || box != (pdfRect{x: 400, y: 200}) {
		t.Fatalf("degenerate box = %+v, %v", box, ok)
	}
}

func TestCropBox(t *testing.T) {
	box := pdfRect{x: 100, y: 50, w: 200, h: 100} // content box in a 400x200 page
	if crop := cropBox(400, 200, box, 100, 100); crop != box {
		t.Fatalf("trim 100 = %+v, want the content box", crop)
	}
	if crop := cropBox(400, 200, box, 0, 0); crop != (pdfRect{x: 0, y: 0, w: 400, h: 200}) {
		t.Fatalf("trim 0 = %+v, want the full page", crop)
	}
	// Half the margins cut on each side: 50/25 kept per side.
	if crop := cropBox(400, 200, box, 50, 50); crop != (pdfRect{x: 50, y: 25, w: 300, h: 150}) {
		t.Fatalf("trim 50 = %+v, want {50 25 300 150}", crop)
	}
	// Per-axis independence: full horizontal trim, no vertical trim.
	if crop := cropBox(400, 200, box, 100, 0); crop != (pdfRect{x: 100, y: 0, w: 200, h: 200}) {
		t.Fatalf("trim 100/0 = %+v, want {100 0 200 200}", crop)
	}
}

func TestRequestTrim(t *testing.T) {
	if value, ok := requestTrim(map[string]any{}, "trim_x"); !ok || value != 100 {
		t.Fatalf("absent trim = %d, %v, want 100", value, ok)
	}
	if value, ok := requestTrim(map[string]any{"trim_x": float64(60)}, "trim_x"); !ok || value != 60 {
		t.Fatalf("trim 60 = %d, %v", value, ok)
	}
	if _, ok := requestTrim(map[string]any{"trim_x": float64(101)}, "trim_x"); ok {
		t.Fatal("trim > 100 must be rejected")
	}
	if _, ok := requestTrim(map[string]any{"trim_x": float64(-1)}, "trim_x"); ok {
		t.Fatal("negative trim must be rejected")
	}
	if _, ok := requestTrim(map[string]any{"trim_x": 1.5}, "trim_x"); ok {
		t.Fatal("fractional trim must be rejected")
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
