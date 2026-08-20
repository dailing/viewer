package fileservice

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRequestPathEmptyMeansHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	path, ok := resolveRequestPath("")
	if !ok {
		t.Fatal("empty path should resolve")
	}
	if path != home {
		t.Fatalf("empty path = %q, want home %q", path, home)
	}
}

func TestResolveRequestPathAbsoluteAndTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if path, ok := resolveRequestPath("/tmp"); !ok || path != "/tmp" {
		t.Fatalf("absolute path = %q ok=%v", path, ok)
	}
	if path, ok := resolveRequestPath("~"); !ok || path != home {
		t.Fatalf("tilde = %q ok=%v, want %q", path, ok, home)
	}
	if path, ok := resolveRequestPath("~/x"); !ok || path != filepath.Join(home, "x") {
		t.Fatalf("tilde subpath = %q ok=%v", path, ok)
	}
}
