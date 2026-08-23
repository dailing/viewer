package chat

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBuildVoiceCatalog(t *testing.T) {
	chats := []Chat{
		{ID: "c1", Name: "项目A", Root: "/home/user/project-a"},
		{ID: "c2", Name: "杂谈", Root: "/home/user/chat"},
	}
	entries := buildVoiceCatalog(chats)
	if len(entries) != 2+4 {
		t.Fatalf("entries=%d, want 6", len(entries))
	}
	byID := map[string]voiceEntry{}
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	open := byID[voiceOpenPrefix+"c1"]
	if open.Kind != "open_instance" || open.InstanceID != "c1" || open.PaneType != "chat" || open.Channel != voiceInvokeChannel {
		t.Fatalf("open entry = %#v", open)
	}
	if !strings.Contains(open.Title, "项目A") {
		t.Fatalf("open entry title = %q", open.Title)
	}
	// The root basename is a keyword so "打开 project-a" can match.
	foundBase := false
	for _, keyword := range open.Keywords {
		if keyword == "project-a" {
			foundBase = true
		}
	}
	if !foundBase {
		t.Fatalf("keywords missing root basename: %#v", open.Keywords)
	}
	for _, id := range []string{voiceActionReadLatest, voiceActionDictate, voiceActionStop, voiceActionStatus} {
		entry := byID[id]
		if entry.Kind != "action" || entry.Channel != voiceInvokeChannel {
			t.Fatalf("action %s missing: %#v", id, entry)
		}
	}
}

// fakeVoiceCompleter fakes the global llm plugin: every call returns the
// canned spoken text.
func fakeVoiceCompleter(spoken string, calls *int32) llmCompleter {
	return func(_ context.Context, _ []map[string]string, _ bool, _ int) (completionResult, error) {
		atomic.AddInt32(calls, 1)
		return completionResult{Content: spoken, Model: "fake"}, nil
	}
}

func newVoiceTestPlugin(t *testing.T) *Plugin {
	t.Helper()
	plugin, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { plugin.Close() })
	chat := Chat{ID: "c1", Name: "项目A", Type: "group", MemberRoleIDsJSON: "[]", RoleRoutingOverridesJSON: "{}"}
	if err := plugin.store.saveChat(&chat); err != nil {
		t.Fatal(err)
	}
	return plugin
}

func TestInvokeReadLatest(t *testing.T) {
	plugin := newVoiceTestPlugin(t)
	raw := "已提交 commit 8f3a2b1c，改动在 internal/plugins/chat/voicecmd.go。\n```go\nfunc x() {}\n```\n全部测试通过。"
	if err := plugin.store.addMessage(&Message{ID: "m1", ChatID: "c1", TurnID: "t1", Role: "assistant", Text: raw, SenderFrom: "role", RoleName: "助手", CreatedAt: nowMillis()}); err != nil {
		t.Fatal(err)
	}
	var calls int32
	plugin.llmFn = fakeVoiceCompleter("助手完成了提交，全部测试通过。", &calls)

	result := plugin.invokeVoiceEntry(context.Background(), voiceActionReadLatest, "c1")
	if !result.OK {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(result.Say, "全部测试通过") || strings.Contains(result.Say, "8f3a2b1c") {
		t.Fatalf("say should be the speakable rewrite, got %q", result.Say)
	}
	if !strings.Contains(result.Say, "助手") {
		t.Fatalf("say should name the role, got %q", result.Say)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("LLM calls = %d, want 1 (speakable)", calls)
	}
}

func TestInvokeStatusAndStop(t *testing.T) {
	plugin := newVoiceTestPlugin(t)
	status := plugin.invokeVoiceEntry(context.Background(), voiceActionStatus, "c1")
	if !status.OK || status.Say != "当前没有正在运行的任务" {
		t.Fatalf("status result = %#v", status)
	}
	// Nothing running: stop reports that instead of failing.
	stop := plugin.invokeVoiceEntry(context.Background(), voiceActionStop, "c1")
	if stop.Say != "当前没有正在运行的任务" {
		t.Fatalf("stop result = %#v", stop)
	}
}

func TestInvokeOpenChat(t *testing.T) {
	plugin := newVoiceTestPlugin(t)
	result := plugin.invokeVoiceEntry(context.Background(), voiceOpenPrefix+"c1", "")
	if !result.OK || result.Say != "已打开聊天 项目A" {
		t.Fatalf("result = %#v", result)
	}
	// open_instance effects are voice-control's job; the plugin only does
	// backend bookkeeping.
	if len(result.Effects) != 0 {
		t.Fatalf("effects = %#v", result.Effects)
	}
	if plugin.activeChatID != "c1" {
		t.Fatalf("activeChatID = %q, want c1", plugin.activeChatID)
	}
}

func TestInvokeDictate(t *testing.T) {
	plugin := newVoiceTestPlugin(t)
	result := plugin.invokeVoiceEntry(context.Background(), voiceActionDictate, "c1")
	if !result.OK || len(result.Effects) != 1 {
		t.Fatalf("result = %#v", result)
	}
	effect := result.Effects[0]
	if effect.Type != "start_dictation" || effect.Plugin != "chat" || effect.InstanceID != "c1" {
		t.Fatalf("effect = %#v", effect)
	}
	// Without an active/target chat, dictate refuses aloud.
	empty, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if refused := empty.invokeVoiceEntry(context.Background(), voiceActionDictate, ""); refused.OK || !strings.Contains(refused.Say, "没有激活的聊天") {
		t.Fatalf("refused = %#v", refused)
	}
}

func TestSpeakableFallback(t *testing.T) {
	raw := "完成了。\n```go\nfunc x() {}\n```\n提交成功。"
	got := speakableFallback(raw)
	if strings.Contains(got, "func x()") || !strings.Contains(got, "提交成功") {
		t.Fatalf("fallback = %q", got)
	}
	long := strings.Repeat("字", 300)
	if truncated := speakableFallback(long); len([]rune(truncated)) > 210 {
		t.Fatalf("fallback should truncate, got %d runes", len([]rune(truncated)))
	}
}
