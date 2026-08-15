package gateway

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/coder/websocket"
)

func TestRestartHandler(t *testing.T) {
	server := New(DefaultConfig())

	t.Run("accepted", func(t *testing.T) {
		var spawned atomic.Bool
		original := restartSelf
		restartSelf = func(_ context.Context) error { spawned.Store(true); return nil }
		defer func() { restartSelf = original }()

		rec := httptest.NewRecorder()
		server.serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/restart", nil))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("restart status = %d, want %d", rec.Code, http.StatusAccepted)
		}
		if body := rec.Body.String(); body != `{"status":"restarting"}` {
			t.Fatalf("restart body = %q", body)
		}
		if !spawned.Load() {
			t.Fatal("restartSelf was not called")
		}
	})

	t.Run("spawn failure returns 500 and keeps serving", func(t *testing.T) {
		original := restartSelf
		restartSelf = func(_ context.Context) error { return errors.New("boom") }
		defer func() { restartSelf = original }()

		rec := httptest.NewRecorder()
		server.serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/restart", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("restart failure status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
	})

	t.Run("get is not a restart", func(t *testing.T) {
		original := restartSelf
		restartSelf = func(_ context.Context) error { t.Fatal("GET must not restart"); return nil }
		defer func() { restartSelf = original }()

		server := New(Config{StaticFS: fstest.MapFS{
			"index.html": &fstest.MapFile{Data: []byte("<h1>viewer</h1>")},
		}})
		rec := httptest.NewRecorder()
		server.serveHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/restart", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET restart status = %d, want static 404", rec.Code)
		}
	})
}

func TestBrowserHello(t *testing.T) {
	const conn = "11111111-1111-4111-8111-111111111111"
	version, got, err := browserHello(websocket.MessageText, []byte(`{"type":"hello","protocol_version":1,"conn":"`+conn+`"}`))
	if err != nil || version != 1 || got != conn {
		t.Fatalf("browserHello() = (%d, %q, %v)", version, got, err)
	}
	for _, raw := range []string{
		`{"type":"publish"}`,
		`{"type":"hello","conn":"not-a-uuid"}`,
		`{"type":"hello","conn":"11111111-1111-1111-8111-111111111111"}`,
	} {
		if _, _, err := browserHello(websocket.MessageText, []byte(raw)); err == nil {
			t.Fatalf("browserHello(%s) unexpectedly succeeded", raw)
		}
	}
}

func TestStaticFS(t *testing.T) {
	server := New(Config{StaticFS: fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<h1>viewer</h1>")},
		"app.js":     &fstest.MapFile{Data: []byte("export {}")},
	}})

	root := httptest.NewRecorder()
	server.serveStatic(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if root.Code != http.StatusOK || root.Body.String() != "<h1>viewer</h1>" {
		t.Fatalf("GET / = %d %q", root.Code, root.Body.String())
	}

	script := httptest.NewRecorder()
	server.serveStatic(script, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if script.Code != http.StatusOK || script.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("GET /app.js = %d Content-Type %q", script.Code, script.Header().Get("Content-Type"))
	}

	traversal := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/safe", nil)
	request.URL.Path = "/../../etc/passwd"
	server.serveStatic(traversal, request)
	if traversal.Code != http.StatusNotFound {
		t.Fatalf("traversal status = %d", traversal.Code)
	}
}

func TestDirectoryFSRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	staticFS, err := DirectoryFS(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile(staticFS, "escape.txt"); err == nil {
		t.Fatal("escaping symlink unexpectedly opened")
	}
}
