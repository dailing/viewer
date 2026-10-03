// /api/files/raw: the data-plane half of framework §6.2 — large file bytes
// never cross the bus; the browser holds a short-lived ticket (issued by
// file:_:ticket) and the gateway streams the file over HTTP. The control
// plane (ticket resolution) is one small bus RPC to file-service.
package gateway

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"viewer/sdk/go/busclient"
)

const rawResolveTimeout = 10 * time.Second

// rawFileInfo is the resolved form of a file ticket.
type rawFileInfo struct {
	Path string
	MIME string
}

// rawResolver resolves a ticket to a file; a field so tests can stub the bus.
type rawResolver func(ctx context.Context, ticket string) (rawFileInfo, error)

// resolveRawTicket asks file-service to validate the ticket and hand back the
// bound path (small control-plane payload — the bytes stay off the bus).
func (s *Server) resolveRawTicket(ctx context.Context, ticket string) (rawFileInfo, error) {
	if s.client == nil {
		return rawFileInfo{}, errors.New("gateway bus client not running")
	}
	result, err := s.client.Request(ctx, "file:_:ticket:resolve", map[string]any{"ticket": ticket}, rawResolveTimeout)
	if err != nil {
		return rawFileInfo{}, err
	}
	value, ok := result.(map[string]any)
	if !ok {
		return rawFileInfo{}, errors.New("malformed ticket resolve reply")
	}
	path, _ := value["path"].(string)
	mime, _ := value["mime"].(string)
	if path == "" {
		return rawFileInfo{}, errors.New("malformed ticket resolve reply")
	}
	return rawFileInfo{Path: path, MIME: mime}, nil
}

func (s *Server) serveFileRaw(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("ticket")
	if ticket == "" {
		http.Error(w, "missing ticket", http.StatusBadRequest)
		return
	}
	resolve := s.rawResolver
	if resolve == nil {
		resolve = s.resolveRawTicket
	}
	info, err := resolve(r.Context(), ticket)
	if err != nil {
		var rpcErr *busclient.RPCError
		switch {
		case errors.As(err, &rpcErr) && rpcErr.Code == "invalid_ticket":
			http.Error(w, "invalid or expired ticket", http.StatusForbidden)
		case errors.As(err, &rpcErr) && rpcErr.Code == "not_found":
			http.Error(w, "file not found", http.StatusNotFound)
		default:
			http.Error(w, "ticket resolve failed", http.StatusBadGateway)
		}
		return
	}
	file, err := os.Open(info.Path)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer func() { _ = file.Close() }()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	if info.MIME != "" {
		// Sniffing gets SVG wrong for <img>; trust the issuer's mapping.
		w.Header().Set("Content-Type", info.MIME)
	}
	// ServeContent adds Range, If-Modified-Since and sniffing fallbacks.
	http.ServeContent(w, r, filepath.Base(info.Path), stat.ModTime(), file)
}
