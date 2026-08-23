package chat

import (
	"context"
	"fmt"
	"strings"

	"viewer/sdk/go/busclient"
)

// Voice actions (framework: voice actions, slice 2). Chat publishes its
// voice-addressable entries on the retained `voice-catalog:_:chat` mailbox and
// executes them behind `chat:_:voice:invoke`. Intent resolution and the
// cross-plugin catalog live in the voice-control plugin; the entry/result
// shapes below are the wire contract with it.

// voiceEntry is one catalog entry as published on voice-catalog:chat.
type voiceEntry struct {
	ID          string   `json:"id"`
	Plugin      string   `json:"plugin"`
	Kind        string   `json:"kind"` // "open_instance" | "action"
	Title       string   `json:"title"`
	Keywords    []string `json:"keywords,omitempty"`
	Description string   `json:"description,omitempty"`
	PaneType    string   `json:"pane_type,omitempty"`
	InstanceID  string   `json:"instance_id,omitempty"`
	Channel     string   `json:"channel"`
}

// VoiceEffect tells the frontend (via voice-control) to take a UI action:
// start dictation into a specific chat pane. open_instance effects are added
// by voice-control itself for open_instance entries.
type VoiceEffect struct {
	Type       string `json:"type"`
	Plugin     string `json:"plugin,omitempty"`
	PaneType   string `json:"pane_type,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
}

// voiceInvokeResult is always a spoken-outcome envelope: Say is read aloud
// by the client even for failures.
type voiceInvokeResult struct {
	OK      bool          `json:"ok"`
	Say     string        `json:"say"`
	Effects []VoiceEffect `json:"effects"`
}

const (
	voiceActionReadLatest = "action:read-latest"
	voiceActionDictate    = "action:dictate"
	voiceActionStop       = "action:stop"
	voiceActionStatus     = "action:status"
	voiceOpenPrefix       = "open-chat:"
	voiceCatalogChannel   = "voice-catalog:_:chat"
	voiceInvokeChannel    = "chat:_:voice:invoke"
)

// buildVoiceCatalog lists every voice-addressable chat entry: one open entry
// per chat plus the fixed action set on the active chat.
func buildVoiceCatalog(chats []Chat) []voiceEntry {
	entries := make([]voiceEntry, 0, len(chats)+4)
	for _, item := range chats {
		keywords := []string{item.Name}
		if base := baseName(item.Root); base != "" && base != item.Name {
			keywords = append(keywords, base)
		}
		entries = append(entries, voiceEntry{
			ID: voiceOpenPrefix + item.ID, Plugin: "chat", Kind: "open_instance",
			Title: "打开聊天 " + item.Name, Keywords: keywords,
			Description: "切换到名为「" + item.Name + "」的聊天",
			PaneType:    "chat", InstanceID: item.ID, Channel: voiceInvokeChannel,
		})
	}
	entries = append(entries,
		voiceEntry{ID: voiceActionReadLatest, Plugin: "chat", Kind: "action", Title: "读最新回复", Keywords: []string{"读", "回复", "最新", "念"}, Description: "朗读当前聊天最新一条助手回复（改写为适合朗读的口语摘要）", Channel: voiceInvokeChannel},
		voiceEntry{ID: voiceActionDictate, Plugin: "chat", Kind: "action", Title: "输入新消息", Keywords: []string{"输入", "发消息", "发送消息", "打字"}, Description: "开始语音口述，把说的话写进当前聊天的输入框", Channel: voiceInvokeChannel},
		voiceEntry{ID: voiceActionStop, Plugin: "chat", Kind: "action", Title: "停止当前运行", Keywords: []string{"停止", "停下", "取消运行"}, Description: "停止当前聊天正在运行的任务", Channel: voiceInvokeChannel},
		voiceEntry{ID: voiceActionStatus, Plugin: "chat", Kind: "action", Title: "汇报运行状态", Keywords: []string{"状态", "运行", "进展"}, Description: "汇报哪些聊天正在运行、有多少消息在排队", Channel: voiceInvokeChannel},
	)
	return entries
}

// publishVoiceCatalog sets the retained catalog mailbox. Called on start and
// after every chat mutation so voice-control always sees a current catalog.
func (p *Plugin) publishVoiceCatalog() {
	if p.client == nil {
		return
	}
	chats, err := p.store.chats()
	if err != nil {
		return
	}
	_ = p.client.Set(context.Background(), voiceCatalogChannel, map[string]any{"entries": buildVoiceCatalog(chats)})
}

func baseName(path string) string {
	trimmed := strings.TrimRight(path, "/\\")
	if trimmed == "" {
		return ""
	}
	parts := strings.FieldsFunc(trimmed, func(r rune) bool { return r == '/' || r == '\\' })
	return parts[len(parts)-1]
}

const speakableTemplate = `把下面这段助手回复改写成适合朗读的口语化中文摘要。
规则：去掉 commit hash、文件路径、URL、代码块和命令行；保留结论和关键事实；最多三句话；直接输出朗读文本，不要任何额外内容。

助手回复：
{{text}}`

// speakableAloud rewrites dense agent output into TTS-friendly prose. On LLM
// failure the caller falls back to speakableFallback.
func speakableAloud(ctx context.Context, complete llmCompleter, text string) (string, error) {
	prompt := strings.ReplaceAll(speakableTemplate, "{{text}}", text)
	messages := []map[string]string{
		{"role": "system", "content": "你是朗读改写器，只输出适合朗读的中文口语摘要。"},
		{"role": "user", "content": prompt},
	}
	result, err := complete(ctx, messages, false, 0)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

// speakableFallback degrades a reply for TTS without an LLM: fenced code
// blocks are dropped and the rest truncated to ~200 runes.
func speakableFallback(text string) string {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			kept = append(kept, line)
		}
	}
	runes := []rune(strings.Join(kept, "\n"))
	if len(runes) > 200 {
		return string(runes[:200]) + "……"
	}
	return string(runes)
}

func (p *Plugin) handleVoiceInvoke(frame busclient.Frame) {
	request, err := frameObject(frame)
	if err != nil {
		p.reply(frame, nil, err)
		return
	}
	entryID := strings.TrimSpace(requestString(request, "entry_id"))
	if entryID == "" {
		p.reply(frame, nil, errBadRequest)
		return
	}
	p.reply(frame, p.invokeVoiceEntry(p.ctx, entryID, requestString(request, "instance_id")), nil)
}

// invokeVoiceEntry executes one catalog entry. It never returns a transport
// error for user-facing failures — every outcome carries a Say line.
func (p *Plugin) invokeVoiceEntry(ctx context.Context, entryID, instanceID string) voiceInvokeResult {
	result := voiceInvokeResult{Effects: []VoiceEffect{}}
	targetChatID := instanceID
	if targetChatID == "" {
		targetChatID = p.activeChatID
	}

	switch {
	case strings.HasPrefix(entryID, voiceOpenPrefix):
		id := strings.TrimPrefix(entryID, voiceOpenPrefix)
		target, findErr := p.store.chat(id)
		if findErr != nil || target == nil {
			result.Say = "要打开的聊天不存在"
			return result
		}
		// Mirror ChatsPanel.activate: backend active state first; the
		// open_instance effect is appended by voice-control.
		p.activeChatID = id
		if err := p.store.setActiveChatID(id); err == nil && p.client != nil {
			_ = p.client.Set(context.Background(), "chat:_:active", id)
		}
		result.OK = true
		result.Say = "已打开聊天 " + target.Name

	case entryID == voiceActionReadLatest:
		if targetChatID == "" {
			result.Say = "当前没有激活的聊天"
			return result
		}
		message, queryErr := p.store.latestAssistantMessage(targetChatID)
		if queryErr != nil {
			result.Say = "读取消息失败"
			return result
		}
		if message == nil {
			result.Say = "这个聊天还没有助手回复"
			return result
		}
		spoken, speakErr := speakableAloud(ctx, p.llmFn, message.Text)
		if speakErr != nil || strings.TrimSpace(spoken) == "" {
			spoken = speakableFallback(message.Text)
		}
		result.OK = true
		if message.RoleName != "" {
			result.Say = message.RoleName + "的最新回复：" + spoken
		} else {
			result.Say = "最新回复：" + spoken
		}

	case entryID == voiceActionStatus:
		chats, listErr := p.store.chats()
		if listErr != nil {
			result.Say = "读取聊天列表失败"
			return result
		}
		running := p.runningChatIDs()
		names := []string{}
		for _, id := range running {
			name := id
			for _, item := range chats {
				if item.ID == id {
					name = item.Name
					break
				}
			}
			names = append(names, name)
		}
		queued := 0
		p.mu.Lock()
		for _, queue := range p.queues {
			queued += len(queue)
		}
		p.mu.Unlock()
		result.OK = true
		switch {
		case len(names) == 0 && queued == 0:
			result.Say = "当前没有正在运行的任务"
		case len(names) == 0:
			result.Say = fmt.Sprintf("当前没有正在运行的任务，有 %d 条消息在排队", queued)
		default:
			result.Say = fmt.Sprintf("有 %d 个聊天正在运行：%s", len(names), strings.Join(names, "、"))
			if queued > 0 {
				result.Say += fmt.Sprintf("，另有 %d 条消息在排队", queued)
			}
		}

	case entryID == voiceActionStop:
		if targetChatID == "" {
			result.Say = "当前没有激活的聊天"
			return result
		}
		stopped, stopErr := p.stopTurn(targetChatID, "", "")
		switch {
		case stopErr != nil:
			result.Say = "停止失败：" + stopErr.Error()
		case stopped:
			result.OK = true
			result.Say = "已停止当前运行"
		default:
			result.Say = "当前没有正在运行的任务"
		}

	case entryID == voiceActionDictate:
		if targetChatID == "" {
			result.Say = "当前没有激活的聊天，请先打开一个聊天"
			return result
		}
		result.OK = true
		result.Say = "请开始说，停顿几秒后自动结束"
		result.Effects = append(result.Effects, VoiceEffect{Type: "start_dictation", Plugin: "chat", PaneType: "chat", InstanceID: targetChatID})

	default:
		result.Say = "没听懂，请再说一遍"
	}
	return result
}
