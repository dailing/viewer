package gateway

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/coder/websocket"
)

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
