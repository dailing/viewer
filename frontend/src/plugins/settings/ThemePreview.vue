<script setup lang="ts">
/**
 * 主题效果预览: a static mock of the shell's common elements — titlebar,
 * surface variants, chat boxes (with a markdown sample), buttons, status
 * colors and a form input — all painted from the live --color-* /
 * --markdown-* / --syntax-* custom properties. Theme edits above apply to
 * .app-shell immediately, and 消息样式 overrides likewise, so this block is
 * always a real-time WYSIWYG preview; no local state needed.
 */
</script>

<template>
  <div class="preview">
    <div class="preview-caption">效果预览（实时，含下方「消息样式」的改动）</div>
    <div class="preview-window">
      <div class="preview-titlebar">
        <i class="bi bi-chat-left-text"></i> 聊天 · 抬头底色/文字
      </div>
      <div class="preview-canvas">
        <div class="preview-chips">
          <span class="preview-chip raised">浮起面</span>
          <span class="preview-chip hover">悬停</span>
          <span class="preview-chip selected">选中</span>
          <span class="preview-chip soft">主题浅底</span>
          <span class="preview-chip overlay">遮罩层</span>
        </div>

        <div class="preview-box">
          <div class="preview-box-top">
            <span class="preview-role-label"><i class="bi bi-person"></i> 我</span>
            <span class="preview-time">12:01</span>
          </div>
          <div class="preview-box-body">帮我把主题调成护眼一点的配色</div>
        </div>

        <div class="preview-box">
          <div class="preview-box-top">
            <span class="preview-role-label"><i class="bi bi-robot"></i> 助手</span>
            <span class="preview-meta-detail">hermes · anthropic · 42% ctx</span>
            <span class="preview-time">12:02</span>
          </div>
          <div class="preview-box-body markdown-content">
            <p>好的，这段是正文，含<strong>加粗</strong>、<a href="#" @click.prevent>链接</a>和<code>inline code</code>。</p>
            <pre><code>theme.setVar("accent", "#58749a")</code></pre>
          </div>
        </div>

        <div class="preview-row">
          <button type="button" class="btn btn-sm btn-primary">主要按钮</button>
          <button type="button" class="btn btn-sm btn-outline-secondary">次要按钮</button>
          <span class="preview-status ok"><i class="bi bi-check-circle"></i> 成功</span>
          <span class="preview-status warn"><i class="bi bi-exclamation-triangle"></i> 警告</span>
          <span class="preview-status err"><i class="bi bi-x-circle"></i> 危险</span>
          <span class="preview-status inf"><i class="bi bi-info-circle"></i> 信息</span>
        </div>

        <input class="preview-input" placeholder="输入框 — 点击看焦点色">

        <div class="preview-texts">
          <span class="t">正文文字</span>
          <span class="m">次要文字</span>
          <span class="s">弱化文字</span>
          <span class="inv">反色文字</span>
          <span class="b">边框</span>
          <span class="bs">强调边框</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.preview {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-top: 4px;
}

.preview-caption {
  color: color-mix(in srgb, var(--color-text-muted) 72%, transparent);
  font-size: 11px;
}

.preview-window {
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  overflow: hidden;
}

.preview-titlebar {
  align-items: center;
  background: var(--color-titlebar);
  color: var(--color-titlebar-text);
  display: flex;
  font-size: var(--font-size-ui-small);
  gap: 6px;
  padding: 5px 10px;
}

.preview-canvas {
  background: var(--color-canvas);
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px;
}

.preview-chips,
.preview-row,
.preview-texts {
  align-items: center;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.preview-chip {
  border-radius: var(--radius-sm);
  font-size: 11px;
  padding: 2px 8px;
}

.preview-chip.raised {
  background: var(--color-surface-raised);
  border: 1px solid var(--color-border);
  color: var(--color-text);
}

.preview-chip.hover {
  background: var(--color-surface-hover);
  border: 1px solid var(--color-border);
  color: var(--color-text);
}

.preview-chip.selected {
  background: var(--color-surface-selected);
  border: 1px solid var(--color-border);
  color: var(--color-accent);
}

.preview-chip.soft {
  background: var(--color-accent-soft);
  border: 1px solid var(--color-border);
  color: var(--color-accent);
}

.preview-chip.overlay {
  background: var(--color-overlay);
  color: var(--color-text-inverse);
}

.preview-box-top {
  align-items: center;
  color: var(--color-text-muted);
  display: flex;
  font-size: var(--font-size-ui);
  gap: 6px;
  padding: 0 2px 2px;
}

.preview-role-label {
  align-items: center;
  display: inline-flex;
  font-weight: 700;
  gap: 4px;
}

.preview-meta-detail {
  color: color-mix(in srgb, var(--color-text-muted) 72%, transparent);
  font-size: 10px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.preview-time {
  color: var(--color-text-muted);
  font-size: 11px;
  margin-left: auto;
}

.preview-box-body {
  background: var(--color-surface-muted);
  border-radius: var(--radius-md);
  font-size: var(--font-size-ui);
  padding: 8px 10px;
}

.preview-status {
  align-items: center;
  display: inline-flex;
  font-size: 11px;
  gap: 3px;
}

.preview-status.ok { color: var(--color-success); }
.preview-status.warn { color: var(--color-warning); }
.preview-status.err { color: var(--color-danger); }
.preview-status.inf { color: var(--color-info); }

.preview-input {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  color: var(--color-text);
  font-size: var(--font-size-ui);
  padding: 4px 8px;
}

.preview-input:focus {
  border-color: var(--color-focus);
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--color-focus) 30%, transparent);
  outline: none;
}

.preview-texts {
  font-size: var(--font-size-ui);
}

.preview-texts .t { color: var(--color-text); }
.preview-texts .m { color: var(--color-text-muted); }
.preview-texts .s { color: var(--color-text-subtle); }

.preview-texts .inv {
  background: var(--color-accent);
  border-radius: var(--radius-sm);
  color: var(--color-text-inverse);
  padding: 0 6px;
}

.preview-texts .b,
.preview-texts .bs {
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  padding: 0 6px;
}

.preview-texts .b { border: 1px solid var(--color-border); }
.preview-texts .bs { border: 1px solid var(--color-border-strong); }
</style>
