package fileservice

import (
	"fmt"
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
		t.Fatalf("tilde path = %q ok=%v", path, ok)
	}
	if path, ok := resolveRequestPath("~/x"); !ok || path != filepath.Join(home, "x") {
		t.Fatalf("tilde subpath = %q ok=%v", path, ok)
	}
}

func TestListDirectoryPageStreamsBoundedPages(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 205; index++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%03d.txt", index)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".hidden"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	offset := 0
	for index, want := range []int{100, 100, 5} {
		page, err := listDirectoryPage(root, false, false, offset, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Entries) != want || page.NextOffset != offset+want || page.HasMore != (index < 2) {
			t.Fatalf("page %d = %d entries next=%d more=%v", index, len(page.Entries), page.NextOffset, page.HasMore)
		}
		for _, entry := range page.Entries {
			if seen[entry.Path] {
				t.Fatalf("duplicate entry across pages: %s", entry.Path)
			}
			seen[entry.Path] = true
		}
		offset = page.NextOffset
	}
	if len(seen) != 205 {
		t.Fatalf("visible entries = %d, want 205", len(seen))
	}
	for _, name := range []string{"a", "b", "c"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	first, err := listDirectoryPage(root, true, false, 0, 2)
	if err != nil || len(first.Entries) != 2 || !first.HasMore || first.NextOffset != 2 {
		t.Fatalf("dirs-only first page = %+v, err=%v", first, err)
	}
	last, err := listDirectoryPage(root, true, false, first.NextOffset, 2)
	if err != nil || len(last.Entries) != 1 || last.HasMore || last.NextOffset != 3 {
		t.Fatalf("dirs-only last page = %+v, err=%v", last, err)
	}
}

func TestListDirectoryPageShowHidden(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"visible", ".hidden"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, ".hidden-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	assertNames := func(page directoryListPage, want ...string) {
		t.Helper()
		if len(page.Entries) != len(want) {
			t.Fatalf("entry count = %d, want %d: %+v", len(page.Entries), len(want), page.Entries)
		}
		for index, entry := range page.Entries {
			if entry.Name != want[index] {
				t.Fatalf("entry %d = %q, want %q", index, entry.Name, want[index])
			}
		}
	}

	visible, err := listDirectoryPage(root, false, false, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	assertNames(visible, "visible")
	withHidden, err := listDirectoryPage(root, false, true, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	assertNames(withHidden, ".hidden-dir", ".hidden", "visible")
	hiddenDirectories, err := listDirectoryPage(root, true, true, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	assertNames(hiddenDirectories, ".hidden-dir")
}

func TestListPaginationValidation(t *testing.T) {
	if _, _, err := listPagination(map[string]any{"offset": float64(-1), "limit": float64(100)}); err == nil {
		t.Fatal("negative offset accepted")
	}
	if _, _, err := listPagination(map[string]any{"limit": float64(maxDirectoryListPage + 1)}); err == nil {
		t.Fatal("unbounded page accepted")
	}
	offset, limit, err := listPagination(map[string]any{"offset": float64(1)})
	if err != nil || offset != 1 || limit != directoryListDefaultPage {
		t.Fatalf("default pagination = offset %d limit %d, err=%v", offset, limit, err)
	}
}
