// Package gateway implements the C4 HTTP gateway core plugin.
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"viewer/internal/protocol"
)

const helloTimeout = 10 * time.Second

var manifest = protocol.Manifest{
	ID: "gateway", Version: "0.1.0", Slots: map[string]any{}, Emits: map[string]any{},
}

// Config configures a gateway server. StaticFS is intentionally an fs.FS so
// the single-binary assembly can later supply an embed.FS without changing the
// gateway API. TODO(milestone embed): wire the frontend's embedded subtree.
type Config struct {
	KernelWS     string
	Host         string
	Port         int
	StaticFS     fs.FS
	PingInterval time.Duration
}

func DefaultConfig() Config {
	return Config{Host: "127.0.0.1", Port: 18730, PingInterval: 30 * time.Second}
}

// Server serves browser WebSockets and static files on one HTTP port.
type Server struct {
	config Config

	listener net.Listener
	http     *http.Server
	serveErr chan error

	mu      sync.Mutex
	sockets map[*websocket.Conn]struct{}
}

func New(config Config) *Server {
	defaults := DefaultConfig()
	if config.Host == "" {
		config.Host = defaults.Host
	}
	if config.PingInterval <= 0 {
		config.PingInterval = defaults.PingInterval
	}
	return &Server{config: config, serveErr: make(chan error, 1), sockets: make(map[*websocket.Conn]struct{})}
}

func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return net.JoinHostPort(s.config.Host, strconv.Itoa(s.config.Port))
}

func (s *Server) Port() int {
	if s.listener != nil {
		return s.listener.Addr().(*net.TCPAddr).Port
	}
	return s.config.Port
}

func (s *Server) Start() error {
	if s.config.KernelWS == "" {
		return errors.New("kernel WebSocket URL is required")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(s.config.Host, strconv.Itoa(s.config.Port)))
	if err != nil {
		return err
	}
	s.listener = listener
	s.http = &http.Server{Handler: http.HandlerFunc(s.serveHTTP), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		err := s.http.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			err = nil
		}
		s.serveErr <- err
	}()
	slog.Info("gateway listening", "address", "http://"+s.Addr(), "kernel_ws", s.config.KernelWS)
	return nil
}

func (s *Server) Wait() error { return <-s.serveErr }

func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	sockets := make([]*websocket.Conn, 0, len(s.sockets))
	for socket := range s.sockets {
		sockets = append(sockets, socket)
	}
	s.mu.Unlock()
	for _, socket := range sockets {
		_ = socket.Close(websocket.StatusGoingAway, "gateway shutting down")
	}
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/ws" {
		s.serveBrowser(w, r)
		return
	}
	s.serveStatic(w, r)
}

func (s *Server) serveBrowser(w http.ResponseWriter, r *http.Request) {
	browser, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	browser.SetReadLimit(-1)
	s.mu.Lock()
	s.sockets[browser] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sockets, browser)
		s.mu.Unlock()
		browser.CloseNow()
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	helloCtx, helloCancel := context.WithTimeout(ctx, helloTimeout)
	kind, raw, err := browser.Read(helloCtx)
	helloCancel()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			_ = browser.Close(websocket.StatusCode(4002), "first frame must be hello")
		}
		return
	}
	protocolVersion, conn, err := browserHello(kind, raw)
	if err != nil {
		_ = browser.Close(websocket.StatusCode(4002), err.Error())
		return
	}

	dialCtx, dialCancel := context.WithTimeout(ctx, helloTimeout)
	kernel, _, err := websocket.Dial(dialCtx, s.config.KernelWS, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	dialCancel()
	if err != nil {
		_ = browser.Close(websocket.StatusServiceRestart, "kernel unavailable, retry")
		return
	}
	defer kernel.CloseNow()
	kernel.SetReadLimit(-1)
	kernelHello := struct {
		Type            string            `json:"type"`
		ProtocolVersion int               `json:"protocol_version"`
		Conn            string            `json:"conn"`
		Manifest        protocol.Manifest `json:"manifest"`
		Managed         bool              `json:"managed"`
		InstanceID      string            `json:"instance_id"`
	}{"hello", protocolVersion, conn, manifest, false, conn}
	encoded, err := json.Marshal(kernelHello)
	if err != nil || kernel.Write(ctx, websocket.MessageText, encoded) != nil {
		_ = browser.Close(websocket.StatusServiceRestart, "kernel unavailable, retry")
		return
	}

	type relayResult struct {
		fromKernel bool
		err        error
	}
	results := make(chan relayResult, 2)
	go func() { results <- relayResult{false, relay(ctx, browser, kernel)} }()
	go func() { results <- relayResult{true, relay(ctx, kernel, browser)} }()
	pingDone := make(chan struct{})
	go pingLoop(ctx, s.config.PingInterval, pingDone, browser, kernel)
	result := <-results
	if result.fromKernel {
		code, reason := closeDetails(result.err, websocket.StatusGoingAway)
		_ = browser.Close(code, reason)
	} else {
		code, reason := closeDetails(result.err, websocket.StatusNormalClosure)
		_ = kernel.Close(code, reason)
	}
	// coder/websocket treats cancellation of a Read context as an immediate
	// transport close. Complete the downstream close handshake first so an
	// application close code such as the kernel's 4009 reaches the browser.
	cancel()
	<-pingDone
}

func browserHello(kind websocket.MessageType, raw []byte) (int, string, error) {
	if kind != websocket.MessageText {
		return 0, "", errors.New("first frame must be hello")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return 0, "", errors.New("first frame must be hello")
	}
	var frameType string
	if json.Unmarshal(object["type"], &frameType) != nil || frameType != "hello" {
		return 0, "", errors.New("first frame must be hello")
	}
	version := protocol.Version
	if rawVersion, exists := object["protocol_version"]; exists {
		if json.Unmarshal(rawVersion, &version) != nil {
			return 0, "", errors.New("protocol_version must be an integer")
		}
	}
	conn, err := uuidV4()
	if err != nil {
		return 0, "", errors.New("cannot generate conn UUID")
	}
	if rawConn, exists := object["conn"]; exists {
		if json.Unmarshal(rawConn, &conn) != nil || !isUUIDV4(conn) {
			return 0, "", errors.New("conn must be a UUIDv4 string")
		}
	}
	return version, conn, nil
}

func relay(ctx context.Context, source, target *websocket.Conn) error {
	for {
		kind, data, err := source.Read(ctx)
		if err != nil {
			return err
		}
		if err := target.Write(ctx, kind, data); err != nil {
			return err
		}
	}
}

func pingLoop(ctx context.Context, interval time.Duration, done chan<- struct{}, sockets ...*websocket.Conn) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, socket := range sockets {
				pingCtx, cancel := context.WithTimeout(ctx, interval)
				_ = socket.Ping(pingCtx)
				cancel()
			}
		}
	}
}

func closeDetails(err error, fallback websocket.StatusCode) (websocket.StatusCode, string) {
	var closeErr websocket.CloseError
	if errors.As(err, &closeErr) {
		return closeErr.Code, closeErr.Reason
	}
	return fallback, ""
}

func uuidV4() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	hexValue := hex.EncodeToString(value[:])
	return hexValue[:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:], nil
}

func isUUIDV4(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value[14] != '4' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil && len(decoded) == 16 && decoded[8]&0xc0 == 0x80
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if s.config.StaticFS == nil {
		http.Error(w, "no static directory configured", http.StatusNotFound)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	// Check before cleaning: accepting a/../b would hide a traversal attempt.
	if !fs.ValidPath(name) || hasParentSegment(r.URL.Path) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	file, err := s.config.StaticFS.Open(name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	data, err := fs.ReadFile(s.config.StaticFS, name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func hasParentSegment(value string) bool {
	for _, segment := range strings.Split(strings.ReplaceAll(value, "\\", "/"), "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

// DirectoryFS returns an fs.FS rooted at root. Every open resolves symlinks
// and confirms that the final path remains beneath the resolved root.
func DirectoryFS(root string) (fs.FS, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve static root: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, fmt.Errorf("resolve static root: %w", err)
	}
	info, err := fs.Stat(osDirectoryFS{root: resolved}, ".")
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("static root is not a directory: %s", root)
	}
	return osDirectoryFS{root: resolved}, nil
}

type osDirectoryFS struct{ root string }

func (d osDirectoryFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, fs.ErrInvalid
	}
	candidate, err := filepath.EvalSymlinks(filepath.Join(d.root, filepath.FromSlash(name)))
	if err != nil {
		return nil, fs.ErrNotExist
	}
	relative, err := filepath.Rel(d.root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fs.ErrNotExist
	}
	return os.Open(candidate)
}
