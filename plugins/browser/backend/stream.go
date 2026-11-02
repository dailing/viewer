package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type StreamController interface {
	Ensure(context.Context, string, uint32, Viewport, VideoSettings) error
	Ticket(context.Context) (map[string]any, error)
	Stop()
}

type PageStream struct {
	pageID     string
	gatewayURL string
	ffmpegPath string

	mu        sync.Mutex
	streamID  string
	secret    string
	signature string
	command   *exec.Cmd
	conn      *websocket.Conn
	done      chan struct{}
	lastErr   error
}

func NewPageStream(pageID, gatewayURL, ffmpegPath string) *PageStream {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	return &PageStream{pageID: pageID, gatewayURL: strings.TrimRight(gatewayURL, "/"), ffmpegPath: ffmpegPath}
}

func streamSignature(display string, xid uint32, viewport Viewport, video VideoSettings) string {
	return fmt.Sprintf("%s/%d/%dx%d/%d/%d", display, xid, viewport.PixelWidth, viewport.PixelHeight, video.FPS, video.BitrateKbps)
}

func (s *PageStream) Ensure(ctx context.Context, display string, xid uint32, viewport Viewport, video VideoSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	signature := streamSignature(display, xid, viewport, video)
	if s.signature == signature && s.command != nil {
		return nil
	}
	s.stopLocked()
	if xid == 0 {
		return errors.New("browser X11 window is unavailable")
	}
	if s.streamID == "" {
		id, err := randomToken("st_", 16)
		if err != nil {
			return err
		}
		s.streamID = strings.ToLower(id)
		s.secret, err = randomSecret()
		if err != nil {
			return err
		}
	}
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "x11grab", "-window_id", strconv.FormatUint(uint64(xid), 10),
		"-framerate", strconv.Itoa(video.FPS), "-video_size", fmt.Sprintf("%dx%d", viewport.PixelWidth, viewport.PixelHeight),
		"-draw_mouse", "1", "-i", display,
		"-an", "-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency",
		"-profile:v", "baseline", "-pix_fmt", "yuv420p", "-threads", "2",
		"-b:v", strconv.Itoa(video.BitrateKbps) + "k", "-maxrate", strconv.Itoa(video.BitrateKbps) + "k",
		"-bufsize", strconv.Itoa(video.BitrateKbps*2) + "k", "-g", strconv.Itoa(video.FPS),
		"-keyint_min", strconv.Itoa(video.FPS), "-sc_threshold", "0", "-bf", "0",
		"-x264-params", "aud=1:repeat-headers=1", "-f", "h264", "pipe:1",
	}
	command := exec.Command(s.ffmpegPath, args...)
	command.Env = append(command.Environ(), "DISPLAY="+display)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start H.264 encoder: %w", err)
	}
	wsURL := "ws" + strings.TrimPrefix(s.gatewayURL, "http") + "/api/media/publish"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
		HTTPHeader:      http.Header{"Authorization": {"Bearer " + s.secret}, "X-Viewer-Stream": {s.streamID}},
	})
	if err != nil {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		return fmt.Errorf("connect gateway media publisher: %w", err)
	}
	s.command, s.conn, s.done, s.signature, s.lastErr = command, conn, make(chan struct{}), signature, nil
	done := s.done
	go s.run(command, conn, stdout, &stderr, done)
	return nil
}

func (s *PageStream) run(command *exec.Cmd, conn *websocket.Conn, stdout io.Reader, stderr *bytes.Buffer, done chan struct{}) {
	splitter := annexBSplitter{emit: func(data []byte) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return conn.Write(ctx, websocket.MessageBinary, data)
	}}
	err := splitter.read(stdout)
	waitErr := command.Wait()
	if err == nil {
		err = waitErr
	}
	// Signal the reconfiguration path before taking s.mu: Stop holds that lock
	// while waiting for done, so locking first would add a two-second deadlock
	// timeout to every FPS/bitrate/resolution change.
	close(done)
	s.mu.Lock()
	if s.command == command {
		if err != nil && s.signature != "" {
			message := strings.TrimSpace(stderr.String())
			if len(message) > 500 {
				message = message[len(message)-500:]
			}
			s.lastErr = fmt.Errorf("video publisher stopped: %w %s", err, message)
		}
		s.command, s.conn = nil, nil
	}
	s.mu.Unlock()
	_ = conn.CloseNow()
}

func (s *PageStream) Ticket(ctx context.Context) (map[string]any, error) {
	s.mu.Lock()
	streamID, secret := s.streamID, s.secret
	running := s.command != nil
	lastErr := s.lastErr
	s.mu.Unlock()
	if !running {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("video stream is not running")
	}
	ticket, err := randomSecret()
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{"stream_id": streamID, "publish_key": secret, "ticket": ticket})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.gatewayURL+"/api/media/ticket", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1000))
		return nil, fmt.Errorf("media ticket failed: %s", strings.TrimSpace(string(message)))
	}
	return map[string]any{"stream_id": streamID, "ticket": ticket, "expires_in": 60}, nil
}

func (s *PageStream) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

func (s *PageStream) stopLocked() {
	command, conn, done := s.command, s.conn, s.done
	s.command, s.conn, s.done, s.signature = nil, nil, nil, ""
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "stream reconfiguring")
	}
	if command != nil {
		_ = command.Process.Kill()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

type annexBSplitter struct {
	emit    func([]byte) error
	pending []byte
	current []byte
}

func (s *annexBSplitter) read(reader io.Reader) error {
	buffer := make([]byte, 64<<10)
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			s.pending = append(s.pending, buffer[:count]...)
			if len(s.pending) > 16<<20 {
				return errors.New("H.264 parser buffer exceeded 16 MiB")
			}
			if splitErr := s.split(false); splitErr != nil {
				return splitErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return s.split(true)
			}
			return err
		}
	}
}

func h264Start(data []byte, offset int) (index, prefix int) {
	for index := offset; index+3 <= len(data); index++ {
		if data[index] == 0 && data[index+1] == 0 {
			if data[index+2] == 1 {
				return index, 3
			}
			if index+4 <= len(data) && data[index+2] == 0 && data[index+3] == 1 {
				return index, 4
			}
		}
	}
	return -1, 0
}

func (s *annexBSplitter) split(eof bool) error {
	for {
		first, prefix := h264Start(s.pending, 0)
		if first < 0 {
			if eof {
				s.pending = nil
				return s.flush()
			}
			if len(s.pending) > 3 {
				s.pending = append(s.pending[:0], s.pending[len(s.pending)-3:]...)
			}
			return nil
		}
		if first > 0 {
			s.pending = s.pending[first:]
		}
		next, _ := h264Start(s.pending, prefix)
		if next < 0 {
			if !eof {
				return nil
			}
			next = len(s.pending)
		}
		nal := append([]byte(nil), s.pending[:next]...)
		s.pending = s.pending[next:]
		nalType := nal[prefix] & 0x1f
		if nalType == 9 && len(s.current) > 0 {
			if err := s.flush(); err != nil {
				return err
			}
		}
		s.current = append(s.current, nal...)
		if eof && len(s.pending) == 0 {
			return s.flush()
		}
	}
}

func (s *annexBSplitter) flush() error {
	if len(s.current) == 0 {
		return nil
	}
	data := s.current
	s.current = nil
	return s.emit(data)
}
