package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"viewer/sdk/go/busclient"
)

func uploadTestServer(path string, overwrite bool) *Server {
	server := New(DefaultConfig())
	server.uploadResolver = func(context.Context, string) (uploadFileInfo, error) {
		return uploadFileInfo{Path: path, Overwrite: overwrite}, nil
	}
	return server
}

func assertNoUploadTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".upload-") {
			t.Fatalf("temporary upload left behind: %s", entry.Name())
		}
	}
}

func TestServeFileUploadStreamsLargeBody(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "large.bin")
	server := uploadTestServer(target, false)
	content := bytes.Repeat([]byte("0123456789abcdef"), 128*1024) // 2 MiB, above the bus cap.
	rec := httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/files/upload?ticket=good", bytes.NewReader(content)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("uploaded content mismatch: len = %d, err = %v", len(got), err)
	}
	assertNoUploadTemps(t, dir)
}

func TestServeFileUploadCollisionAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	uploadTestServer(target, false).serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/files/upload?ticket=good", strings.NewReader("new")))
	if rec.Code != http.StatusConflict {
		t.Fatalf("collision status = %d, want 409", rec.Code)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatalf("collision changed target to %q", got)
	}
	rec = httptest.NewRecorder()
	uploadTestServer(target, true).serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/files/upload?ticket=good", strings.NewReader("new")))
	if rec.Code != http.StatusCreated {
		t.Fatalf("overwrite status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got, _ := os.ReadFile(target); string(got) != "new" {
		t.Fatalf("overwrite target = %q", got)
	}
	assertNoUploadTemps(t, dir)
}

func TestServeFileUploadRejectsInvalidTicket(t *testing.T) {
	server := New(DefaultConfig())
	server.uploadResolver = func(context.Context, string) (uploadFileInfo, error) {
		return uploadFileInfo{}, &busclient.RPCError{Code: "invalid_ticket", Message: "invalid"}
	}
	rec := httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/files/upload?ticket=bad", strings.NewReader("x")))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

type interruptedReader struct{ sent bool }

func (r *interruptedReader) Read(buffer []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(buffer, "partial"), nil
	}
	return 0, io.ErrUnexpectedEOF
}

func TestServeFileUploadInterruptedBodyCleansTemporary(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "partial.bin")
	server := uploadTestServer(target, false)
	rec := httptest.NewRecorder()
	server.serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/files/upload?ticket=good", &interruptedReader{}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial target exists or stat failed unexpectedly: %v", err)
	}
	assertNoUploadTemps(t, dir)
}
