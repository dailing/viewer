// Package voicecontrol implements the global voice-control plugin
// (framework: voice actions, slice 2). It owns the cross-plugin voice entry
// catalog and runs the continuous voice conversation: each transcribed
// utterance goes to the global llm plugin with the merged catalog, the
// running conversation summary, and recent turns; the model either answers
// directly (spoken) or picks one catalog entry to invoke. Plugins publish
// their voice-addressable entries as a retained mailbox on
// `voice-catalog:_:<plugin_id>`; this plugin subscribes `voice-catalog:_:*`
// and merges them, so startup order never matters.
package voicecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"viewer/internal/plugins/pluginrpc"
	"viewer/sdk/go/busclient"
)

var Manifest = busclient.Manifest{
	ID: "voice-control", Version: "0.2.0",
	Slots: map[string]any{
		"voice-control:_:command":   map[string]any{"summary": "run one voice conversation exchange: LLM answers directly or invokes a catalog entry; RPC {text, phase?} -> CommandResult"},
		"voice-control:_:catalog":   map[string]any{"summary": "list the merged voice entry catalog; RPC -> {entries}"},
		"voice-control:_:enable":    map[string]any{"summary": "toggle proactive announcements; RPC {enabled} -> {enabled}"},
		"voice-control:_:log":       map[string]any{"summary": "snapshot the bounded interaction log; RPC -> {entries} (newest last)"},
		"voice-control:_:log:clear": map[string]any{"summary": "clear the interaction log; RPC -> {ok}"},
		"voice-control:_:prompt":    map[string]any{"summary": "prompt assembly debug view: effective templates + defaults + rendered preview; RPC {text?} -> {system_template, summary_template, defaults, summary, history, messages}"},
	},
	Emits: map[string]any{
		"voice-control:_:announce": map[string]any{"summary": "proactive spoken announcement for the frontend loop; {say}"},
		"voice-control:_:log":      map[string]any{"summary": "retained mailbox carrying the whole bounded interaction log {entries}; republished on every new entry"},
	},
}

const (
	// catalogPrefix namespaces per-plugin entry mailboxes.
	catalogPrefix = "voice-catalog:_:"
	// announceChannel carries proactive spoken lines to the frontend.
	announceChannel = "voice-control:_:announce"
	// logChannel is the retained mailbox mirroring the interaction log.
	logChannel    = "voice-control:_:log"
	rpcBudget     = 5 * time.Second
	logMaxEntries = 100
	// configNamespace holds the frontend-editable prompt templates
	// (system_template / summary_template), re-read per call so edits take
	// effect immediately.
	configNamespace = "plugins.voice-control"
	// convoCharBudget bounds the running summary plus retained dialogue
	// history; crossing it folds the oldest half of the history into the
	// summary before the next LLM call.
	convoCharBudget = 3000
	// convoMaxTurns hard-caps retained turns even when summarization fails.
	convoMaxTurns = 40
)

// Entry is one voice-addressable capability published by a plugin on its
// `voice-catalog:_:<plugin>` mailbox: either opening a sidebar instance
// (kind "open_instance") or invoking an action (kind "action").
type Entry struct {
	ID          string   `json:"id"`
	Plugin      string   `json:"plugin"`
	Kind        string   `json:"kind"`
	Title       string   `json:"title"`
	Keywords    []string `json:"keywords,omitempty"`
	Description string   `json:"description,omitempty"`
	// PaneType and InstanceID let voice-control open the target pane itself
	// for open_instance entries; plugins never emit frontend effects for them.
	PaneType   string `json:"pane_type,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	// Channel is the plugin RPC invoked with {entry_id, instance_id, text}.
	Channel string `json:"channel"`
}

// Effect tells the frontend to take a UI action after a command: open a pane
// instance, or start dictation into a specific pane (delivered to that pane
// over `voice-fx:<plugin>:<instance_id>`).
type Effect struct {
	Type       string `json:"type"` // "open_instance" | plugin-defined (e.g. "start_dictation")
	Plugin     string `json:"plugin,omitempty"`
	PaneType   string `json:"pane_type,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
}

// CommandResult is always a spoken-outcome envelope: Say is read aloud by
// the client even for failures, so the user never has to look at the screen.
type CommandResult struct {
	OK         bool     `json:"ok"`
	Say        string   `json:"say"`
	EntryID    string   `json:"entry_id,omitempty"`
	Transcript string   `json:"transcript"`
	Effects    []Effect `json:"effects"`
}

// invokeResult mirrors the owning plugin's invoke reply.
type invokeResult struct {
	OK      bool     `json:"ok"`
	Say     string   `json:"say"`
	Effects []Effect `json:"effects"`
}

// llmCompleter abstracts the global llm plugin call (injectable in tests).
type llmCompleter func(ctx context.Context, messages []map[string]string, jsonMode bool, timeoutSeconds int) (string, error)

// invoker abstracts the RPC call into the owning plugin (injectable in tests).
type invoker func(ctx context.Context, channel string, payload map[string]any) (invokeResult, error)

// convoTurn is one retained dialogue exchange (user utterance or the spoken
// assistant reply).
type convoTurn struct {
	Role    string // "user" | "assistant"
	Content string
}

// LogEntry is one interaction-log record. Kind "exchange" is one voice
// command round-trip (transcript → LLM decision → optional catalog invoke →
// spoken reply); kind "event" marks state transitions (loop toggled, context
// compressed, announcement) so the debug pane can reconstruct what happened.
type LogEntry struct {
	TS         int64    `json:"ts"`              // unix milliseconds
	Kind       string   `json:"kind"`            // "exchange" | "event"
	Phase      string   `json:"phase,omitempty"` // frontend state-machine phase reporting the exchange
	Transcript string   `json:"transcript,omitempty"`
	Say        string   `json:"say,omitempty"`     // final spoken line
	LLMRaw     string   `json:"llm_raw,omitempty"` // raw model output before parsing
	OK         bool     `json:"ok,omitempty"`
	EntryID    string   `json:"entry_id,omitempty"`
	EntryTitle string   `json:"entry_title,omitempty"`
	Channel    string   `json:"channel,omitempty"` // invoked plugin RPC channel
	Effects    []Effect `json:"effects,omitempty"`
	Detail     string   `json:"detail,omitempty"` // event text or failure reason
	LLMMs      int64    `json:"llm_ms,omitempty"`
	InvokeMs   int64    `json:"invoke_ms,omitempty"`
}

type Plugin struct {
	client *busclient.Client
	llmFn  llmCompleter
	call   invoker
	// announceFn pushes a proactive spoken line to the frontend (injectable
	// in tests).
	announceFn func(say, chatID string)

	mu      sync.Mutex
	catalog map[string][]Entry // plugin id → entries
	enabled bool               // proactive announcements on/off

	convoMu sync.Mutex
	summary string      // folded summary of dropped older turns
	history []convoTurn // recent dialogue, oldest first

	logMu      sync.Mutex
	logEntries []LogEntry // bounded interaction log, oldest first
}

func New() *Plugin {
	return &Plugin{catalog: map[string][]Entry{}}
}

func (p *Plugin) Start(ctx context.Context, kernelWS string, managed bool) error {
	p.client = busclient.New(kernelWS, Manifest, busclient.WithManaged(managed))
	p.llmFn = p.llmCompleteViaBus
	p.call = p.invokeViaBus
	p.announceFn = func(say, chatID string) {
		_ = p.client.Publish(context.Background(), announceChannel, map[string]any{"say": say, "chat_id": chatID})
	}
	handlers := map[string]func(busclient.Frame){
		"voice-control:_:command":   p.handleCommand,
		"voice-control:_:catalog":   p.handleCatalog,
		"voice-control:_:enable":    p.handleEnable,
		"voice-control:_:log":       p.handleLog,
		"voice-control:_:log:clear": p.handleLogClear,
		"voice-control:_:prompt":    p.handlePrompt,
	}
	for pattern, handler := range handlers {
		asyncHandler := handler
		if _, err := p.client.Subscribe(pattern, func(frame busclient.Frame) { go asyncHandler(frame) }); err != nil {
			return fmt.Errorf("subscribe %s: %w", pattern, err)
		}
	}
	if _, err := p.client.Subscribe("voice-catalog:_:*", p.handleCatalogFrame); err != nil {
		return fmt.Errorf("subscribe voice-catalog:_:*: %w", err)
	}
	// Turn lifecycle feed (slice 4 proactive announcements). Chat-specific
	// for now: chat is the only turn-based plugin.
	if _, err := p.client.Subscribe("chat:_:turn", func(frame busclient.Frame) { go p.handleTurnFrame(frame) }); err != nil {
		return fmt.Errorf("subscribe chat:_:turn: %w", err)
	}
	if err := p.client.Connect(ctx); err != nil {
		return fmt.Errorf("connect voice-control plugin: %w", err)
	}
	return nil
}

func (p *Plugin) Close(context.Context) error {
	if p.client != nil {
		return p.client.Close()
	}
	return nil
}

// handleCatalogFrame replaces one plugin's entries from its mailbox frame.
func (p *Plugin) handleCatalogFrame(frame busclient.Frame) {
	pluginID := strings.TrimPrefix(frame.Channel, catalogPrefix)
	if pluginID == "" || pluginID == frame.Channel {
		return
	}
	var payload struct {
		Entries []Entry `json:"entries"`
	}
	encoded, err := json.Marshal(frame.Value)
	if err == nil {
		_ = json.Unmarshal(encoded, &payload)
	}
	for i := range payload.Entries {
		payload.Entries[i].Plugin = pluginID
	}
	p.mu.Lock()
	if len(payload.Entries) == 0 {
		delete(p.catalog, pluginID)
	} else {
		p.catalog[pluginID] = payload.Entries
	}
	p.mu.Unlock()
}

// entries snapshots the merged catalog, ordered by plugin for determinism.
func (p *Plugin) entries() []Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	plugins := make([]string, 0, len(p.catalog))
	for pluginID := range p.catalog {
		plugins = append(plugins, pluginID)
	}
	sort.Strings(plugins)
	result := []Entry{}
	for _, pluginID := range plugins {
		result = append(result, p.catalog[pluginID]...)
	}
	return result
}

func (p *Plugin) announcementsEnabled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.enabled
}

// appendLog records one entry and mirrors the whole bounded log to the
// retained mailbox so an open debug pane updates live (and a freshly opened
// one immediately sees history).
func (p *Plugin) appendLog(entry LogEntry) {
	if entry.TS == 0 {
		entry.TS = time.Now().UnixMilli()
	}
	p.logMu.Lock()
	p.logEntries = append(p.logEntries, entry)
	if len(p.logEntries) > logMaxEntries {
		p.logEntries = append([]LogEntry(nil), p.logEntries[len(p.logEntries)-logMaxEntries:]...)
	}
	snapshot := append([]LogEntry(nil), p.logEntries...)
	p.logMu.Unlock()
	if p.client != nil {
		_ = p.client.Set(context.Background(), logChannel, map[string]any{"entries": snapshot})
	}
}

// logSnapshot returns the retained log, oldest first.
func (p *Plugin) logSnapshot() []LogEntry {
	p.logMu.Lock()
	defer p.logMu.Unlock()
	return append([]LogEntry(nil), p.logEntries...)
}

// logEvent records a state-transition line (kind "event").
func (p *Plugin) logEvent(detail string) {
	p.appendLog(LogEntry{Kind: "event", Detail: detail})
}

func (p *Plugin) handleLog(frame busclient.Frame) {
	entries := p.logSnapshot()
	if entries == nil {
		entries = []LogEntry{}
	}
	_ = pluginrpc.Respond(p.client, frame, map[string]any{"entries": entries})
}

func (p *Plugin) handleLogClear(frame busclient.Frame) {
	p.logMu.Lock()
	p.logEntries = nil
	p.logMu.Unlock()
	if p.client != nil {
		_ = p.client.Set(context.Background(), logChannel, map[string]any{"entries": []LogEntry{}})
	}
	_ = pluginrpc.Respond(p.client, frame, map[string]any{"ok": true})
}

// handlePrompt exposes the prompt assembly for the debug pane: the effective
// templates (config-store override or built-in default), the built-in
// defaults, current conversation state, and the message list a command
// would send right now.
func (p *Plugin) handlePrompt(frame busclient.Frame) {
	request, _ := pluginrpc.Object(frame)
	text, _ := request["text"].(string)
	if strings.TrimSpace(text) == "" {
		text = "（示例转写）现在有什么功能"
	}
	p.convoMu.Lock()
	summary, history := p.summary, append([]convoTurn(nil), p.history...)
	p.convoMu.Unlock()
	ctx := context.Background()
	messages := buildConvoMessages(p.systemTemplate(ctx), text, p.entries(), summary, history)
	_ = pluginrpc.Respond(p.client, frame, map[string]any{
		"system_template":  p.systemTemplate(ctx),
		"summary_template": p.summaryTemplate(ctx),
		"defaults": map[string]any{
			"system_template":  convoSystemTemplate,
			"summary_template": convoSummaryTemplate,
		},
		"summary":  summary,
		"history":  history,
		"messages": messages,
	})
}

// configTemplate reads one prompt template from the config-store, falling
// back to the built-in default; re-read per call so frontend edits take
// effect immediately (same contract as the llm plugin's active config).
func (p *Plugin) configTemplate(ctx context.Context, key, fallback string) string {
	if p.client == nil {
		return fallback
	}
	value, err := p.client.Request(ctx, "config:_:get", map[string]any{"plugin": configNamespace, "key": key}, rpcBudget)
	if err != nil || value == nil {
		return fallback
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return fallback
	}
	return text
}

func (p *Plugin) systemTemplate(ctx context.Context) string {
	return p.configTemplate(ctx, "system_template", convoSystemTemplate)
}

func (p *Plugin) summaryTemplate(ctx context.Context) string {
	return p.configTemplate(ctx, "summary_template", convoSummaryTemplate)
}

func (p *Plugin) handleEnable(frame busclient.Frame) {
	request, ok := pluginrpc.Object(frame)
	if !ok {
		_ = pluginrpc.RespondError(p.client, frame, "bad_request", "payload must be an object")
		return
	}
	enabled, _ := request["enabled"].(bool)
	p.mu.Lock()
	p.enabled = enabled
	p.mu.Unlock()
	// Toggling the loop bounds one voice session: drop the dialogue state so
	// the next session starts with a clean context.
	p.convoMu.Lock()
	p.summary = ""
	p.history = nil
	p.convoMu.Unlock()
	if enabled {
		p.logEvent("语音控制已开启（新会话，上下文已重置）")
	} else {
		p.logEvent("语音控制已关闭")
	}
	_ = pluginrpc.Respond(p.client, frame, map[string]any{"enabled": enabled})
}

func (p *Plugin) handleCatalog(frame busclient.Frame) {
	_ = pluginrpc.Respond(p.client, frame, map[string]any{"entries": p.entries()})
}

func (p *Plugin) handleCommand(frame busclient.Frame) {
	request, ok := pluginrpc.Object(frame)
	if !ok {
		_ = pluginrpc.RespondError(p.client, frame, "bad_request", "payload must be an object")
		return
	}
	text, _ := request["text"].(string)
	if strings.TrimSpace(text) == "" {
		_ = pluginrpc.RespondError(p.client, frame, "bad_request", "text is required")
		return
	}
	phase, _ := request["phase"].(string)
	_ = pluginrpc.Respond(p.client, frame, p.runCommand(context.Background(), text, phase))
}

// runCommand runs one conversation exchange: the LLM either answers the
// utterance directly or picks a catalog entry to invoke. It never returns a
// transport error for user-facing failures — every outcome carries a Say
// line. Every exchange lands in the interaction log (deferred, so all
// return paths are covered).
func (p *Plugin) runCommand(ctx context.Context, text, phase string) CommandResult {
	result := CommandResult{Transcript: text, Effects: []Effect{}}
	logged := LogEntry{Kind: "exchange", Phase: phase, Transcript: text}
	defer func() {
		logged.Say = result.Say
		logged.OK = result.OK
		logged.Effects = result.Effects
		p.appendLog(logged)
	}()
	entries := p.entries()
	llmStart := time.Now()
	reply, raw, err := p.converse(ctx, text, entries)
	logged.LLMMs = time.Since(llmStart).Milliseconds()
	logged.LLMRaw = raw
	if err != nil {
		logged.Detail = err.Error()
		if llmNotConfigured(err) {
			result.Say = "语音控制需要先在 LLM 面板里配置模型"
		} else {
			result.Say = "对话失败，请检查模型服务后再试"
		}
		return result
	}
	if reply.EntryID == "" {
		// Direct answer: nothing to invoke, the model's line is the reply.
		result.OK = true
		result.Say = reply.Say
		if strings.TrimSpace(result.Say) == "" {
			result.Say = "没听懂，请再说一遍"
		}
		p.recordTurn(text, result.Say, "")
		return result
	}
	var entry *Entry
	for i := range entries {
		if entries[i].ID == reply.EntryID {
			entry = &entries[i]
			break
		}
	}
	if entry == nil {
		// parseConvoReply already validates against the catalog; a miss here
		// means the catalog changed between calls.
		result.OK = true
		result.Say = reply.Say
		if strings.TrimSpace(result.Say) == "" {
			result.Say = "刚才要执行的功能已经不存在了"
		}
		logged.Detail = "model picked an entry that vanished from the catalog"
		p.recordTurn(text, result.Say, "")
		return result
	}
	result.EntryID = reply.EntryID
	logged.EntryID = entry.ID
	logged.EntryTitle = entry.Title
	logged.Channel = entry.Channel
	invokeStart := time.Now()
	invoked, err := p.call(ctx, entry.Channel, map[string]any{
		"entry_id": entry.ID, "instance_id": entry.InstanceID, "text": text,
	})
	logged.InvokeMs = time.Since(invokeStart).Milliseconds()
	if err != nil {
		result.Say = "执行失败：" + err.Error()
		logged.Detail = err.Error()
		p.recordTurn(text, result.Say, "")
		return result
	}
	result.OK = invoked.OK
	result.Say = invoked.Say
	result.Effects = invoked.Effects
	if entry.Kind == "open_instance" && invoked.OK {
		// Opening the pane is a shell concern; the owning plugin only does
		// backend bookkeeping, so voice-control appends the effect itself.
		result.Effects = append(result.Effects, Effect{Type: "open_instance", Plugin: entry.Plugin, PaneType: entry.PaneType, InstanceID: entry.InstanceID})
	}
	if result.Effects == nil {
		result.Effects = []Effect{}
	}
	p.recordTurn(text, result.Say, reply.EntryID)
	return result
}

// convoReply is the model's structured decision for one utterance.
type convoReply struct {
	Say     string
	EntryID string
}

// converse asks the LLM to answer the utterance or pick a catalog entry;
// it also returns the raw model output for the interaction log.
func (p *Plugin) converse(ctx context.Context, text string, entries []Entry) (convoReply, string, error) {
	p.compressIfNeeded(ctx)
	p.convoMu.Lock()
	summary, history := p.summary, append([]convoTurn(nil), p.history...)
	p.convoMu.Unlock()
	messages := buildConvoMessages(p.systemTemplate(ctx), text, entries, summary, history)
	content, err := p.llmFn(ctx, messages, true, 0)
	if err != nil {
		return convoReply{}, "", fmt.Errorf("voice conversation model failed: %w", err)
	}
	return parseConvoReply(content, entries), content, nil
}

// recordTurn appends one exchange to the dialogue history, enforcing the
// hard turn cap. The assistant turn is stored in the same JSON protocol the
// system template demands: recording plain say text taught the model to drop
// the JSON envelope on later turns (observed live: the second "open chat"
// command came back as plain text and no entry was dispatched). entryID is
// "" for direct answers and failures.
func (p *Plugin) recordTurn(text, say, entryID string) {
	p.convoMu.Lock()
	defer p.convoMu.Unlock()
	p.history = append(p.history, convoTurn{Role: "user", Content: text}, convoTurn{Role: "assistant", Content: assistantHistory(say, entryID)})
	if len(p.history) > convoMaxTurns {
		p.history = append([]convoTurn(nil), p.history[len(p.history)-convoMaxTurns:]...)
	}
}

// assistantHistory renders one assistant turn as canonical protocol JSON.
func assistantHistory(say, entryID string) string {
	id := strings.TrimSpace(entryID)
	if id == "" {
		id = "none"
	}
	content, err := json.Marshal(map[string]string{"say": say, "entry_id": id})
	if err != nil {
		return say
	}
	return string(content)
}

// convoChars estimates the retained context size in runes.
func convoChars(summary string, history []convoTurn) int {
	total := len([]rune(summary))
	for _, turn := range history {
		total += len([]rune(turn.Content))
	}
	return total
}

// compressIfNeeded folds the oldest half of the history into the running
// summary when the retained context exceeds convoCharBudget. On
// summarization failure the oldest turns are dropped anyway — a degraded
// context must never block the live conversation.
func (p *Plugin) compressIfNeeded(ctx context.Context) {
	p.convoMu.Lock()
	if convoChars(p.summary, p.history) <= convoCharBudget || len(p.history) < 4 {
		p.convoMu.Unlock()
		return
	}
	// Fold at least the oldest half; keep folding while the remainder stays
	// over budget, always leaving the 4 newest turns untouched.
	foldCount := len(p.history) / 2
	for convoChars(p.summary, p.history[foldCount:]) > convoCharBudget && len(p.history)-foldCount > 4 {
		foldCount++
	}
	fold := append([]convoTurn(nil), p.history[:foldCount]...)
	previous := p.summary
	p.convoMu.Unlock()

	summary, err := p.summarizeTurns(ctx, previous, fold)

	p.convoMu.Lock()
	// Re-check under the lock: a concurrent exchange may have appended turns,
	// but folded turns are identified by content position, so drop by length.
	if len(p.history) >= len(fold) {
		p.history = append([]convoTurn(nil), p.history[len(fold):]...)
	}
	folded := len(fold)
	summarized := err == nil && strings.TrimSpace(summary) != ""
	if summarized {
		p.summary = strings.TrimSpace(summary)
	}
	p.convoMu.Unlock()
	if summarized {
		p.logEvent(fmt.Sprintf("对话上下文超预算：折叠 %d 条最旧轮次进摘要", folded))
	} else {
		p.logEvent(fmt.Sprintf("对话上下文超预算：摘要失败，丢弃 %d 条最旧轮次", folded))
	}
}

const convoSummaryTemplate = `把一段语音对话的旧部分压缩成简短摘要，合并进已有摘要。
规则：保留用户的目标、已经执行过的操作和结果、重要事实；不超过 200 字；直接输出摘要文本，不要任何额外内容。

已有摘要：
{{summary}}

旧对话：
{{turns}}`

// summarizeTurns compresses folded turns into the running summary.
func (p *Plugin) summarizeTurns(ctx context.Context, previous string, fold []convoTurn) (string, error) {
	var lines []string
	for _, turn := range fold {
		role := "用户"
		content := turn.Content
		if turn.Role == "assistant" {
			role = "助手"
			// Assistant history is protocol JSON; the summarizer wants speech.
			var parsed struct {
				Say string `json:"say"`
			}
			if json.Unmarshal([]byte(content), &parsed) == nil && parsed.Say != "" {
				content = parsed.Say
			}
		}
		lines = append(lines, role+"："+content)
	}
	prompt := strings.ReplaceAll(p.summaryTemplate(ctx), "{{summary}}", previous)
	prompt = strings.ReplaceAll(prompt, "{{turns}}", strings.Join(lines, "\n"))
	content, err := p.llmFn(ctx, []map[string]string{
		{"role": "user", "content": prompt},
	}, false, 0)
	if err != nil {
		return "", err
	}
	return content, nil
}

const convoSystemTemplate = `你是 Viewer 桌面的语音助手，和用户进行连续的语音对话，你的回复会被直接朗读出来。
规则：
- 用口语化的简体中文回复，简短（最多三句话），不要用 markdown、列表或代码。
- 当用户想操作桌面功能时，从下方「可用功能目录」中选择最匹配的一个条目执行；语音转写可能有识别噪声（中英混说、同音错字、术语误识），按语义理解，不要逐字匹配。
- 当用户只是提问或闲聊（包括询问"有什么功能"）时，根据目录和自己的知识直接回答，不执行条目。
- 只输出 JSON：{"say":"要朗读给用户的话","entry_id":"目录条目的id"}；不执行条目时 entry_id 填 "none"。
{{catalog_section}}`

// buildConvoMessages assembles the prompt: system rules + catalog, running
// summary, recent turns, and the current utterance. The system template is
// the config-store override or the built-in default.
func buildConvoMessages(systemTemplate, text string, entries []Entry, summary string, history []convoTurn) []map[string]string {
	catalogSection := "\n当前没有任何可语音操作的功能。"
	if len(entries) > 0 {
		encoded, _ := json.MarshalIndent(entries, "", "  ")
		catalogSection = "\n可用功能目录：\n" + string(encoded)
	}
	messages := []map[string]string{
		{"role": "system", "content": strings.ReplaceAll(systemTemplate, "{{catalog_section}}", catalogSection)},
	}
	if strings.TrimSpace(summary) != "" {
		messages = append(messages, map[string]string{"role": "system", "content": "此前对话的摘要：" + summary})
	}
	for _, turn := range history {
		messages = append(messages, map[string]string{"role": turn.Role, "content": turn.Content})
	}
	return append(messages, map[string]string{"role": "user", "content": text})
}

// parseConvoReply tolerantly extracts {"say","entry_id"} from LLM output and
// validates the entry id against the catalog; unknown/empty/"none" all map
// to a direct answer.
func parseConvoReply(content string, entries []Entry) convoReply {
	trimmed := strings.TrimSpace(content)
	start, end := strings.Index(trimmed, "{"), strings.LastIndex(trimmed, "}")
	if start < 0 || end < start {
		return convoReply{Say: trimmed}
	}
	var raw struct {
		Say     string `json:"say"`
		EntryID string `json:"entry_id"`
	}
	if json.Unmarshal([]byte(trimmed[start:end+1]), &raw) != nil {
		return convoReply{Say: trimmed}
	}
	reply := convoReply{Say: strings.TrimSpace(raw.Say)}
	id := strings.TrimSpace(raw.EntryID)
	if id == "" || strings.EqualFold(id, "none") {
		return reply
	}
	for _, entry := range entries {
		if entry.ID == id {
			reply.EntryID = id
			return reply
		}
	}
	return reply
}

// handleTurnFrame announces completed turns when the loop is enabled: the
// latest reply is read aloud via chat's own read-latest action, so the
// speakable rewrite logic is reused unchanged.
func (p *Plugin) handleTurnFrame(frame busclient.Frame) {
	value, ok := pluginrpc.Object(frame)
	if !ok {
		return
	}
	phase, _ := value["phase"].(string)
	reason, _ := value["stop_reason"].(string)
	chatID, _ := value["chat_id"].(string)
	roleName, _ := value["role_name"].(string)
	if phase != "completed" || reason == "cancelled" || chatID == "" || !p.announcementsEnabled() {
		return
	}
	// Failed turns have no fresh assistant reply to read out; announce the
	// failure directly instead of reading a stale older message.
	if reason == "error" {
		if roleName == "" {
			roleName = "某个角色"
		}
		if p.announceFn != nil {
			p.announceFn(roleName+"的任务执行失败，请查看聊天", chatID)
		}
		p.logEvent("主动播报：" + roleName + "的任务执行失败")
		return
	}
	invoked, err := p.call(context.Background(), "chat:_:voice:invoke", map[string]any{
		"entry_id": "action:read-latest", "instance_id": chatID, "text": "",
	})
	var say string
	switch {
	case err != nil:
		say = "聊天任务完成，但读取回复失败"
	case invoked.OK && invoked.Say != "":
		say = invoked.Say
	default:
		say = "聊天任务完成"
	}
	if roleName != "" && !strings.Contains(say, roleName) {
		say = roleName + "：" + say
	}
	if p.announceFn != nil {
		p.announceFn(say, chatID)
	}
	p.logEvent("主动播报：" + say)
}

// llmCompleteViaBus calls the global llm plugin over the bus.
func (p *Plugin) llmCompleteViaBus(ctx context.Context, messages []map[string]string, jsonMode bool, timeoutSeconds int) (string, error) {
	if p.client == nil {
		return "", errors.New("bus unavailable")
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	value, err := p.client.Request(ctx, "llm:_:complete", map[string]any{
		"messages": messages, "json_mode": jsonMode, "timeout_seconds": timeoutSeconds,
		// Voice is latency-sensitive: ask thinking-capable endpoints to skip
		// reasoning. Passed through verbatim by the llm plugin; endpoints that
		// don't know the field ignore or reject it (surfaced as llm_error).
		"extra_body": map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}},
	}, time.Duration(timeoutSeconds+15)*time.Second)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var result struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(encoded, &result); err != nil || result.Content == "" {
		return "", errors.New("malformed llm reply")
	}
	return result.Content, nil
}

// invokeViaBus calls the owning plugin's invoke channel over the bus.
func (p *Plugin) invokeViaBus(ctx context.Context, channel string, payload map[string]any) (invokeResult, error) {
	var result invokeResult
	if p.client == nil {
		return result, errors.New("bus unavailable")
	}
	value, err := p.client.Request(ctx, channel, payload, 2*time.Minute)
	if err != nil {
		var rpcErr *busclient.RPCError
		if errors.As(err, &rpcErr) {
			return result, errors.New(rpcErr.Message)
		}
		return result, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		return result, errors.New("malformed invoke reply")
	}
	return result, nil
}

// llmNotConfigured reports whether err is the llm plugin's not_configured
// RPC error.
func llmNotConfigured(err error) bool {
	var rpcErr *busclient.RPCError
	return errors.As(err, &rpcErr) && rpcErr.Code == "not_configured"
}
