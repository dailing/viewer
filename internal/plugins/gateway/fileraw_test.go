package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"viewer/sdk/go/busclient"
)

func rawTestServer(t *testing.T, resolve rawResolver) *Server {
	t.Helper()
	server := New(DefaultConfig())
	server.rawResolver = resolve
	return server
}

func TestServeFileRawStreamsContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.png")
	content := []byte("png-bytes-\x89\x50\x4e\x47")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	server := rawTestServer(t, func(_ context.Context, ticket string) (rawFileInfo, error) {
		if ticket != "good" {
			t.Fatalf("resolver ticket = %q, want good", ticket)
		}
		return rawFileInfo{Path: path, MIME: "image/png"}, nil
	})

	rec := httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/files/raw?ticket=good", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
	if rec.Body.String() != string(content) {
		t.Fatalf("body = %q, want %q", rec.Body.String(), string(content))
	}

	// Range requests ride ServeContent.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/files/raw?ticket=good", nil)
	req.Header.Set("Range", "bytes=0-8")
	server.serveHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", rec.Code)
	}
	if rec.Body.String() != string(content[:9]) {
		t.Fatalf("range body = %q, want %q", rec.Body.String(), string(content[:9]))
	}
}

func TestServeFileRawRejectsBadTickets(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		resolve rawResolver
		want    int
	}{
		{
			name: "missing ticket",
			url:  "/api/files/raw",
			want: http.StatusBadRequest,
		},
		{
			name: "invalid ticket",
			url:  "/api/files/raw?ticket=bad",
			resolve: func(context.Context, string) (rawFileInfo, error) {
				return rawFileInfo{}, &busclient.RPCError{Code: "invalid_ticket", Message: "invalid or expired ticket"}
			},
			want: http.StatusForbidden,
		},
		{
			name: "file gone",
			url:  "/api/files/raw?ticket=stale",
			resolve: func(context.Context, string) (rawFileInfo, error) {
				return rawFileInfo{}, &busclient.RPCError{Code: "not_found", Message: "no such file"}
			},
			want: http.StatusNotFound,
		},
		{
			name: "bus failure",
			url:  "/api/files/raw?ticket=x",
			resolve: func(context.Context, string) (rawFileInfo, error) {
				return rawFileInfo{}, errors.New("connection lost")
			},
			want: http.StatusBadGateway,
		},
		{
			name: "resolved path vanished",
			url:  "/api/files/raw?ticket=ghost",
			resolve: func(context.Context, string) (rawFileInfo, error) {
				return rawFileInfo{Path: filepath.Join(t.TempDir(), "gone.png")}, nil
			},
			want: http.StatusNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := rawTestServer(t, tc.resolve)
			rec := httptest.NewRecorder()
			server.serveHTTP(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
