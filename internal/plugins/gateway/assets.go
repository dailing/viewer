// Plugin frontend asset pipeline (framework section 14.3): plugins push
// their built frontend bundle over the bus after hello; the gateway stores
// the bytes in a content-addressed library and serves them same-origin at
// /plugins/<id>/assets/<hash>/. The current id → bundle mapping lives in the
// plugins:_:assets mailbox, which the shell subscribes to for dynamic
// loading (framework section 8.6).
//
// Wire contract of gateway:_:assets:push (origin plugin id binds the asset
// id — a plugin can only publish its own bundle):
//
//	one-shot: {files: [{path, data_b64}], manifest?}            → entry
//	chunked:  {op: "begin", manifest?}                          → {upload_id}
//	          {op: "file", upload_id, path, data_b64, append?}    → {received}
//	          {op: "commit", upload_id}                         → entry
//
// Chunked exists because kernel frames cap at 1 MiB; SDKs pick the mode
// automatically and split oversized files into append:true chunks. gateway:_:assets:remove {id} drops a plugin's assets
// (plugin-manager delete flow); the mailbox update unloads the frontend.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

// assetEntry is one plugin's live bundle record — the value shape of the
// plugins:_:assets mailbox and of entry.json inside each hash directory.
type assetEntry struct {
	URL       string         `json:"url"` // /plugins/<id>/assets/<hash>/
	Entry     string         `json:"entry"`
	Hash      string         `json:"hash"`
	Files     []string       `json:"files"`
	Manifest  map[string]any `json:"manifest,omitempty"`
	UpdatedAt int64          `json:"updated_at"`
}

// uploadSession accumulates a chunked push between begin and commit.
type uploadSession struct {
	manifest map[string]any
	files    map[string][]byte
	created  time.Time
}

const (
	assetEntryFile       = "frontend.js" // bundle entry convention (framework section 14.1)
	assetEntryMeta       = "entry.json"
	assetMaxFileBytes    = 32 << 20 // decoded per-file cap
	assetMaxTotalBytes   = 64 << 20 // decoded per-push cap
	assetKeepGenerations = 3
	uploadExpiry         = 5 * time.Minute
)

var (
	assetIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)
	assetHashPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)
	assetPathPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,255}$`)
)

var assetsManifest = busclient.Manifest{
	ID: "gateway", Version: "0.1.0",
	Slots: map[string]any{"assets:push": map[string]any{}, "assets:remove": map[string]any{}},
	Emits: map[string]any{"assets": map[string]any{}},
}

// assetsMu guards assets, uploads.
type assetsState struct {
	mu      sync.Mutex
	assets  map[string]*assetEntry
	uploads map[string]*uploadSession
}

// startAssets connects the gateway's own bus client (assets RPC endpoint)
// and rebuilds the mailbox from the on-disk library so bundles survive a
// gateway restart. No-op when AssetsDir is unset.
func (s *Server) startAssets(ctx context.Context) error {
	if s.config.AssetsDir == "" {
		return nil
	}
	if err := os.MkdirAll(s.config.AssetsDir, 0o755); err != nil {
		return fmt.Errorf("create plugin-assets dir: %w", err)
	}
	s.assetsState = &assetsState{assets: map[string]*assetEntry{}, uploads: map[string]*uploadSession{}}
	s.loadAssetsFromDisk()
	s.client = busclient.New(s.config.KernelWS, assetsManifest)
	if _, err := s.client.Subscribe("gateway:_:assets:push", s.handleAssetsPush); err != nil {
		return err
	}
	if _, err := s.client.Subscribe("gateway:_:assets:remove", s.handleAssetsRemove); err != nil {
		return err
	}
	if err := s.client.Connect(ctx); err != nil {
		return fmt.Errorf("connect gateway assets client: %w", err)
	}
	s.publishAssets()
	return nil
}

func (s *Server) stopAssets() {
	if s.client != nil {
		_ = s.client.Close()
	}
}

// ---------- bus RPC handlers ----------

func (s *Server) handleAssetsPush(frame busclient.Frame) {
	value, ok := pluginrpc.Object(frame)
	if !ok || pluginrpc.Cancelled(frame) {
		return
	}
	id := ""
	if frame.Origin != nil {
		id = frame.Origin.Plugin
	}
	if id == "" {
		id, _ = value["id"].(string) // in-process callers without origin
	}
	if !assetIDPattern.MatchString(id) {
		_ = pluginrpc.RespondError(s.client, frame, "bad_request", "valid plugin id required (origin or id field)")
		return
	}
	s.expireUploads()
	op, _ := value["op"].(string)
	switch op {
	case "":
		manifest, _ := value["manifest"].(map[string]any)
		files, err := decodeAssetFiles(value["files"])
		if err != nil {
			_ = pluginrpc.RespondError(s.client, frame, "bad_request", err.Error())
			return
		}
		entry, err := s.commitAssets(id, manifest, files)
		s.replyAssets(frame, entry, err)
	case "begin":
		manifest, _ := value["manifest"].(map[string]any)
		uploadID := newUploadID()
		s.assetsState.mu.Lock()
		s.assetsState.uploads[uploadID] = &uploadSession{manifest: manifest, files: map[string][]byte{}, created: time.Now()}
		s.assetsState.mu.Unlock()
		_ = pluginrpc.Respond(s.client, frame, map[string]any{"upload_id": uploadID})
	case "file":
		uploadID, _ := value["upload_id"].(string)
		s.assetsState.mu.Lock()
		session := s.assetsState.uploads[uploadID]
		s.assetsState.mu.Unlock()
		if session == nil {
			_ = pluginrpc.RespondError(s.client, frame, "not_found", "unknown or expired upload_id")
			return
		}
		path, _ := value["path"].(string)
		data, _ := value["data_b64"].(string)
		decoded, err := decodeAssetFile(path, data)
		if err != nil {
			_ = pluginrpc.RespondError(s.client, frame, "bad_request", err.Error())
			return
		}
		total := len(decoded)
		for _, existing := range session.files {
			total += len(existing)
		}
		if total > assetMaxTotalBytes {
			_ = pluginrpc.RespondError(s.client, frame, "too_large", "push exceeds the 64 MiB total cap")
			return
		}
		s.assetsState.mu.Lock()
		if appendChunk, _ := value["append"].(bool); appendChunk && session.files[path] != nil {
			// Intra-file chunking: SDKs split files whose base64 would
			// exceed one kernel frame into sequential file ops.
			session.files[path] = append(session.files[path], decoded...)
		} else {
			session.files[path] = decoded
		}
		session.created = time.Now()
		s.assetsState.mu.Unlock()
		_ = pluginrpc.Respond(s.client, frame, map[string]any{"received": len(decoded)})
	case "commit":
		uploadID, _ := value["upload_id"].(string)
		s.assetsState.mu.Lock()
		session := s.assetsState.uploads[uploadID]
		delete(s.assetsState.uploads, uploadID)
		s.assetsState.mu.Unlock()
		if session == nil {
			_ = pluginrpc.RespondError(s.client, frame, "not_found", "unknown or expired upload_id")
			return
		}
		entry, err := s.commitAssets(id, session.manifest, session.files)
		s.replyAssets(frame, entry, err)
	default:
		_ = pluginrpc.RespondError(s.client, frame, "bad_request", "unknown op: "+op)
	}
}

func (s *Server) handleAssetsRemove(frame busclient.Frame) {
	value, ok := pluginrpc.Object(frame)
	if !ok || pluginrpc.Cancelled(frame) {
		return
	}
	id, _ := value["id"].(string)
	if !assetIDPattern.MatchString(id) {
		_ = pluginrpc.RespondError(s.client, frame, "bad_request", "id is required")
		return
	}
	s.assetsState.mu.Lock()
	_, existed := s.assetsState.assets[id]
	delete(s.assetsState.assets, id)
	s.assetsState.mu.Unlock()
	if existed {
		_ = os.RemoveAll(filepath.Join(s.config.AssetsDir, id))
		s.publishAssets()
	}
	_ = pluginrpc.Respond(s.client, frame, map[string]any{"removed": existed})
}

func (s *Server) replyAssets(frame busclient.Frame, entry *assetEntry, err error) {
	if err != nil {
		_ = pluginrpc.RespondError(s.client, frame, "push_failed", err.Error())
		return
	}
	_ = pluginrpc.Respond(s.client, frame, entry)
}

// ---------- store ----------

// commitAssets writes one bundle generation into the content-addressed
// library, prunes old generations, and updates the mailbox.
func (s *Server) commitAssets(id string, manifest map[string]any, files map[string][]byte) (*assetEntry, error) {
	if len(files) == 0 {
		return nil, errors.New("no files pushed")
	}
	entryData, ok := files[assetEntryFile]
	if !ok || len(entryData) == 0 {
		return nil, fmt.Errorf("bundle entry %s is required", assetEntryFile)
	}
	hasher := sha256.New()
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		hasher.Write([]byte(path))
		hasher.Write([]byte{0})
		hasher.Write(files[path])
		hasher.Write([]byte{0})
	}
	hash := hex.EncodeToString(hasher.Sum(nil))[:16]
	dir := filepath.Join(s.config.AssetsDir, id, hash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for _, path := range paths {
		target := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(target, files[path], 0o644); err != nil {
			return nil, err
		}
	}
	entry := &assetEntry{
		URL: "/plugins/" + id + "/assets/" + hash + "/", Entry: assetEntryFile,
		Hash: hash, Files: paths, Manifest: manifest, UpdatedAt: time.Now().UnixMilli(),
	}
	meta, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, assetEntryMeta), meta, 0o644); err != nil {
		return nil, err
	}
	s.pruneGenerations(id, assetKeepGenerations)
	s.assetsState.mu.Lock()
	s.assetsState.assets[id] = entry
	s.assetsState.mu.Unlock()
	s.publishAssets()
	slog.Info("plugin assets committed", "plugin", id, "hash", hash, "files", len(paths))
	return entry, nil
}

// pruneGenerations keeps the newest `keep` hash directories of a plugin.
func (s *Server) pruneGenerations(id string, keep int) {
	base := filepath.Join(s.config.AssetsDir, id)
	entries, err := os.ReadDir(base)
	if err != nil || len(entries) <= keep {
		return
	}
	type generation struct {
		name    string
		modTime time.Time
	}
	generations := make([]generation, 0, len(entries))
	for _, item := range entries {
		if !item.IsDir() {
			continue
		}
		info, err := item.Info()
		if err != nil {
			continue
		}
		generations = append(generations, generation{name: item.Name(), modTime: info.ModTime()})
	}
	sort.Slice(generations, func(i, j int) bool { return generations[i].modTime.After(generations[j].modTime) })
	for _, old := range generations[keep:] {
		_ = os.RemoveAll(filepath.Join(base, old.name))
	}
}

// loadAssetsFromDisk rebuilds the in-memory map from the newest generation
// of each plugin, so a gateway restart keeps serving known bundles.
func (s *Server) loadAssetsFromDisk() {
	plugins, err := os.ReadDir(s.config.AssetsDir)
	if err != nil {
		return
	}
	for _, pluginDir := range plugins {
		if !pluginDir.IsDir() {
			continue
		}
		generations, err := os.ReadDir(filepath.Join(s.config.AssetsDir, pluginDir.Name()))
		if err != nil {
			continue
		}
		var newest os.DirEntry
		var newestTime time.Time
		for _, generation := range generations {
			if !generation.IsDir() {
				continue
			}
			info, err := generation.Info()
			if err == nil && info.ModTime().After(newestTime) {
				newest, newestTime = generation, info.ModTime()
			}
		}
		if newest == nil {
			continue
		}
		meta, err := os.ReadFile(filepath.Join(s.config.AssetsDir, pluginDir.Name(), newest.Name(), assetEntryMeta))
		if err != nil {
			continue
		}
		var entry assetEntry
		if json.Unmarshal(meta, &entry) == nil && entry.Hash == newest.Name() {
			s.assetsState.assets[pluginDir.Name()] = &entry
		}
	}
}

// publishAssets replaces the plugins:_:assets mailbox with the full map.
func (s *Server) publishAssets() {
	if s.client == nil || !s.client.Connected() {
		return
	}
	s.assetsState.mu.Lock()
	value := make(map[string]any, len(s.assetsState.assets))
	for id, entry := range s.assetsState.assets {
		value[id] = entry
	}
	s.assetsState.mu.Unlock()
	if err := s.client.Set(context.Background(), "plugins:_:assets", value); err != nil {
		slog.Warn("publish plugins:_:assets failed", "error", err)
	}
}

func (s *Server) expireUploads() {
	s.assetsState.mu.Lock()
	defer s.assetsState.mu.Unlock()
	for id, session := range s.assetsState.uploads {
		if time.Since(session.created) > uploadExpiry {
			delete(s.assetsState.uploads, id)
		}
	}
}

// ---------- validation / decoding ----------

func decodeAssetFiles(raw any) (map[string][]byte, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, errors.New("files must be an array of {path, data_b64}")
	}
	files := make(map[string][]byte, len(list))
	total := 0
	for _, item := range list {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("files entries must be objects")
		}
		path, _ := object["path"].(string)
		data, _ := object["data_b64"].(string)
		decoded, err := decodeAssetFile(path, data)
		if err != nil {
			return nil, err
		}
		total += len(decoded)
		if total > assetMaxTotalBytes {
			return nil, errors.New("push exceeds the 64 MiB total cap")
		}
		files[path] = decoded
	}
	return files, nil
}

func decodeAssetFile(path, dataB64 string) ([]byte, error) {
	if !assetPathPattern.MatchString(path) || strings.Contains(path, "..") || strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("invalid asset path: %q", path)
	}
	decoded, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return nil, fmt.Errorf("asset %s is not valid base64: %w", path, err)
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("asset %s is empty", path)
	}
	if len(decoded) > assetMaxFileBytes {
		return nil, fmt.Errorf("asset %s exceeds the 32 MiB file cap", path)
	}
	return decoded, nil
}

func newUploadID() string {
	value, err := uuidV4()
	if err != nil {
		return fmt.Sprintf("upload-%d", time.Now().UnixNano())
	}
	return value
}

// ---------- HTTP ----------

// servePluginAsset serves /plugins/<id>/assets/<hash>/<path> from the
// content-addressed library. Hash-addressed URLs are immutable.
func (s *Server) servePluginAsset(w http.ResponseWriter, r *http.Request) {
	if s.config.AssetsDir == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/plugins/")
	parts := strings.SplitN(rest, "/", 4)
	// <id> / assets / <hash> / <path...>
	if len(parts) != 4 || parts[1] != "assets" || !assetIDPattern.MatchString(parts[0]) || !assetHashPattern.MatchString(parts[2]) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	rel := parts[3]
	if !assetPathPattern.MatchString(rel) || strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") || strings.HasSuffix(rel, assetEntryMeta) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	full := filepath.Join(s.config.AssetsDir, parts[0], parts[2], filepath.FromSlash(rel))
	data, err := os.ReadFile(full)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(rel))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(data)
}
