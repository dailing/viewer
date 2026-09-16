// File watching: previewed files are watched server-side so external edits
// push one `file:_:changed` event instead of every client polling. Each open
// preview registers (path, watcher id) via file:_:watch; entries expire after
// watchTTL unless renewed, so crashed browsers never leak watches.
//
// Changes are detected two ways feeding one pipeline: fsnotify events on the
// file's parent directory (directory-level, so atomic-save rename and
// delete/recreate are both covered), debounced per path; plus a slow sweep
// that re-stats every watched path, covering silently missed events (queue
// overflow, network filesystems). Both end in verify: stat → cached hash →
// publish only when the digest actually changed.
package fileservice

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	defaultWatchTTL      = 120 * time.Second
	defaultWatchDebounce = 300 * time.Millisecond
	defaultWatchSweep    = 30 * time.Second
)

// ChangedChannel carries file-change events: {path, exists, sha256?, mtime?}.
const ChangedChannel = "file:_:changed"

// hashCache memoizes file digests by (size, mtime): an unchanged file costs
// one stat instead of a full re-read. Shared by the hash/resolve RPCs and the
// watcher.
type hashCache struct {
	mu      sync.Mutex
	entries map[string]hashCacheEntry
}

type hashCacheEntry struct {
	size    int64
	mtimeNs int64
	digest  string
}

func newHashCache() *hashCache {
	return &hashCache{entries: make(map[string]hashCacheEntry)}
}

func (c *hashCache) hash(path string, info os.FileInfo) (string, error) {
	size, mtimeNs := info.Size(), info.ModTime().UnixNano()
	c.mu.Lock()
	if entry, ok := c.entries[path]; ok && entry.size == size && entry.mtimeNs == mtimeNs {
		c.mu.Unlock()
		return entry.digest, nil
	}
	c.mu.Unlock()
	digest, err := sha256File(path)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.entries[path] = hashCacheEntry{size: size, mtimeNs: mtimeNs, digest: digest}
	c.mu.Unlock()
	return digest, nil
}

func (c *hashCache) drop(path string) {
	c.mu.Lock()
	delete(c.entries, path)
	c.mu.Unlock()
}

// watchState is the last-known published state of one watched path; add()
// returns it as the watch RPC's initial baseline.
type watchState struct {
	exists bool
	digest string
	mtime  int64
}

type watchedFile struct {
	watchers map[string]time.Time // watcher id → expiry
	state    watchState
	timer    *time.Timer // debounce
}

type fileWatcher struct {
	fsn     *fsnotify.Watcher
	cache   *hashCache
	publish func(path string, value map[string]any)

	ttl      time.Duration
	debounce time.Duration
	sweep    time.Duration

	mu    sync.Mutex
	files map[string]*watchedFile // absolute file path → state
	dirs  map[string]int          // watched parent dir → file count
	done  chan struct{}
}

func newFileWatcher(cache *hashCache, publish func(path string, value map[string]any)) (*fileWatcher, error) {
	fsn, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &fileWatcher{
		fsn:      fsn,
		cache:    cache,
		publish:  publish,
		ttl:      defaultWatchTTL,
		debounce: defaultWatchDebounce,
		sweep:    defaultWatchSweep,
		files:    make(map[string]*watchedFile),
		dirs:     make(map[string]int),
		done:     make(chan struct{}),
	}
	return w, nil
}

// start launches the event loop. Tests tune ttl/debounce/sweep before it.
func (w *fileWatcher) start() {
	go w.loop()
}

// add registers (path, watcher id), returning the path's current baseline
// state. Re-adding an existing pair renews its TTL. Missing files are
// watchable: the parent-dir watch fires when the file appears.
func (w *fileWatcher) add(path, watcherID string) (watchState, error) {
	dir := filepath.Dir(path)
	w.mu.Lock()
	entry, exists := w.files[path]
	if exists {
		entry.watchers[watcherID] = time.Now().Add(w.ttl)
		state := entry.state
		w.mu.Unlock()
		return state, nil
	}
	if w.dirs[dir] == 0 {
		if err := w.fsn.Add(dir); err != nil {
			w.mu.Unlock()
			return watchState{}, err
		}
	}
	w.dirs[dir]++
	entry = &watchedFile{watchers: make(map[string]time.Time)}
	entry.watchers[watcherID] = time.Now().Add(w.ttl)
	w.files[path] = entry
	w.mu.Unlock()
	// The baseline probe runs after registration so a concurrent event is not
	// lost; if verify wins the race it simply republishes once.
	state := w.probe(path)
	w.mu.Lock()
	entry.state = state
	w.mu.Unlock()
	return state, nil
}

func (w *fileWatcher) remove(path, watcherID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	entry, exists := w.files[path]
	if !exists {
		return
	}
	delete(entry.watchers, watcherID)
	if len(entry.watchers) == 0 {
		w.dropLocked(path)
	}
}

// dropLocked removes one watched file, releasing the dir watch when it was
// the last one there.
func (w *fileWatcher) dropLocked(path string) {
	entry := w.files[path]
	if entry.timer != nil {
		entry.timer.Stop()
	}
	delete(w.files, path)
	dir := filepath.Dir(path)
	w.dirs[dir]--
	if w.dirs[dir] == 0 {
		delete(w.dirs, dir)
		if err := w.fsn.Remove(dir); err != nil {
			slog.Warn("file-service unwatch dir failed", "dir", dir, "error", err)
		}
	}
}

func (w *fileWatcher) loop() {
	ticker := time.NewTicker(w.sweep)
	defer ticker.Stop()
	for {
		select {
		case <-w.done:
			return
		case event, ok := <-w.fsn.Events:
			if !ok {
				return
			}
			w.onEvent(event)
		case err, ok := <-w.fsn.Errors:
			if !ok {
				return
			}
			slog.Warn("file-service watcher error", "error", err)
		case <-ticker.C:
			w.sweepAll()
		}
	}
}

// onEvent debounces relevant fsnotify events per path. Directory-level
// watches report every entry in the dir; only watched file paths matter.
func (w *fileWatcher) onEvent(event fsnotify.Event) {
	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
		return
	}
	path := event.Name
	w.mu.Lock()
	entry, exists := w.files[path]
	if !exists {
		w.mu.Unlock()
		return
	}
	if entry.timer != nil {
		entry.timer.Stop()
	}
	entry.timer = time.AfterFunc(w.debounce, func() { w.verify(path, true) })
	w.mu.Unlock()
}

// sweepAll expires stale watcher ids and re-stats every watched path — the
// safety net for events inotify can never deliver (overflow, network mounts).
func (w *fileWatcher) sweepAll() {
	now := time.Now()
	var paths []string
	w.mu.Lock()
	for path, entry := range w.files {
		for id, expiry := range entry.watchers {
			if now.After(expiry) {
				delete(entry.watchers, id)
			}
		}
		if len(entry.watchers) == 0 {
			w.dropLocked(path)
			continue
		}
		paths = append(paths, path)
	}
	w.mu.Unlock()
	for _, path := range paths {
		w.verify(path, false)
	}
}

// probe stats and hashes a path without publishing — the baseline for a new
// watch.
func (w *fileWatcher) probe(path string) watchState {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return watchState{exists: false}
	}
	digest, err := w.cache.hash(path, info)
	if err != nil {
		return watchState{exists: false}
	}
	return watchState{exists: true, digest: digest, mtime: info.ModTime().Unix()}
}

// verify re-checks one path and publishes a change event only when the
// digest/existence actually flipped — mtime touches with identical content
// stay silent. force (event-driven checks) invalidates the hash cache first:
// filesystems with coarse mtime granularity can report an unchanged
// (size, mtime) pair for a genuinely rewritten file.
func (w *fileWatcher) verify(path string, force bool) {
	if force {
		w.cache.drop(path)
	}
	current := w.probe(path)
	if !current.exists {
		w.cache.drop(path)
	}
	w.mu.Lock()
	entry, exists := w.files[path]
	if !exists {
		w.mu.Unlock()
		return
	}
	previous := entry.state
	entry.state = current
	w.mu.Unlock()
	if previous.exists == current.exists && previous.digest == current.digest {
		return
	}
	value := map[string]any{"path": path, "exists": current.exists}
	if current.exists {
		value["sha256"] = current.digest
		value["mtime"] = current.mtime
	}
	w.publish(path, value)
}

func (w *fileWatcher) close() {
	close(w.done)
	_ = w.fsn.Close()
}
