package fileservice

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

type publishedEvent struct {
	path  string
	value map[string]any
}

func newTestWatcher(t *testing.T) (*fileWatcher, chan publishedEvent) {
	t.Helper()
	events := make(chan publishedEvent, 16)
	watcher, err := newFileWatcher(newHashCache(), func(path string, value map[string]any) {
		events <- publishedEvent{path: path, value: value}
	})
	if err != nil {
		t.Fatalf("newFileWatcher: %v", err)
	}
	watcher.debounce = 20 * time.Millisecond
	watcher.sweep = time.Hour // tests trigger sweepAll explicitly
	t.Cleanup(watcher.close)
	return watcher, events
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func expectEvent(t *testing.T, events chan publishedEvent, path string, exists bool) map[string]any {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-events:
			if event.path != path {
				continue
			}
			gotExists, _ := event.value["exists"].(bool)
			if gotExists == exists {
				return event.value
			}
		case <-deadline:
			t.Fatalf("no event for %s (exists=%v) within 3s", path, exists)
		}
	}
}

func expectNoEvent(t *testing.T, events chan publishedEvent, path string) {
	t.Helper()
	select {
	case event := <-events:
		t.Fatalf("unexpected event for %s: %v", event.path, event.value)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestWatchDetectsModify(t *testing.T) {
	watcher, events := newTestWatcher(t)
	watcher.start()
	path := filepath.Join(t.TempDir(), "note.txt")
	writeFile(t, path, "one")

	state, err := watcher.add(path, "w1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !state.exists || state.digest == "" {
		t.Fatalf("baseline = %+v, want existing file with digest", state)
	}

	writeFile(t, path, "two")
	value := expectEvent(t, events, path, true)
	digest, _ := value["sha256"].(string)
	if digest == "" || digest == state.digest {
		t.Fatalf("event digest %q, want a new one (baseline %q)", digest, state.digest)
	}
}

func TestWatchAtomicReplace(t *testing.T) {
	watcher, events := newTestWatcher(t)
	watcher.start()
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	writeFile(t, path, "one")
	if _, err := watcher.add(path, "w1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Editor-style atomic save: write temp file, rename over the original.
	tmp := filepath.Join(dir, ".note.txt.tmp")
	writeFile(t, tmp, "two")
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename: %v", err)
	}
	expectEvent(t, events, path, true)
}

func TestWatchDeleteAndRecreate(t *testing.T) {
	watcher, events := newTestWatcher(t)
	watcher.start()
	path := filepath.Join(t.TempDir(), "note.txt")
	writeFile(t, path, "one")
	if _, err := watcher.add(path, "w1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	expectEvent(t, events, path, false)

	writeFile(t, path, "three")
	expectEvent(t, events, path, true)
}

func TestWatchBaselineForMissingFile(t *testing.T) {
	watcher, events := newTestWatcher(t)
	watcher.start()
	path := filepath.Join(t.TempDir(), "later.txt")

	state, err := watcher.add(path, "w1")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if state.exists {
		t.Fatalf("baseline = %+v, want missing file", state)
	}
	writeFile(t, path, "now exists")
	expectEvent(t, events, path, true)
}

func TestVerifySilentOnIdenticalContent(t *testing.T) {
	watcher, events := newTestWatcher(t)
	path := filepath.Join(t.TempDir(), "note.txt")
	writeFile(t, path, "same")
	if _, err := watcher.add(path, "w1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Same content, new mtime: verify must stay silent.
	writeFile(t, path, "same")
	watcher.verify(path, false)
	expectNoEvent(t, events, path)
}

func TestSweepExpiresWatchers(t *testing.T) {
	watcher, _ := newTestWatcher(t)
	watcher.ttl = 10 * time.Millisecond
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	writeFile(t, path, "one")
	if _, err := watcher.add(path, "w1"); err != nil {
		t.Fatalf("add: %v", err)
	}

	time.Sleep(30 * time.Millisecond)
	watcher.sweepAll()

	watcher.mu.Lock()
	_, fileGone := watcher.files[path]
	dirCount := watcher.dirs[dir]
	watcher.mu.Unlock()
	if fileGone {
		t.Fatal("expired watcher still registered")
	}
	if dirCount != 0 {
		t.Fatalf("dir watch count = %d, want 0 (released)", dirCount)
	}
}

func TestRemoveReleasesDirWatch(t *testing.T) {
	watcher, _ := newTestWatcher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	writeFile(t, path, "one")
	if _, err := watcher.add(path, "w1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	watcher.remove(path, "w1")
	watcher.mu.Lock()
	dirCount := watcher.dirs[dir]
	watcher.mu.Unlock()
	if dirCount != 0 {
		t.Fatalf("dir watch count = %d, want 0", dirCount)
	}
}

func TestHashCacheReusesUnchanged(t *testing.T) {
	cache := newHashCache()
	path := filepath.Join(t.TempDir(), "note.txt")
	writeFile(t, path, "one")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	first, err := cache.hash(path, info)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	second, err := cache.hash(path, info)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if first != second {
		t.Fatalf("cached digest changed: %q → %q", first, second)
	}

	// A different size guarantees recompute even on filesystems whose mtime
	// granularity cannot tell two immediate writes apart.
	writeFile(t, path, "two!")
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	third, err := cache.hash(path, info2)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if third == first {
		t.Fatal("digest not recomputed after content change")
	}

	cache.drop(path)
	fourth, err := cache.hash(path, info2)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if fourth != third {
		t.Fatalf("digest changed after drop with same file: %q → %q", third, fourth)
	}
}
