package chat

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"viewer/internal/acp"
)

type fakeAgent struct {
	mu            sync.Mutex
	sessionID     string
	loadErr       error
	updates       func(acp.Update)
	promptStarted chan struct{}
	promptRelease chan struct{}
	cancelled     bool
	newCalls      int
	loadCalls     int
}

func newFakeAgent() *fakeAgent {
	return &fakeAgent{sessionID: "new-session", promptStarted: make(chan struct{}), promptRelease: make(chan struct{})}
}
func (f *fakeAgent) Initialize(context.Context) (map[string]any, error) { return map[string]any{}, nil }
func (f *fakeAgent) NewSession(context.Context, string) (string, error) {
	f.newCalls++
	return f.sessionID, nil
}
func (f *fakeAgent) LoadSession(context.Context, string, string) error {
	f.loadCalls++
	return f.loadErr
}
func (f *fakeAgent) OnUpdate(callback func(acp.Update)) { f.updates = callback }
func (f *fakeAgent) Stderr() string                     { return "" }
func (f *fakeAgent) Close() error                       { return nil }
func (f *fakeAgent) Cancel(context.Context, string) error {
	f.mu.Lock()
	f.cancelled = true
	f.mu.Unlock()
	closeOnce(f.promptRelease)
	return nil
}
func (f *fakeAgent) Prompt(_ context.Context, sessionID, text string) (string, error) {
	closeOnce(f.promptStarted)
	if f.updates != nil {
		f.updates(acp.Update{SessionID: sessionID, Value: map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "answer"}}})
	}
	<-f.promptRelease
	f.mu.Lock()
	cancelled := f.cancelled
	f.mu.Unlock()
	if cancelled {
		return "cancelled", nil
	}
	return "end_turn", nil
}

func closeOnce(channel chan struct{}) {
	select {
	case <-channel:
	default:
		close(channel)
	}
}

func TestPersistenceTurnAndSameChatBusy(t *testing.T) {
	fake := newFakeAgent()
	p, err := New(t.TempDir(), WithAgentFactory(func(context.Context) (agent, string, error) { return fake, "test", nil }))
	if err != nil {
		t.Fatal(err)
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	defer p.Close()
	result, start, err := p.accept(context.Background(), "chat-1", "hello", t.TempDir())
	if err != nil || result["accepted"] != true {
		t.Fatalf("accept: %#v %v", result, err)
	}
	start()
	<-fake.promptStarted
	if _, _, err := p.accept(context.Background(), "chat-1", "again", ""); !errors.Is(err, errTurnActive) {
		t.Fatalf("expected busy, got %v", err)
	}
	closeOnce(fake.promptRelease)
	p.wg.Wait()
	var chats, messages, turns int64
	p.store.db.Model(&Chat{}).Count(&chats)
	p.store.db.Model(&Message{}).Count(&messages)
	p.store.db.Model(&Turn{}).Count(&turns)
	if chats != 1 || messages != 2 || turns != 1 {
		t.Fatalf("rows chats=%d messages=%d turns=%d", chats, messages, turns)
	}
	var turn Turn
	p.store.db.First(&turn)
	if turn.StopReason == nil || *turn.StopReason != "end_turn" || turn.EndedAt == nil {
		t.Fatalf("incomplete turn: %#v", turn)
	}
}

func TestPersistedSessionLoadAndDegradeToNew(t *testing.T) {
	dir := t.TempDir()
	seed, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	chat := &Chat{ID: "reuse", CreatedAt: nowMillis(), Provider: "hermes", ProviderProfile: "test", ProviderSessionID: "old-session", CWD: dir}
	if err := seed.store.saveChat(chat); err != nil {
		t.Fatal(err)
	}
	_ = seed.Close()

	fake := newFakeAgent()
	fake.loadErr = errors.New("gone")
	p, err := New(dir, WithAgentFactory(func(context.Context) (agent, string, error) { return fake, "test", nil }))
	if err != nil {
		t.Fatal(err)
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	defer p.Close()
	stored, _ := p.store.chat("reuse")
	runtime, err := p.ensureRuntime(context.Background(), stored)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.sessionID != "new-session" {
		t.Fatalf("expected degraded new session, got %q", runtime.sessionID)
	}
	refreshed, _ := p.store.chat("reuse")
	if refreshed.ProviderSessionID != "new-session" {
		t.Fatalf("database not updated: %#v", refreshed)
	}
}

func TestPersistedSessionLoadsWithoutCreatingReplacement(t *testing.T) {
	dir := t.TempDir()
	seed, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	chat := &Chat{ID: "reuse", CreatedAt: nowMillis(), Provider: "hermes", ProviderProfile: "test", ProviderSessionID: "old-session", CWD: dir}
	if err := seed.store.saveChat(chat); err != nil {
		t.Fatal(err)
	}
	_ = seed.Close()

	fake := newFakeAgent()
	p, err := New(dir, WithAgentFactory(func(context.Context) (agent, string, error) { return fake, "test", nil }))
	if err != nil {
		t.Fatal(err)
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	defer p.Close()
	stored, _ := p.store.chat("reuse")
	runtime, err := p.ensureRuntime(context.Background(), stored)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.sessionID != "old-session" || fake.loadCalls != 1 || fake.newCalls != 0 {
		t.Fatalf("session=%q load=%d new=%d", runtime.sessionID, fake.loadCalls, fake.newCalls)
	}
}

func TestStopIsIdempotentAndCompletesCancelled(t *testing.T) {
	fake := newFakeAgent()
	p, err := New(t.TempDir(), WithAgentFactory(func(context.Context) (agent, string, error) { return fake, "test", nil }))
	if err != nil {
		t.Fatal(err)
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	defer p.Close()
	result, start, err := p.accept(context.Background(), "stop-chat", "long", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	start()
	<-fake.promptStarted
	stopped, err := p.stopTurn("stop-chat")
	if err != nil || !stopped {
		t.Fatalf("stop=%v err=%v", stopped, err)
	}
	p.wg.Wait()
	stopped, err = p.stopTurn("stop-chat")
	if err != nil || stopped {
		t.Fatalf("idempotent stop=%v err=%v", stopped, err)
	}
	var turn Turn
	p.store.db.First(&turn, "id = ?", result["turn_id"])
	if turn.StopReason == nil || *turn.StopReason != "cancelled" {
		t.Fatalf("reason: %#v", turn.StopReason)
	}
}

func TestUpdateTextOnly(t *testing.T) {
	if got := updateText(map[string]any{"sessionUpdate": "tool_call", "text": "ignored"}); got != "" {
		t.Fatal(got)
	}
	if got := updateText(map[string]any{"session_update": "agent_message_chunk", "content": map[string]any{"text": "ok"}}); got != "ok" {
		t.Fatal(got)
	}
	time.Sleep(0)
}
