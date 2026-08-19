package gateway

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"viewer/sdk/go/busclient"
)

// TestPushAssetsSDK exercises the Go SDK helper end to end: a small bundle
// goes one-shot; a bundle over the one-shot budget switches to the chunked
// begin/file/commit sequence with intra-file append chunks, and the served
// bytes match the source exactly.
func TestPushAssetsSDK(t *testing.T) {
	server, caller, _ := testAssetsServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Small bundle → one-shot.
	small := t.TempDir()
	if err := os.WriteFile(filepath.Join(small, "frontend.js"), []byte("export default {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reply, err := busclient.PushAssets(ctx, caller, small, map[string]any{"name": "My Plugin"})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := reply.(map[string]any)
	if !ok || entry["entry"] != "frontend.js" || entry["url"] == "" {
		t.Fatalf("reply=%#v", reply)
	}

	// Missing entry file fails before touching the bus.
	if _, err = busclient.PushAssets(ctx, caller, t.TempDir(), nil); err == nil {
		t.Fatal("expected error for missing frontend.js")
	}

	// Bundle over the one-shot budget → chunked with append chunks. One
	// 1.2 MiB file forces both the chunked path and intra-file splitting.
	big := t.TempDir()
	payload := make([]byte, 1200*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(big, "frontend.js"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(big, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(big, "assets", "style.css"), []byte("body{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reply, err = busclient.PushAssets(ctx, caller, big, nil)
	if err != nil {
		t.Fatal(err)
	}
	url, _ := reply.(map[string]any)["url"].(string)
	if url == "" {
		t.Fatalf("reply=%#v", reply)
	}
	recorder := httptest.NewRecorder()
	server.serveHTTP(recorder, httptest.NewRequest(http.MethodGet, url+"frontend.js", nil))
	if recorder.Code != http.StatusOK || len(recorder.Body.Bytes()) != len(payload) {
		t.Fatalf("GET: code=%d len=%d want %d", recorder.Code, len(recorder.Body.Bytes()), len(payload))
	}
	for i, b := range recorder.Body.Bytes() {
		if b != payload[i] {
			t.Fatalf("served content differs at byte %d", i)
		}
	}
	// Subdirectory files are served too.
	recorder = httptest.NewRecorder()
	server.serveHTTP(recorder, httptest.NewRequest(http.MethodGet, url+"assets/style.css", nil))
	if recorder.Body.String() != "body{}\n" {
		t.Fatalf("subdirectory file mismatch: %q", recorder.Body.String())
	}
}
