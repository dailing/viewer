// PushAssets uploads a plugin's built frontend bundle to the gateway's
// content-addressed asset store (framework section 14.3). Call it after
// Connect — the shell loads the bundle from the plugins:_:assets mailbox
// without a page refresh. Re-push after a rebuild: the changed content hash
// hot-reloads open panes.
//
// Wire mode is chosen automatically: a single assets:push RPC when the
// base64 payload fits one kernel frame, otherwise the begin/file/commit
// chunked sequence (large files are split with append:true).
package busclient

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// frameBudget leaves headroom under the kernel's 1 MiB frame cap for the
// envelope and field names.
const (
	assetsOneShotBudget = 700 * 1024 // total base64 bytes
	assetsChunkBudget   = 480 * 1024 // per file-op, decoded bytes
)

// PushAssets uploads every file under dir (relative paths, dotfiles and the
// gateway's own entry.json skipped) as the plugin's frontend bundle. dir
// must contain the bundle entry frontend.js. manifest is optional display
// metadata ({name, icon, description}) carried in the assets mailbox.
func PushAssets(ctx context.Context, client *Client, dir string, manifest map[string]any) (any, error) {
	files, err := readBundle(dir)
	if err != nil {
		return nil, err
	}
	total := 0
	for _, data := range files {
		total += len(base64.StdEncoding.EncodeToString(data))
	}
	if total <= assetsOneShotBudget {
		payload := make([]map[string]any, 0, len(files))
		for _, path := range sortedKeys(files) {
			payload = append(payload, map[string]any{"path": path, "data_b64": base64.StdEncoding.EncodeToString(files[path])})
		}
		return client.Request(ctx, "gateway:_:assets:push", map[string]any{"files": payload, "manifest": manifest}, 30*time.Second)
	}
	begin, err := client.Request(ctx, "gateway:_:assets:push", map[string]any{"op": "begin", "manifest": manifest}, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("assets begin: %w", err)
	}
	uploadID, _ := begin.(map[string]any)["upload_id"].(string)
	if uploadID == "" {
		return nil, errors.New("assets begin returned no upload_id")
	}
	for _, path := range sortedKeys(files) {
		data := files[path]
		for offset := 0; offset < len(data); offset += assetsChunkBudget {
			end := offset + assetsChunkBudget
			if end > len(data) {
				end = len(data)
			}
			op := map[string]any{"op": "file", "upload_id": uploadID, "path": path, "data_b64": base64.StdEncoding.EncodeToString(data[offset:end])}
			if offset > 0 {
				op["append"] = true
			}
			if _, err := client.Request(ctx, "gateway:_:assets:push", op, 30*time.Second); err != nil {
				return nil, fmt.Errorf("assets file %s: %w", path, err)
			}
		}
	}
	return client.Request(ctx, "gateway:_:assets:push", map[string]any{"op": "commit", "upload_id": uploadID}, 30*time.Second)
}

// readBundle walks dir and returns relative slash-path → content.
func readBundle(dir string) (map[string][]byte, error) {
	if _, err := os.Stat(filepath.Join(dir, "frontend.js")); err != nil {
		return nil, fmt.Errorf("bundle entry frontend.js missing in %s", dir)
	}
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if strings.HasPrefix(name, ".") && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || name == "entry.json" {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	return files, err
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
