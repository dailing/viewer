package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
)

func TestMediaHubH264WebRTCEndToEnd(t *testing.T) {
	server := New(Config{})
	httpServer := httptest.NewServer(http.HandlerFunc(server.serveHTTP))
	defer httpServer.Close()
	defer server.media.close()
	streamID, secret := "st_teststream", "sec_0123456789abcdef0123456789abcdef0123456789"
	wsURL := "ws" + httpServer.URL[len("http"):] + "/api/media/publish"
	conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{HTTPHeader: http.Header{
		"Authorization": {"Bearer " + secret}, "X-Viewer-Stream": {streamID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	ticket := "ticket_0123456789abcdef0123456789abcdef0123456789"
	postMedia(t, httpServer.URL+"/api/media/ticket", map[string]any{"stream_id": streamID, "publish_key": secret, "ticket": ticket}, http.StatusOK)

	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	onTrack := make(chan *webrtc.TrackRemote, 1)
	peer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { onTrack <- track })
	if _, err := peer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	offer, err := peer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(peer)
	if err := peer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	response := postMedia(t, httpServer.URL+"/api/media/webrtc", map[string]any{"stream_id": streamID, "ticket": ticket, "offer": peer.LocalDescription()}, http.StatusOK)
	var signal struct {
		Answer webrtc.SessionDescription `json:"answer"`
	}
	if err := json.Unmarshal(response, &signal); err != nil {
		t.Fatal(err)
	}
	if err := peer.SetRemoteDescription(signal.Answer); err != nil {
		t.Fatal(err)
	}
	accessUnit := []byte{0, 0, 0, 1, 0x09, 0x10, 0, 0, 0, 1, 0x65, 0x88, 0x84, 0x21}
	writeDone := make(chan struct{})
	defer close(writeDone)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-writeDone:
				return
			case <-ticker.C:
				_ = conn.Write(context.Background(), websocket.MessageBinary, accessUnit)
			}
		}
	}()
	select {
	case track := <-onTrack:
		if track.Codec().MimeType != webrtc.MimeTypeH264 {
			t.Fatalf("codec = %s", track.Codec().MimeType)
		}
		packet, _, err := track.ReadRTP()
		if err != nil || len(packet.Payload) == 0 {
			t.Fatalf("ReadRTP payload=%d err=%v", len(packet.Payload), err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for relayed H.264 track")
	}
}

func TestMediaTicketRequiresJSONAndSecret(t *testing.T) {
	server := New(Config{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/media/ticket", bytes.NewBufferString(`{}`))
	request.Header.Set("Content-Type", "text/plain")
	server.serveHTTP(recorder, request)
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestMediaProducerEndpointsRejectNonLoopback(t *testing.T) {
	server := New(Config{})
	for _, item := range []struct{ method, path string }{{http.MethodGet, "/api/media/publish"}, {http.MethodPost, "/api/media/ticket"}} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(item.method, item.path, bytes.NewBufferString(`{}`))
		request.Header.Set("Content-Type", "application/json")
		server.serveHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d, want loopback rejection", item.path, recorder.Code)
		}
	}
}

func postMedia(t *testing.T, url string, value any, wantStatus int) []byte {
	t.Helper()
	body, _ := json.Marshal(value)
	response, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var buffer bytes.Buffer
	_, _ = buffer.ReadFrom(response.Body)
	if response.StatusCode != wantStatus {
		t.Fatalf("POST %s status=%d body=%s", url, response.StatusCode, buffer.String())
	}
	return buffer.Bytes()
}
