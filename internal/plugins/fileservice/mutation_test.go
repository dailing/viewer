package fileservice

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMakeDirectoryValidatesAndRejectsCollision(t *testing.T) {
	root := t.TempDir()
	entry, code, err := makeDirectory(root, "new-folder")
	if err != nil || code != "" || !entry.IsDir || entry.Name != "new-folder" {
		t.Fatalf("mkdir = %+v, code = %q, err = %v", entry, code, err)
	}
	if _, code, err = makeDirectory(root, "new-folder"); err == nil || code != "already_exists" {
		t.Fatalf("collision code = %q, err = %v", code, err)
	}
	for _, name := range []string{"", ".", "..", "a/b", `a\\b`, "a\x00b"} {
		if _, code, err = makeDirectory(root, name); err == nil || code != "invalid_request" {
			t.Fatalf("invalid name %q: code = %q, err = %v", name, code, err)
		}
	}
}

func TestDeleteFileTreeRemovesFilesDirectoriesAndOnlySymlink(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, err := deleteFileTree(file); err != nil || code != "" {
		t.Fatalf("delete file code = %q, err = %v", code, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}

	directory := filepath.Join(root, "tree")
	if err := os.MkdirAll(filepath.Join(directory, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, err := deleteFileTree(directory); err != nil || code != "" {
		t.Fatalf("delete directory code = %q, err = %v", code, err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("directory still exists: %v", err)
	}

	outside := t.TempDir()
	external := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(external, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if code, err := deleteFileTree(link); err != nil || code != "" {
		t.Fatalf("delete symlink code = %q, err = %v", code, err)
	}
	if got, err := os.ReadFile(external); err != nil || string(got) != "keep" {
		t.Fatalf("symlink target changed: %q, %v", got, err)
	}
}

func TestDeleteFileTreeRejectsRootAndMissing(t *testing.T) {
	if code, err := deleteFileTree("/"); err == nil || code != "invalid_request" {
		t.Fatalf("root delete code = %q, err = %v", code, err)
	}
	if code, err := deleteFileTree(filepath.Join(t.TempDir(), "missing")); err == nil || code != "not_found" {
		t.Fatalf("missing delete code = %q, err = %v", code, err)
	}
}
