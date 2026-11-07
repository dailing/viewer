package fileservice

import (
	"errors"
	"os"
	"path/filepath"

	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

func makeDirectory(dir, name string) (fileEntry, string, error) {
	if !validUploadName(name) {
		return fileEntry{}, "invalid_request", errors.New("name must be a basename without path separators")
	}
	resolved, ok := resolveRequestPath(dir)
	if !ok {
		return fileEntry{}, "invalid_request", errors.New("invalid directory")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return fileEntry{}, "not_found", err
		}
		return fileEntry{}, "read_error", err
	}
	if !info.IsDir() {
		return fileEntry{}, "not_directory", errors.New("path is not a directory: " + resolved)
	}
	target := filepath.Join(resolved, name)
	if err := os.Mkdir(target, 0o755); err != nil {
		if os.IsExist(err) {
			return fileEntry{}, "already_exists", err
		}
		return fileEntry{}, "write_error", err
	}
	return entryFor(target), "", nil
}

func deleteFileTree(path string) (string, error) {
	clean := filepath.Clean(path)
	volumeRoot := filepath.VolumeName(clean) + string(os.PathSeparator)
	if clean == string(os.PathSeparator) || clean == volumeRoot {
		return "invalid_request", errors.New("refusing to delete a filesystem root")
	}
	info, err := os.Lstat(clean)
	if err != nil {
		if os.IsNotExist(err) {
			return "not_found", err
		}
		return "delete_error", err
	}
	if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		if err := os.RemoveAll(clean); err != nil {
			return "delete_error", err
		}
		return "", nil
	}
	if err := os.Remove(clean); err != nil {
		return "delete_error", err
	}
	return "", nil
}

func (p *Plugin) mkdir(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	value, ok := pluginrpc.Object(frame)
	dir, _ := value["dir"].(string)
	name, _ := value["name"].(string)
	if !ok {
		p.replyError(frame, "invalid_request", "missing required fields: dir, name")
		return
	}
	entry, code, err := makeDirectory(dir, name)
	if err != nil {
		p.replyError(frame, code, err.Error())
		return
	}
	p.reply(frame, entry)
}

func (p *Plugin) delete(frame busclient.Frame) {
	if pluginrpc.Cancelled(frame) {
		return
	}
	path, ok := requestPath(frame)
	if !ok {
		p.replyError(frame, "invalid_request", "missing required field: path")
		return
	}
	if code, err := deleteFileTree(path); err != nil {
		p.replyError(frame, code, err.Error())
		return
	}
	p.reply(frame, map[string]any{"path": path, "deleted": true})
}
