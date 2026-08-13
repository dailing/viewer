// Package terminal exposes interactive PTY sessions as a reusable Viewer bus plugin.
package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"

	"viewer/internal/busclient"
)

const (
	DefaultCols    = 120
	DefaultRows    = 30
	RingChunks     = 1000
	ReadSize       = 65536
	FlushChars     = 128 * 1024
	SnapshotBudget = 800_000

	flushInterval = 30 * time.Millisecond
	retentionTime = 30 * time.Second
)

// Manifest is the terminal plugin's declared bus surface.
var Manifest = busclient.Manifest{
	ID: "terminal", Version: "0.1.0",
	Slots: map[string]any{
		"terminal:_:create":   map[string]any{"summary": "spawn a PTY; RPC -> {id}"},
		"terminal:_:list":     map[string]any{"summary": "list terminals; RPC -> [{id, state, ...}]"},
		"terminal:*:write":    map[string]any{"value": map[string]any{"data": "str — keystrokes/paste"}},
		"terminal:*:resize":   map[string]any{"value": map[string]any{"cols": "int", "rows": "int"}},
		"terminal:*:kill":     map[string]any{"summary": "terminate the PTY"},
		"terminal:*:snapshot": map[string]any{"summary": "scrollback history; RPC {limit?} -> {entries}"},
	},
	Emits: map[string]any{
		"terminal:*:output": map[string]any{"value": map[string]any{"seq": "int", "ts": "ms", "data": "str"}},
		"terminal:*:status": map[string]any{"mailbox": "full value: state/pid/cwd/shell/cols/rows/exit_code"},
	},
}

// Entry is one coalesced output chunk retained for snapshots.
type Entry struct {
	Seq  int64  `json:"seq"`
	TS   int64  `json:"ts"`
	Data string `json:"data"`
}

// Status is the complete retained value for a terminal.
type Status struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	ExitCode  *int   `json:"exit_code"`
	PID       int    `json:"pid"`
	CWD       string `json:"cwd"`
	Shell     string `json:"shell"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
	CreatedTS int64  `json:"created_ts"`
}

type session struct {
	mu sync.Mutex

	id            string
	cmd           *exec.Cmd
	pty           *os.File
	cwd           string
	shell         string
	cols          int
	rows          int
	createdTS     int64
	state         string
	exitCode      *int
	seq           int64
	ring          []Entry
	pending       []string
	pendingChars  int
	decoderTail   []byte
	flushTimer    *time.Timer
	killRequested bool
	retainUntil   time.Time
	done          chan struct{}
}

func (s *session) status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func (s *session) statusLocked() Status {
	var code *int
	if s.exitCode != nil {
		value := *s.exitCode
		code = &value
	}
	return Status{ID: s.id, State: s.state, ExitCode: code, PID: s.cmd.Process.Pid,
		CWD: s.cwd, Shell: s.shell, Cols: s.cols, Rows: s.rows, CreatedTS: s.createdTS}
}

// Plugin owns the bus client and every PTY it creates. It can be embedded in
// another Go process or used by cmd/viewer-terminal.
type Plugin struct {
	client *busclient.Client

	mu       sync.RWMutex
	sessions map[string]*session
	nextID   uint64
	closed   bool
	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// New constructs a disconnected terminal plugin.
func New(kernelWS string, options ...busclient.Option) *Plugin {
	client := busclient.New(kernelWS, Manifest, options...)
	p := &Plugin{client: client, sessions: make(map[string]*session), nextID: 1, stop: make(chan struct{})}
	client.OnStateChange(func(state busclient.ConnectionState) {
		if state == busclient.StateConnected {
			p.republishStatuses()
		}
	})
	client.OnError(func(entry busclient.ErrorEntry) {
		fmt.Fprintf(os.Stderr, "terminal protocol error [%s] %s\n", entry.Code, entry.Message)
	})
	return p
}

// Start registers all slots, connects to the kernel, and starts cleanup work.
func (p *Plugin) Start(ctx context.Context) error {
	handlers := map[string]func(busclient.Frame){
		"terminal:_:create":   p.handleCreate,
		"terminal:_:list":     p.handleList,
		"terminal:*:write":    p.handleWrite,
		"terminal:*:resize":   p.handleResize,
		"terminal:*:kill":     p.handleKill,
		"terminal:*:snapshot": p.handleSnapshot,
	}
	for pattern, handler := range handlers {
		if _, err := p.client.Subscribe(pattern, handler); err != nil {
			return fmt.Errorf("subscribe %s: %w", pattern, err)
		}
	}
	if err := p.client.Connect(ctx); err != nil {
		return err
	}
	p.wg.Add(1)
	go p.cleanupLoop()
	return nil
}

// Close stops all PTYs owned by this plugin and closes its bus connection.
func (p *Plugin) Close(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	sessions := make([]*session, 0, len(p.sessions))
	for _, item := range p.sessions {
		sessions = append(sessions, item)
	}
	p.mu.Unlock()
	p.stopOnce.Do(func() { close(p.stop) })
	for _, item := range sessions {
		_ = p.killSession(ctx, item)
		item.mu.Lock()
		_ = item.pty.Close()
		item.mu.Unlock()
	}
	p.wg.Wait()
	return p.client.Close()
}

func (p *Plugin) handleCreate(frame busclient.Frame) {
	value := objectValue(frame.Value)
	cwd := homeDir()
	cols, err := positiveInt(value["cols"], DefaultCols)
	if err != nil {
		p.respondError(frame, "bad_size", "value.cols/value.rows must be positive ints")
		return
	}
	rows, err := positiveInt(value["rows"], DefaultRows)
	if err != nil {
		p.respondError(frame, "bad_size", "value.cols/value.rows must be positive ints")
		return
	}
	s, err := p.spawn(cwd, cols, rows)
	if err != nil {
		p.respondError(frame, "spawn_failed", err.Error())
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = p.killSession(context.Background(), s)
		p.respondError(frame, "closed", "terminal plugin is stopping")
		return
	}
	p.sessions[s.id] = s
	p.mu.Unlock()
	p.setStatus(s)
	p.publish("instance:_:set", map[string]any{"plugin": "terminal", "instance": s.id,
		"value": map[string]any{"cwd": s.cwd, "shell": s.shell}})
	p.respond(frame, map[string]any{"id": s.id})
}

func (p *Plugin) handleList(frame busclient.Frame) {
	p.mu.RLock()
	items := make([]*session, 0, len(p.sessions))
	for _, item := range p.sessions {
		items = append(items, item)
	}
	p.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		a, _ := strconv.ParseUint(items[i].id, 10, 64)
		b, _ := strconv.ParseUint(items[j].id, 10, 64)
		return a < b
	})
	statuses := make([]Status, 0, len(items))
	for _, item := range items {
		statuses = append(statuses, item.status())
	}
	p.respond(frame, statuses)
}

func (p *Plugin) handleWrite(frame busclient.Frame) {
	s := p.sessionFor(frame.Channel)
	if s == nil {
		p.respondError(frame, "no_such_terminal", frame.Channel)
		return
	}
	data, ok := objectValue(frame.Value)["data"].(string)
	if !ok || data == "" {
		p.respondError(frame, "bad_input", "value.data must be a non-empty string")
		return
	}
	s.mu.Lock()
	if s.state != "running" {
		s.mu.Unlock()
		p.respondError(frame, "write_failed", "terminal is not running")
		return
	}
	_, err := s.pty.Write([]byte(data))
	s.mu.Unlock()
	if err != nil {
		p.respondError(frame, "write_failed", err.Error())
		return
	}
	p.respond(frame, map[string]any{"ok": true})
}

func (p *Plugin) handleResize(frame busclient.Frame) {
	s := p.sessionFor(frame.Channel)
	if s == nil {
		p.respondError(frame, "no_such_terminal", frame.Channel)
		return
	}
	value := objectValue(frame.Value)
	cols, errCols := positiveInt(value["cols"], 0)
	rows, errRows := positiveInt(value["rows"], 0)
	if errCols != nil || errRows != nil {
		p.respondError(frame, "bad_resize", "value.cols/value.rows must be ints")
		return
	}
	s.mu.Lock()
	err := pty.Setsize(s.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err == nil {
		s.cols, s.rows = cols, rows
	}
	s.mu.Unlock()
	if err != nil {
		p.respondError(frame, "bad_resize", err.Error())
		return
	}
	p.setStatus(s)
	p.respond(frame, map[string]any{"ok": true})
}

func (p *Plugin) handleKill(frame busclient.Frame) {
	s := p.sessionFor(frame.Channel)
	if s == nil {
		p.respondError(frame, "no_such_terminal", frame.Channel)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.killSession(ctx, s); err != nil {
		p.respondError(frame, "kill_failed", err.Error())
		return
	}
	p.publish("instance:_:delete", map[string]any{"plugin": "terminal", "instance": s.id})
	p.respond(frame, map[string]any{"ok": true})
}

func (p *Plugin) handleSnapshot(frame busclient.Frame) {
	s := p.sessionFor(frame.Channel)
	if s == nil {
		p.respondError(frame, "no_such_terminal", frame.Channel)
		return
	}
	value := objectValue(frame.Value)
	limit, err := positiveInt(value["limit"], 200)
	if err != nil {
		p.respondError(frame, "bad_snapshot", "value.limit must be a positive int")
		return
	}
	before, hasBefore := integer(value["before_seq"])
	entries := snapshotEntries(s, limit, int64(before), hasBefore)
	p.respond(frame, map[string]any{"entries": entries})
}

func snapshotEntries(s *session, limit int, before int64, hasBefore bool) []Entry {
	s.mu.Lock()
	entries := make([]Entry, 0, min(limit, len(s.ring)))
	budget := SnapshotBudget
	for i := len(s.ring) - 1; i >= 0 && len(entries) < limit; i-- {
		entry := s.ring[i]
		if hasBefore && entry.Seq >= before {
			continue
		}
		encoded, _ := json.Marshal(entry)
		if len(entries) > 0 && len(encoded) > budget {
			break
		}
		entries = append(entries, entry)
		budget -= len(encoded)
	}
	s.mu.Unlock()
	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}
	return entries
}

func (p *Plugin) spawn(cwd string, cols, rows int) (*session, error) {
	shell, script := shellCommand()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = cwd
	cmd.Env = environmentWith(map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"})
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	id := strconv.FormatUint(p.nextID, 10)
	p.nextID++
	p.mu.Unlock()
	s := &session{id: id, cmd: cmd, pty: file, cwd: cwd, shell: shell, cols: cols, rows: rows,
		createdTS: time.Now().UnixMilli(), state: "running", ring: make([]Entry, 0, RingChunks), done: make(chan struct{})}
	p.wg.Add(2)
	go p.readLoop(s)
	go p.waitLoop(s)
	return s, nil
}

func (p *Plugin) readLoop(s *session) {
	defer p.wg.Done()
	buffer := make([]byte, ReadSize)
	for {
		n, err := s.pty.Read(buffer)
		if n > 0 {
			p.appendOutput(s, buffer[:n], false)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
				// Linux PTYs report EIO when the slave side closes.
			}
			p.appendOutput(s, nil, true)
			p.flush(s)
			return
		}
	}
}

func (p *Plugin) appendOutput(s *session, data []byte, final bool) {
	s.mu.Lock()
	combined := append(append([]byte(nil), s.decoderTail...), data...)
	text, tail := decodeUTF8(combined, final)
	s.decoderTail = tail
	chars := utf8.RuneCountInString(text)
	shouldPreFlush := len(s.pending) > 0 && s.pendingChars+chars > FlushChars
	s.mu.Unlock()
	if shouldPreFlush {
		p.flush(s)
	}
	if text == "" {
		return
	}
	s.mu.Lock()
	s.pending = append(s.pending, text)
	s.pendingChars += chars
	if s.pendingChars >= FlushChars {
		s.mu.Unlock()
		p.flush(s)
		return
	}
	if s.flushTimer == nil {
		s.flushTimer = time.AfterFunc(flushInterval, func() { p.flush(s) })
	}
	s.mu.Unlock()
}

func (p *Plugin) flush(s *session) {
	s.mu.Lock()
	if s.flushTimer != nil {
		s.flushTimer.Stop()
		s.flushTimer = nil
	}
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return
	}
	text := strings.Join(s.pending, "")
	s.pending = nil
	s.pendingChars = 0
	s.seq++
	entry := Entry{Seq: s.seq, TS: time.Now().UnixMilli(), Data: text}
	if len(s.ring) == RingChunks {
		copy(s.ring, s.ring[1:])
		s.ring[len(s.ring)-1] = entry
	} else {
		s.ring = append(s.ring, entry)
	}
	id := s.id
	s.mu.Unlock()
	p.publish("terminal:"+id+":output", entry)
}

func (p *Plugin) waitLoop(s *session) {
	defer p.wg.Done()
	err := s.cmd.Wait()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
	}
	s.mu.Lock()
	s.exitCode = &code
	if s.killRequested {
		s.state = "killed"
	} else {
		s.state = "exited"
	}
	s.retainUntil = time.Now().Add(retentionTime)
	status := s.statusLocked()
	close(s.done)
	s.mu.Unlock()
	p.setStatusValue(status)
}

func (p *Plugin) killSession(ctx context.Context, s *session) error {
	s.mu.Lock()
	if s.state != "running" {
		s.mu.Unlock()
		return nil
	}
	s.killRequested = true
	pid := s.cmd.Process.Pid
	done := s.done
	s.mu.Unlock()
	signalSessionProcessGroups(pid, syscall.SIGTERM)
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		signalSessionProcessGroups(pid, syscall.SIGKILL)
	case <-ctx.Done():
		signalSessionProcessGroups(pid, syscall.SIGKILL)
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Plugin) cleanupLoop() {
	defer p.wg.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			p.mu.Lock()
			for id, item := range p.sessions {
				item.mu.Lock()
				expired := item.state != "running" && !item.retainUntil.IsZero() && !now.Before(item.retainUntil)
				if expired {
					_ = item.pty.Close()
					delete(p.sessions, id)
				}
				item.mu.Unlock()
			}
			p.mu.Unlock()
		case <-p.stop:
			return
		}
	}
}

func (p *Plugin) sessionFor(channel string) *session {
	parts := strings.Split(channel, ":")
	if len(parts) < 2 {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sessions[parts[1]]
}

func (p *Plugin) setStatus(s *session)         { p.setStatusValue(s.status()) }
func (p *Plugin) setStatusValue(status Status) { p.set("terminal:"+status.ID+":status", status) }

func (p *Plugin) republishStatuses() {
	p.mu.RLock()
	items := make([]*session, 0, len(p.sessions))
	for _, item := range p.sessions {
		items = append(items, item)
	}
	p.mu.RUnlock()
	for _, item := range items {
		p.setStatus(item)
	}
}

func (p *Plugin) publish(channel string, value any) {
	if !p.client.Connected() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.client.Publish(ctx, channel, value)
}

func (p *Plugin) set(channel string, value any) {
	if !p.client.Connected() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.client.Set(ctx, channel, value)
}

func (p *Plugin) respond(frame busclient.Frame, result any) {
	value := objectValue(frame.Value)
	replyTo, _ := value["_reply_to"].(string)
	corr, _ := value["_corr"].(string)
	if replyTo == "" || corr == "" {
		return
	}
	p.publish(replyTo, map[string]any{"_corr": corr, "ok": true, "result": result})
}

func (p *Plugin) respondError(frame busclient.Frame, code, message string) {
	value := objectValue(frame.Value)
	replyTo, _ := value["_reply_to"].(string)
	corr, _ := value["_corr"].(string)
	if replyTo == "" || corr == "" {
		return
	}
	p.publish(replyTo, map[string]any{"_corr": corr, "ok": false,
		"error": map[string]any{"code": code, "message": message}})
}

func objectValue(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return map[string]any{}
}

func positiveInt(value any, fallback int) (int, error) {
	if value == nil {
		return fallback, nil
	}
	number, ok := integer(value)
	if !ok || number <= 0 || number > 65535 {
		return 0, errors.New("not a positive integer")
	}
	return number, nil
}

func integer(value any) (int, bool) {
	switch number := value.(type) {
	case float64:
		integer := int(number)
		return integer, number == float64(integer)
	case int:
		return number, true
	case int64:
		return int(number), true
	case json.Number:
		parsed, err := strconv.Atoi(number.String())
		return parsed, err == nil
	default:
		return 0, false
	}
}

func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "/tmp"
}

func shellCommand() (string, string) {
	if _, err := exec.LookPath("zsh"); err == nil {
		return "zsh", "exec zsh -f"
	}
	return "bash", "exec bash --noprofile --norc"
}

func environmentWith(overrides map[string]string) []string {
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; !replaced {
			environment = append(environment, entry)
		}
	}
	for key, value := range overrides {
		environment = append(environment, key+"="+value)
	}
	return environment
}

func decodeUTF8(data []byte, final bool) (string, []byte) {
	cut := len(data)
	if !final {
		start := max(0, len(data)-utf8.UTFMax+1)
		for i := start; i < len(data); i++ {
			if utf8.RuneStart(data[i]) && !utf8.FullRune(data[i:]) {
				cut = i
				break
			}
		}
	}
	return strings.ToValidUTF8(string(data[:cut]), "\uFFFD"), append([]byte(nil), data[cut:]...)
}

// signalSessionProcessGroups signals every process group still belonging to
// the PTY's session. Interactive shells create separate groups for jobs, so
// signaling only the session leader's group can otherwise orphan a child.
func signalSessionProcessGroups(sessionID int, signal syscall.Signal) {
	groups := map[int]struct{}{sessionID: {}}
	paths, _ := filepath.Glob("/proc/[0-9]*/stat")
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		closeParen := strings.LastIndexByte(string(data), ')')
		if closeParen < 0 {
			continue
		}
		fields := strings.Fields(string(data[closeParen+1:]))
		if len(fields) < 4 {
			continue
		}
		pgid, errPG := strconv.Atoi(fields[2])
		sid, errSID := strconv.Atoi(fields[3])
		if errPG == nil && errSID == nil && sid == sessionID && pgid > 0 {
			groups[pgid] = struct{}{}
		}
	}
	for pgid := range groups {
		if pgid != sessionID {
			_ = syscall.Kill(-pgid, signal)
		}
	}
	if len(groups) > 1 && signal == syscall.SIGTERM {
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(-sessionID, signal)
}
