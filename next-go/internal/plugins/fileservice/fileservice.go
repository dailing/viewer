// Package fileservice implements unrestricted local path metadata and reads.
package fileservice

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"viewer/internal/busclient"
	"viewer/internal/plugins/pluginrpc"
)

const DefaultMaxReadBytes int64 = 1024 * 1024

var Manifest = busclient.Manifest{
	ID: "file-service", Version: "0.1.0",
	Slots: map[string]any{"resolve": map[string]any{}, "read": map[string]any{}, "hash": map[string]any{}},
	Emits: map[string]any{},
}

type Plugin struct{ client *busclient.Client }

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Start(ctx context.Context, kernelWS string, managed bool) error {
	client := busclient.New(kernelWS, Manifest, busclient.WithManaged(managed))
	for pattern, handler := range map[string]func(busclient.Frame){
		"file:_:resolve": p.resolve,
		"file:_:read":    p.read,
		"file:_:hash":    p.hash,
	} {
		if _, err := client.Subscribe(pattern, handler); err != nil {
			_ = client.Close()
			return err
		}
	}
	p.client = client
	if err := client.Connect(ctx); err != nil {
		p.client = nil
		_ = client.Close()
		return err
	}
	slog.Info("file-service started")
	return nil
}

func (p *Plugin) Close() error {
	if p.client == nil {
		return nil
	}
	return p.client.Close()
}

func (p *Plugin) resolve(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	path, ok := requestPath(frame)
	if !ok {
		p.replyError(frame, "invalid_request", "missing required field: path")
		return
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		p.replyError(frame, "not_found", "no such file: "+path)
		return
	}
	if err != nil {
		p.replyError(frame, "read_error", err.Error())
		return
	}
	var digest any
	if info.Mode().IsRegular() {
		hash, hashErr := sha256File(path)
		if hashErr != nil {
			p.replyError(frame, "read_error", hashErr.Error())
			return
		}
		digest = hash
	}
	p.reply(frame, map[string]any{
		"path": path, "exists": true, "is_dir": info.IsDir(),
		"size": info.Size(), "mtime": info.ModTime().Unix(), "sha256": digest,
	})
}

func (p *Plugin) read(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	path, ok := requestPath(frame)
	if !ok {
		p.replyError(frame, "invalid_request", "missing required field: path")
		return
	}
	value, _ := pluginrpc.Object(frame)
	maxBytes, validLimit := maxReadBytes(value)
	if !validLimit {
		p.replyError(frame, "invalid_request", "max_bytes must be an integer")
		return
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		p.replyError(frame, "not_found", "no such file: "+path)
		return
	}
	if err != nil {
		p.replyError(frame, "read_error", err.Error())
		return
	}
	if info.Size() > maxBytes {
		p.replyError(frame, "too_large", fmt.Sprintf(
			"%s is %d bytes, above the %d-byte inline cap; use the gateway by-reference data plane",
			path, info.Size(), maxBytes,
		))
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		p.replyError(frame, "read_error", err.Error())
		return
	}
	encoding := "utf-8"
	content := string(raw)
	if !utf8.Valid(raw) {
		encoding = "base64"
		content = base64.StdEncoding.EncodeToString(raw)
	}
	p.reply(frame, map[string]any{
		"path": path, "size": info.Size(), "encoding": encoding, "content": content,
	})
}

func (p *Plugin) hash(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	path, ok := requestPath(frame)
	if !ok {
		p.replyError(frame, "invalid_request", "missing required field: path")
		return
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) || err == nil && !info.Mode().IsRegular() {
		p.replyError(frame, "not_found", "no such file: "+path)
		return
	}
	if err != nil {
		p.replyError(frame, "read_error", err.Error())
		return
	}
	digest, err := sha256File(path)
	if err != nil {
		p.replyError(frame, "read_error", err.Error())
		return
	}
	p.reply(frame, map[string]any{"path": path, "sha256": digest})
}

func requestPath(frame busclient.Frame) (string, bool) {
	value, ok := pluginrpc.Object(frame)
	if !ok {
		return "", false
	}
	raw, ok := value["path"].(string)
	if !ok || raw == "" {
		return "", false
	}
	expanded, err := expandUser(raw)
	if err != nil {
		return "", false
	}
	absolute, err := filepath.Abs(expanded)
	if err != nil {
		return "", false
	}
	return absolute, true
}

func expandUser(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
}

func maxReadBytes(value map[string]any) (int64, bool) {
	raw, exists := value["max_bytes"]
	if !exists {
		return DefaultMaxReadBytes, true
	}
	switch number := raw.(type) {
	case float64:
		if number != float64(int64(number)) {
			return 0, false
		}
		return int64(number), true
	case string:
		parsed, err := strconv.ParseInt(number, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	buffer := make([]byte, 64*1024)
	for {
		count, readErr := file.Read(buffer)
		if count > 0 {
			_, _ = digest.Write(buffer[:count])
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return "", readErr
		}
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func (p *Plugin) reply(frame busclient.Frame, result any) {
	if err := pluginrpc.Respond(p.client, frame, result); err != nil {
		slog.Error("file-service RPC response failed", "error", err)
	}
}

func (p *Plugin) replyError(frame busclient.Frame, code, message string) {
	if err := pluginrpc.RespondError(p.client, frame, code, message); err != nil {
		slog.Error("file-service RPC error response failed", "error", err)
	}
}
