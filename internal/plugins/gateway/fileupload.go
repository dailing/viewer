package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"viewer/sdk/go/busclient"
)

const uploadResolveTimeout = 10 * time.Second

type uploadFileInfo struct {
	Path      string
	Overwrite bool
}
type uploadResolver func(ctx context.Context, ticket string) (uploadFileInfo, error)

func (s *Server) resolveUploadTicket(ctx context.Context, ticket string) (uploadFileInfo, error) {
	if s.client == nil {
		return uploadFileInfo{}, errors.New("gateway bus client not running")
	}
	result, err := s.client.Request(ctx, "file:_:upload:resolve", map[string]any{"ticket": ticket}, uploadResolveTimeout)
	if err != nil {
		return uploadFileInfo{}, err
	}
	value, ok := result.(map[string]any)
	if !ok {
		return uploadFileInfo{}, errors.New("malformed upload ticket reply")
	}
	path, _ := value["path"].(string)
	overwrite, _ := value["overwrite"].(bool)
	if path == "" || !filepath.IsAbs(path) || !validGatewayUploadBase(filepath.Base(path)) {
		return uploadFileInfo{}, errors.New("malformed upload ticket path")
	}
	return uploadFileInfo{Path: path, Overwrite: overwrite}, nil
}

func validGatewayUploadBase(name string) bool {
	return name != "" && name != "." && name != ".." && name != string(filepath.Separator)
}

func uploadHTTPError(w http.ResponseWriter, err error) {
	var rpcErr *busclient.RPCError
	if errors.As(err, &rpcErr) && rpcErr.Code == "invalid_ticket" {
		http.Error(w, "invalid or expired ticket", http.StatusForbidden)
		return
	}
	http.Error(w, "upload ticket resolve failed", http.StatusBadGateway)
}

func (s *Server) serveFileUpload(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("ticket")
	if ticket == "" {
		http.Error(w, "missing ticket", http.StatusBadRequest)
		return
	}
	resolve := s.uploadResolver
	if resolve == nil {
		resolve = s.resolveUploadTicket
	}
	info, err := resolve(r.Context(), ticket)
	if err != nil {
		uploadHTTPError(w, err)
		return
	}
	if existing, statErr := os.Lstat(info.Path); statErr == nil && (!info.Overwrite || existing.IsDir()) {
		http.Error(w, "target already exists", http.StatusConflict)
		return
	}
	dir := filepath.Dir(info.Path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(info.Path)+".upload-*")
	if err != nil {
		http.Error(w, "cannot create upload temporary file", http.StatusInternalServerError)
		return
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	written, copyErr := io.Copy(temp, r.Body)
	if copyErr != nil {
		http.Error(w, "upload interrupted: "+copyErr.Error(), http.StatusBadRequest)
		return
	}
	if err = temp.Chmod(0o644); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		http.Error(w, "cannot finish upload", http.StatusInternalServerError)
		return
	}
	if info.Overwrite {
		err = os.Rename(tempPath, info.Path)
	} else {
		err = unix.Renameat2(unix.AT_FDCWD, tempPath, unix.AT_FDCWD, info.Path, unix.RENAME_NOREPLACE)
	}
	if err != nil {
		if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.EISDIR) || os.IsExist(err) {
			http.Error(w, "target already exists", http.StatusConflict)
		} else {
			http.Error(w, fmt.Sprintf("cannot publish upload: %v", err), http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"path": info.Path, "size": written})
}
