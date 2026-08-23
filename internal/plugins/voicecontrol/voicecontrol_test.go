package voicecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"viewer/sdk/go/busclient"
)

func catalogFrame(channel string, entries []Entry) busclient.Frame {
	return busclient.Frame{Channel: channel, Value: map[string]any{"entries": entries}}
}

func TestCatalogMergeAndReplace(t *testing.T) {
	p := New()
	p.handleCatalogFrame(catalogFrame("voice-catalog:_:chat", []Entry{
		{ID: "open-chat:c1", Kind: "open_instance", Title: "打开聊天 项目A", Channel: "chat:_:voice:invoke"},
		{ID: "action:status", Kind: "action", Title: "汇报运行状态", Channel: "chat:_:voice:invoke"},
	}))
	p.handleCatalogFrame(catalogFrame("voice-catalog:_:files", []Entry{
		{ID: "open-files:f1", Kind: "open_instance", Title: "打开文件夹", Channel: "files:_:voice:invoke"},
	}))
	entries := p.entries()
	if len(entries) != 3 {
		t.Fatalf("entries=%d, want 3", len(entries))
	}
	// Deterministic order: chat before files.
	if entries[0].Plugin != "chat" || entries[2].Plugin != "files" {
		t.Fatalf("order = %#v", entries)
	}
	// Replace: a fresh frame for the same plugin swaps its whole set.
	p.handleCatalogFrame(catalogFrame("voice-catalog:_:chat", []Entry{
		{ID: "action:status", Kind: "action", Title: "汇报运行状态", Channel: "chat:_:voice:invoke"},
	}))
	entries = p.entries()
	if len(entries) != 2 || entries[0].ID != "action:status" {
		t.Fatalf("after replace = %#v", entries)
	}
	// Empty frame removes the plugin's entries.
	p.handleCatalogFrame(catalogFrame("voice-catalog:_:files", nil))
	if len(p.entries()) != 1 {
		t.Fatalf("after remove = %#v", p.entries())
	}
	// Foreign channels are ignored.
	p.handleCatalogFrame(catalogFrame("chat:_:turn", []Entry{{ID: "x"}}))
	if len(p.entries()) != 1 {
		t.Fatalf("foreign frame leaked: %#v", p.entries())
	}
}

func TestParseConvoReply(t *testing.T) {
	entries := []Entry{{ID: "open-chat:c1"}, {ID: "action:read-latest"}}
	cases := []struct {
		name      string
		content   string
		wantSay   string
		wantEntry string
	}{
		{"direct answer", `{"say":"现在是下午","entry_id":"none"}`, "现在是下午", ""},
		{"entry pick", `{"say":"好的","entry_id":"open-chat:c1"}`, "好的", "open-chat:c1"},
		{"noisy wrapper", "好的，结果是：\n```json\n{\"say\":\"念给你听\",\"entry_id\":\"action:read-latest\"}\n```", "念给你听", "action:read-latest"},
		{"empty entry", `{"say":"嗯","entry_id":""}`, "嗯", ""},
		{"unknown id falls back to answer", `{"say":"不知道","entry_id":"open-chat:nope"}`, "不知道", ""},
		{"not json becomes the answer", "我不知道", "我不知道", ""},
		{"missing say", `{"entry_id":"none"}`, "", ""},
	}
	for _, item := range cases {
		got := parseConvoReply(item.content, entries)
		if got.Say != item.wantSay || got.EntryID != item.wantEntry {
			t.Errorf("%s: parseConvoReply(%q) = %#v, want say=%q entry=%q", item.name, item.content, got, item.wantSay, item.wantEntry)
		}
	}
}

// fakeLLM replies with a fixed say/entry decision and counts calls.
func fakeLLM(say, entryID string, calls *int32) llmCompleter {
	return func(_ context.Context, _ []map[string]string, jsonMode bool, _ int) (string, error) {
		if !jsonMode {
			return "", errors.New("conversation must request json mode")
		}
		atomic.AddInt32(calls, 1)
		return `{"say":"` + say + `","entry_id":"` + entryID + `"}`, nil
	}
}

func TestRunCommandInvokesEntry(t *testing.T) {
	p := New()
	p.handleCatalogFrame(catalogFrame("voice-catalog:_:chat", []Entry{
		{ID: "action:status", Kind: "action", Title: "汇报运行状态", Channel: "chat:_:voice:invoke"},
	}))
	var calls int32
	p.llmFn = fakeLLM("好的", "action:status", &calls)
	var gotChannel string
	var gotPayload map[string]any
	p.call = func(_ context.Context, channel string, payload map[string]any) (invokeResult, error) {
		gotChannel, gotPayload = channel, payload
		return invokeResult{OK: true, Say: "当前没有正在运行的任务"}, nil
	}
	result := p.runCommand(context.Background(), "现在什么在跑", "command")
	if !result.OK || result.EntryID != "action:status" || result.Say != "当前没有正在运行的任务" {
		t.Fatalf("result = %#v", result)
	}
	if gotChannel != "chat:_:voice:invoke" || gotPayload["entry_id"] != "action:status" {
		t.Fatalf("invoke = %s %#v", gotChannel, gotPayload)
	}
	if len(result.Effects) != 0 {
		t.Fatalf("action entries should not gain effects: %#v", result.Effects)
	}
	// The exchange is retained as dialogue history, with the assistant turn
	// stored in the JSON protocol format so later turns keep the envelope.
	p.convoMu.Lock()
	defer p.convoMu.Unlock()
	if len(p.history) != 2 || p.history[0].Role != "user" {
		t.Fatalf("history = %#v", p.history)
	}
	var recorded struct {
		Say     string `json:"say"`
		EntryID string `json:"entry_id"`
	}
	if err := json.Unmarshal([]byte(p.history[1].Content), &recorded); err != nil {
		t.Fatalf("assistant history must be protocol JSON: %v (%q)", err, p.history[1].Content)
	}
	if recorded.Say != "当前没有正在运行的任务" || recorded.EntryID != "action:status" {
		t.Fatalf("assistant history = %#v", recorded)
	}
}

func TestRunCommandDirectAnswer(t *testing.T) {
	p := New()
	p.handleCatalogFrame(catalogFrame("voice-catalog:_:chat", []Entry{
		{ID: "action:status", Kind: "action", Title: "汇报运行状态", Channel: "chat:_:voice:invoke"},
	}))
	var calls int32
	p.llmFn = fakeLLM("我可以打开聊天、读最新回复、口述发消息、停止运行", "none", &calls)
	invoked := false
	p.call = func(context.Context, string, map[string]any) (invokeResult, error) {
		invoked = true
		return invokeResult{}, nil
	}
	result := p.runCommand(context.Background(), "现在有什么功能", "")
	if !result.OK || result.EntryID != "" || !strings.Contains(result.Say, "打开聊天") {
		t.Fatalf("result = %#v", result)
	}
	if invoked {
		t.Fatal("direct answers must not invoke any entry")
	}
}

func TestRunCommandOpenInstanceAppendsEffect(t *testing.T) {
	p := New()
	p.handleCatalogFrame(catalogFrame("voice-catalog:_:chat", []Entry{
		{ID: "open-chat:c1", Kind: "open_instance", Title: "打开聊天 项目A", PaneType: "chat", InstanceID: "c1", Channel: "chat:_:voice:invoke"},
	}))
	var calls int32
	p.llmFn = fakeLLM("好的", "open-chat:c1", &calls)
	p.call = func(_ context.Context, _ string, _ map[string]any) (invokeResult, error) {
		return invokeResult{OK: true, Say: "已打开聊天 项目A"}, nil
	}
	result := p.runCommand(context.Background(), "打开项目A", "")
	if !result.OK || len(result.Effects) != 1 {
		t.Fatalf("result = %#v", result)
	}
	effect := result.Effects[0]
	if effect.Type != "open_instance" || effect.PaneType != "chat" || effect.InstanceID != "c1" {
		t.Fatalf("effect = %#v", effect)
	}
}

func TestAssistantHistoryFormat(t *testing.T) {
	// Regression: plain-text assistant history taught the model to answer in
	// plain text, so the second "open chat" command dispatched nothing.
	if got := assistantHistory("已打开聊天 项目A", "open-chat:c1"); got != `{"entry_id":"open-chat:c1","say":"已打开聊天 项目A"}` {
		t.Fatalf("assistantHistory = %q", got)
	}
	if got := assistantHistory("你好", ""); got != `{"entry_id":"none","say":"你好"}` {
		t.Fatalf("direct answer history = %q", got)
	}

	p := New()
	p.handleCatalogFrame(catalogFrame("voice-catalog:_:chat", []Entry{
		{ID: "open-chat:c1", Kind: "open_instance", Title: "打开聊天 项目A", PaneType: "chat", InstanceID: "c1", Channel: "chat:_:voice:invoke"},
	}))
	var calls int32
	p.llmFn = fakeLLM("好的", "open-chat:c1", &calls)
	p.call = func(_ context.Context, _ string, _ map[string]any) (invokeResult, error) {
		return invokeResult{OK: true, Say: "已打开聊天 项目A"}, nil
	}
	p.runCommand(context.Background(), "打开项目A", "")
	p.convoMu.Lock()
	defer p.convoMu.Unlock()
	var recorded struct {
		Say     string `json:"say"`
		EntryID string `json:"entry_id"`
	}
	if err := json.Unmarshal([]byte(p.history[1].Content), &recorded); err != nil {
		t.Fatalf("assistant history must be protocol JSON: %v (%q)", err, p.history[1].Content)
	}
	if recorded.EntryID != "open-chat:c1" {
		t.Fatalf("successful command must record its entry id: %#v", recorded)
	}
}

func TestRunCommandFailures(t *testing.T) {
	p := New()
	// Empty catalog: the model still answers (conversation never dies), and
	// the empty-catalog note is in its prompt.
	var calls int32
	var gotMessages []map[string]string
	p.llmFn = func(_ context.Context, messages []map[string]string, _ bool, _ int) (string, error) {
		gotMessages = messages
		atomic.AddInt32(&calls, 1)
		return `{"say":"我目前还没有可以操作的功能","entry_id":"none"}`, nil
	}
	result := p.runCommand(context.Background(), "你好", "")
	if !result.OK || !strings.Contains(result.Say, "功能") {
		t.Fatalf("empty catalog = %#v", result)
	}
	if !strings.Contains(gotMessages[0]["content"], "没有任何可语音操作的功能") {
		t.Fatalf("system prompt should note the empty catalog: %q", gotMessages[0]["content"])
	}

	p.handleCatalogFrame(catalogFrame("voice-catalog:_:chat", []Entry{
		{ID: "action:status", Kind: "action", Title: "汇报运行状态", Channel: "chat:_:voice:invoke"},
	}))
	// Empty model say degrades to the canned line.
	p.llmFn = fakeLLM("", "none", &calls)
	if result := p.runCommand(context.Background(), "嗯", ""); result.Say != "没听懂，请再说一遍" {
		t.Fatalf("empty say = %#v", result)
	}
	// LLM transport failure.
	p.llmFn = func(context.Context, []map[string]string, bool, int) (string, error) {
		return "", errors.New("connection refused")
	}
	if result := p.runCommand(context.Background(), "状态", ""); result.Say != "对话失败，请检查模型服务后再试" {
		t.Fatalf("llm failure = %#v", result)
	}
	// LLM not configured (RPC error code from the llm plugin).
	p.llmFn = func(context.Context, []map[string]string, bool, int) (string, error) {
		return "", &busclient.RPCError{Code: "not_configured", Message: "LLM is not configured"}
	}
	if result := p.runCommand(context.Background(), "状态", ""); result.Say != "语音控制需要先在 LLM 面板里配置模型" {
		t.Fatalf("not configured = %#v", result)
	}
	// Invoke failure.
	p.llmFn = fakeLLM("好的", "action:status", &calls)
	p.call = func(context.Context, string, map[string]any) (invokeResult, error) {
		return invokeResult{}, errors.New("plugin unreachable")
	}
	if result := p.runCommand(context.Background(), "状态", ""); result.OK || !strings.Contains(result.Say, "执行失败") {
		t.Fatalf("invoke failure = %#v", result)
	}
}

func TestBuildConvoMessages(t *testing.T) {
	entries := []Entry{{ID: "action:status", Title: "汇报运行状态"}}
	history := []convoTurn{{Role: "user", Content: "你好"}, {Role: "assistant", Content: "你好呀"}}
	messages := buildConvoMessages(convoSystemTemplate, "现在呢", entries, "之前聊了天气", history)
	if len(messages) != 5 {
		t.Fatalf("messages = %d, want 5", len(messages))
	}
	if messages[0]["role"] != "system" || !strings.Contains(messages[0]["content"], "action:status") {
		t.Fatalf("system message missing catalog: %q", messages[0]["content"])
	}
	if messages[1]["role"] != "system" || !strings.Contains(messages[1]["content"], "之前聊了天气") {
		t.Fatalf("summary message = %#v", messages[1])
	}
	if messages[2]["content"] != "你好" || messages[3]["content"] != "你好呀" || messages[4]["content"] != "现在呢" {
		t.Fatalf("turn order = %#v", messages)
	}
	// No summary → no summary message.
	if got := buildConvoMessages(convoSystemTemplate, "x", entries, "", history); len(got) != 4 {
		t.Fatalf("without summary = %d, want 4", len(got))
	}
}

func TestCompressIfNeeded(t *testing.T) {
	p := New()
	// Stay under the summarize path first: over budget, summarizer works.
	var summaryCalls int32
	p.llmFn = func(_ context.Context, messages []map[string]string, jsonMode bool, _ int) (string, error) {
		if jsonMode {
			return "", errors.New("summarization must not request json mode")
		}
		atomic.AddInt32(&summaryCalls, 1)
		if !strings.Contains(messages[0]["content"], "已有摘要") {
			t.Errorf("summary prompt = %q", messages[0]["content"])
		}
		return "用户多次闲聊，没有执行操作", nil
	}
	long := strings.Repeat("啊", 200)
	for i := 0; i < 10; i++ {
		p.recordTurn(long, long, "")
	}
	p.compressIfNeeded(context.Background())
	p.convoMu.Lock()
	defer p.convoMu.Unlock()
	if atomic.LoadInt32(&summaryCalls) != 1 {
		t.Fatalf("summary calls = %d, want 1", summaryCalls)
	}
	if p.summary != "用户多次闲聊，没有执行操作" {
		t.Fatalf("summary = %q", p.summary)
	}
	if len(p.history) != 10 {
		t.Fatalf("history = %d, want newest 10 turns (half of 20)", len(p.history))
	}
	if convoChars(p.summary, p.history) > convoCharBudget {
		t.Fatalf("still over budget after compression")
	}
}

func TestCompressDropsOnSummarizeFailure(t *testing.T) {
	p := New()
	p.llmFn = func(context.Context, []map[string]string, bool, int) (string, error) {
		return "", errors.New("model down")
	}
	long := strings.Repeat("啊", 400)
	for i := 0; i < 8; i++ {
		p.recordTurn(long, long, "")
	}
	p.compressIfNeeded(context.Background())
	p.convoMu.Lock()
	defer p.convoMu.Unlock()
	if p.summary != "" {
		t.Fatalf("failed summarization must not fabricate a summary: %q", p.summary)
	}
	if len(p.history) != 7 {
		t.Fatalf("history = %d, want newest 7 (folded until under budget)", len(p.history))
	}
}

func TestRecordTurnHardCap(t *testing.T) {
	p := New()
	for i := 0; i < convoMaxTurns+6; i++ {
		p.recordTurn("u", "a", "")
	}
	p.convoMu.Lock()
	defer p.convoMu.Unlock()
	if len(p.history) != convoMaxTurns {
		t.Fatalf("history = %d, want cap %d", len(p.history), convoMaxTurns)
	}
}

func TestTurnFrameAnnouncement(t *testing.T) {
	p := New()
	p.mu.Lock()
	p.enabled = true
	p.mu.Unlock()
	p.call = func(_ context.Context, channel string, payload map[string]any) (invokeResult, error) {
		if channel != "chat:_:voice:invoke" || payload["entry_id"] != "action:read-latest" || payload["instance_id"] != "c1" {
			t.Errorf("invoke = %s %#v", channel, payload)
		}
		return invokeResult{OK: true, Say: "最新回复：全部测试通过"}, nil
	}
	var announced string
	p.announceFn = func(say, _ string) { announced = say }

	p.handleTurnFrame(busclient.Frame{Channel: "chat:_:turn", Value: map[string]any{
		"phase": "completed", "chat_id": "c1", "role_name": "助手", "stop_reason": "end_turn",
	}})
	if !strings.Contains(announced, "全部测试通过") || !strings.Contains(announced, "助手") {
		t.Fatalf("announced = %q", announced)
	}

	// Failed turns announce the failure directly, without invoking read-latest.
	announced = ""
	p.handleTurnFrame(busclient.Frame{Channel: "chat:_:turn", Value: map[string]any{
		"phase": "completed", "chat_id": "c1", "role_name": "助手", "stop_reason": "error",
	}})
	if announced != "助手的任务执行失败，请查看聊天" {
		t.Fatalf("error announcement = %q", announced)
	}

	// Cancelled turns and non-completed phases stay silent.
	announced = ""
	p.handleTurnFrame(busclient.Frame{Channel: "chat:_:turn", Value: map[string]any{
		"phase": "completed", "chat_id": "c1", "stop_reason": "cancelled",
	}})
	p.handleTurnFrame(busclient.Frame{Channel: "chat:_:turn", Value: map[string]any{
		"phase": "started", "chat_id": "c1",
	}})
	if announced != "" {
		t.Fatalf("should stay silent, got %q", announced)
	}

	// Disabled: nothing.
	p.mu.Lock()
	p.enabled = false
	p.mu.Unlock()
	p.handleTurnFrame(busclient.Frame{Channel: "chat:_:turn", Value: map[string]any{
		"phase": "completed", "chat_id": "c1", "stop_reason": "end_turn",
	}})
	if announced != "" {
		t.Fatalf("disabled should stay silent, got %q", announced)
	}
}

func TestInteractionLog(t *testing.T) {
	p := New()
	var calls int32
	p.llmFn = fakeLLM("好的", "none", &calls)
	p.call = func(context.Context, string, map[string]any) (invokeResult, error) {
		return invokeResult{OK: true, Say: "x"}, nil
	}
	result := p.runCommand(context.Background(), "你好", "command")
	if !result.OK {
		t.Fatalf("result = %#v", result)
	}
	entries := p.logSnapshot()
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Kind != "exchange" || entry.Transcript != "你好" || entry.Say != "好的" || !entry.OK {
		t.Fatalf("entry = %#v", entry)
	}
	if entry.Phase != "command" || entry.LLMRaw == "" {
		t.Fatalf("phase/raw missing: %#v", entry)
	}

	// Failures are logged with their reason.
	p.llmFn = func(context.Context, []map[string]string, bool, int) (string, error) {
		return "", errors.New("connection refused")
	}
	p.runCommand(context.Background(), "状态", "")
	entries = p.logSnapshot()
	if len(entries) != 2 || entries[1].Detail == "" || entries[1].OK {
		t.Fatalf("failure entry = %#v", entries)
	}

	// The ring is bounded.
	for i := 0; i < logMaxEntries+10; i++ {
		p.appendLog(LogEntry{Kind: "event", Detail: "x"})
	}
	if got := len(p.logSnapshot()); got != logMaxEntries {
		t.Fatalf("log size = %d, want cap %d", got, logMaxEntries)
	}
}

func TestEnableLogsSessionEvent(t *testing.T) {
	p := New()
	p.handleEnable(busclient.Frame{Channel: "voice-control:_:enable", Value: map[string]any{"enabled": true}})
	entries := p.logSnapshot()
	if len(entries) != 1 || entries[0].Kind != "event" || !strings.Contains(entries[0].Detail, "开启") {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestConfigTemplateFallbackWithoutClient(t *testing.T) {
	p := New()
	if got := p.systemTemplate(context.Background()); got != convoSystemTemplate {
		t.Fatalf("system template fallback mismatch")
	}
	if got := p.summaryTemplate(context.Background()); got != convoSummaryTemplate {
		t.Fatalf("summary template fallback mismatch")
	}
}
