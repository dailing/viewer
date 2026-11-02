// Generic low-latency media data plane. Producers write H.264 access units
// over a localhost WebSocket; browsers receive WebRTC from this same gateway
// origin. The gateway knows stream ids and tickets, never browser pages.
package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"
)

const (
	maxMediaStreams  = 128
	maxMediaPeers    = 64
	maxMediaFrame    = 8 << 20
	mediaTicketTTL   = time.Minute
	mediaGatherLimit = 8 * time.Second
)

type mediaTicket struct {
	value     string
	expiresAt time.Time
}

type mediaStream struct {
	id      string
	secret  string
	mu      sync.Mutex
	conn    *websocket.Conn
	tracks  map[*webrtc.TrackLocalStaticSample]struct{}
	peers   map[*webrtc.PeerConnection]struct{}
	tickets map[string]mediaTicket
	last    time.Time
}

type mediaHub struct {
	mu      sync.Mutex
	streams map[string]*mediaStream
	peers   int
	closed  bool
}

func newMediaHub() *mediaHub {
	return &mediaHub{streams: make(map[string]*mediaStream)}
}

func validMediaID(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func secureEqual(a, b string) bool {
	return len(a) >= 32 && len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (h *mediaHub) streamForPublish(id, secret string) (*mediaStream, error) {
	if !validMediaID(id) || len(secret) < 32 || len(secret) > 256 {
		return nil, errors.New("invalid stream credentials")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errors.New("media hub is closed")
	}
	stream := h.streams[id]
	if stream == nil {
		if len(h.streams) >= maxMediaStreams {
			return nil, errors.New("media stream limit reached")
		}
		stream = &mediaStream{id: id, secret: secret, tracks: make(map[*webrtc.TrackLocalStaticSample]struct{}), peers: make(map[*webrtc.PeerConnection]struct{}), tickets: make(map[string]mediaTicket)}
		h.streams[id] = stream
	}
	if !secureEqual(stream.secret, secret) {
		return nil, errors.New("invalid stream credentials")
	}
	return stream, nil
}

func (h *mediaHub) stream(id string) *mediaStream {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.streams[id]
}

func loopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (h *mediaHub) servePublish(w http.ResponseWriter, r *http.Request) {
	if !loopbackRequest(r) {
		http.Error(w, "media publish is loopback-only", http.StatusForbidden)
		return
	}
	id := r.Header.Get("X-Viewer-Stream")
	secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	stream, err := h.streamForPublish(id, secret)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	conn.SetReadLimit(maxMediaFrame)
	stream.mu.Lock()
	previous := stream.conn
	stream.conn = conn
	stream.last = time.Time{}
	stream.mu.Unlock()
	if previous != nil {
		_ = previous.Close(websocket.StatusNormalClosure, "publisher replaced")
	}
	defer func() {
		stream.mu.Lock()
		if stream.conn == conn {
			stream.conn = nil
		}
		stream.mu.Unlock()
		_ = conn.CloseNow()
	}()
	for {
		kind, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		if kind != websocket.MessageBinary || len(data) == 0 {
			continue
		}
		stream.writeH264(data)
	}
}

func (s *mediaStream) writeH264(data []byte) {
	s.mu.Lock()
	now := time.Now()
	duration := 33 * time.Millisecond
	if !s.last.IsZero() {
		duration = now.Sub(s.last)
		if duration < time.Millisecond {
			duration = time.Millisecond
		} else if duration > time.Second {
			duration = time.Second
		}
	}
	s.last = now
	tracks := make([]*webrtc.TrackLocalStaticSample, 0, len(s.tracks))
	for track := range s.tracks {
		tracks = append(tracks, track)
	}
	s.mu.Unlock()
	for _, track := range tracks {
		if err := track.WriteSample(mediaSample(data, duration)); err != nil {
			s.removeTrack(track)
		}
	}
}

// Isolated to keep the Pion media import at the transport boundary.
func mediaSample(data []byte, duration time.Duration) pionmedia.Sample {
	return pionmedia.Sample{Data: data, Duration: duration}
}

func (h *mediaHub) serveTicket(w http.ResponseWriter, r *http.Request) {
	if !jsonRequest(w, r) {
		return
	}
	if !loopbackRequest(r) {
		http.Error(w, "media ticket issuance is loopback-only", http.StatusForbidden)
		return
	}
	var request struct {
		StreamID   string `json:"stream_id"`
		PublishKey string `json:"publish_key"`
		Ticket     string `json:"ticket"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request) != nil || !validMediaID(request.StreamID) || len(request.Ticket) < 32 || len(request.Ticket) > 256 {
		http.Error(w, "invalid media ticket", http.StatusBadRequest)
		return
	}
	stream := h.stream(request.StreamID)
	if stream == nil || !secureEqual(stream.secret, request.PublishKey) {
		http.Error(w, "invalid stream credentials", http.StatusForbidden)
		return
	}
	stream.mu.Lock()
	if len(stream.tickets) >= 32 {
		for value, ticket := range stream.tickets {
			if time.Now().After(ticket.expiresAt) {
				delete(stream.tickets, value)
			}
		}
	}
	if len(stream.tickets) >= 32 {
		stream.mu.Unlock()
		http.Error(w, "media ticket limit reached", http.StatusTooManyRequests)
		return
	}
	stream.tickets[request.Ticket] = mediaTicket{value: request.Ticket, expiresAt: time.Now().Add(mediaTicketTTL)}
	stream.mu.Unlock()
	writeMediaJSON(w, http.StatusOK, map[string]any{"ok": true, "expires_in": int(mediaTicketTTL.Seconds())})
}

func jsonRequest(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

func (h *mediaHub) serveWebRTC(w http.ResponseWriter, r *http.Request) {
	if !jsonRequest(w, r) {
		return
	}
	var request struct {
		StreamID string                    `json:"stream_id"`
		Ticket   string                    `json:"ticket"`
		Offer    webrtc.SessionDescription `json:"offer"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request) != nil {
		http.Error(w, "invalid WebRTC request", http.StatusBadRequest)
		return
	}
	stream := h.stream(request.StreamID)
	if stream == nil || !stream.consumeTicket(request.Ticket) {
		http.Error(w, "invalid or expired media ticket", http.StatusForbidden)
		return
	}
	answer, err := h.addPeer(r.Context(), stream, request.Offer)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeMediaJSON(w, http.StatusOK, map[string]any{"answer": answer})
}

func (s *mediaStream) consumeTicket(value string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, ok := s.tickets[value]
	delete(s.tickets, value)
	return ok && ticket.value == value && time.Now().Before(ticket.expiresAt)
}

func (h *mediaHub) addPeer(ctx context.Context, stream *mediaStream, offer webrtc.SessionDescription) (*webrtc.SessionDescription, error) {
	h.mu.Lock()
	if h.closed || h.peers >= maxMediaPeers {
		h.mu.Unlock()
		return nil, errors.New("media peer limit reached")
	}
	h.peers++
	h.mu.Unlock()
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		h.peerDone()
		return nil, err
	}
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e028"}, "video", stream.id)
	if err != nil {
		_ = peer.Close()
		h.peerDone()
		return nil, err
	}
	sender, err := peer.AddTrack(track)
	if err != nil {
		_ = peer.Close()
		h.peerDone()
		return nil, err
	}
	go func() {
		buffer := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buffer); err != nil {
				return
			}
		}
	}()
	stream.mu.Lock()
	if len(stream.peers) >= 8 {
		stream.mu.Unlock()
		_ = peer.Close()
		h.peerDone()
		return nil, errors.New("stream viewer limit reached")
	}
	stream.tracks[track] = struct{}{}
	stream.peers[peer] = struct{}{}
	stream.mu.Unlock()
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			stream.removePeer(peer, track)
			h.peerDone()
		})
	}
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateFailed:
			cleanup()
		}
	})
	if err := peer.SetRemoteDescription(offer); err != nil {
		cleanup()
		return nil, fmt.Errorf("invalid WebRTC offer: %w", err)
	}
	gathered := webrtc.GatheringCompletePromise(peer)
	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := peer.SetLocalDescription(answer); err != nil {
		cleanup()
		return nil, err
	}
	select {
	case <-gathered:
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	case <-time.After(mediaGatherLimit):
		cleanup()
		return nil, errors.New("WebRTC ICE gathering timed out")
	}
	local := peer.LocalDescription()
	return local, nil
}

func (s *mediaStream) removeTrack(track *webrtc.TrackLocalStaticSample) {
	s.mu.Lock()
	delete(s.tracks, track)
	s.mu.Unlock()
}

func (s *mediaStream) removePeer(peer *webrtc.PeerConnection, track *webrtc.TrackLocalStaticSample) {
	s.mu.Lock()
	delete(s.tracks, track)
	delete(s.peers, peer)
	s.mu.Unlock()
	_ = peer.Close()
}

func (h *mediaHub) peerDone() {
	h.mu.Lock()
	if h.peers > 0 {
		h.peers--
	}
	h.mu.Unlock()
}

func (h *mediaHub) close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	streams := make([]*mediaStream, 0, len(h.streams))
	for _, stream := range h.streams {
		streams = append(streams, stream)
	}
	h.streams = make(map[string]*mediaStream)
	h.mu.Unlock()
	for _, stream := range streams {
		stream.mu.Lock()
		if stream.conn != nil {
			_ = stream.conn.Close(websocket.StatusGoingAway, "gateway shutting down")
		}
		for peer := range stream.peers {
			_ = peer.Close()
		}
		stream.peers = make(map[*webrtc.PeerConnection]struct{})
		stream.tracks = make(map[*webrtc.TrackLocalStaticSample]struct{})
		stream.mu.Unlock()
	}
}

func writeMediaJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil && !errors.Is(err, io.EOF) {
		slog.Debug("media response write failed", "error", err)
	}
}
