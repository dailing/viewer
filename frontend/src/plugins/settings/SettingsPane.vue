<script setup lang="ts">
/**
 * Settings pane: the unified, full-page configuration surface (opened from
 * the Dock gear). All toggles are browser-local (localStorage) and take
 * effect immediately; the backend section drives the gateway admin API.
 * Sections: 布局 (open mode), 聊天 (virtual space), Dock (hover expand
 * delay), 消息样式 (markdown theme overrides), 后端 (restart / build).
 */
import { inject, onMounted, ref } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import { useChatSettingsStore } from "../../stores/chatSettings";
import { useDockSettingsStore } from "../../stores/dockSettings";
import { useLayoutStore } from "../../stores/layout";
import { useMarkdownStyleStore } from "../../stores/markdownStyle";
import type { MarkdownStyleOverrides } from "../../stores/markdownStyle";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("SettingsPane requires PluginPaneHost");
const ctx: PluginCtx = injectedCtx;

const layout = useLayoutStore();
const chatSettings = useChatSettingsStore();
const dockSettings = useDockSettingsStore();
const markdownStyle = useMarkdownStyleStore();

onMounted(() => {
  ctx.setChrome({ title: "设置" });
});

/* ---- 消息样式 (markdown theme overrides) ---- */

const STYLE_DEFAULTS: Required<MarkdownStyleOverrides> = {
  bodyFontSize: 15, bodyLineHeight: 1.65, bodyColor: "#404449", strongColor: "#1f4e79",
  linkColor: "#58749a", codeFontSize: 13, codeBackground: "#f5f5f5", syntaxBackground: "#f5f5f5",
  borderColor: "#e3e4e6",
};
const NUMBER_FIELDS = new Set<keyof MarkdownStyleOverrides>(["bodyFontSize", "bodyLineHeight", "codeFontSize"]);

function overrideValue(field: keyof MarkdownStyleOverrides): string {
  const value = markdownStyle.overrides[field];
  return value === undefined ? String(STYLE_DEFAULTS[field]) : String(value);
}

function setOverride(field: keyof MarkdownStyleOverrides, raw: string): void {
  if (raw === "") {
    markdownStyle.set(field, undefined);
    return;
  }
  if (NUMBER_FIELDS.has(field)) {
    const numeric = Number(raw);
    markdownStyle.set(field, Number.isFinite(numeric) && numeric > 0 ? numeric : undefined);
    return;
  }
  markdownStyle.set(field, raw);
}

/* ---- 后端 (gateway admin API) ---- */

const restarting = ref(false);
const building = ref(false);

const RESTART_POLL_MS = 800;
const RESTART_TIMEOUT_MS = 60_000;
// Build (vite + go build) takes minutes; the gateway stays up until the
// build finishes, so we must first observe it going DOWN before polling for
// recovery — otherwise the still-running old server looks like "recovered".
const BUILD_TIMEOUT_MS = 8 * 60_000;
const BUILD_POLL_MS = 2000;

/**
 * Graceful backend restart (gateway admin API, framework v0.34):
 * POST /api/admin/restart → the gateway spawns a same-args replacement and
 * takes the graceful shutdown path (drains running turns ≤10s); the
 * replacement binds the ports only after the old pid is gone. We poll GET /
 * until the new gateway answers, then reload the shell. The TS BusClient
 * auto-reconnects, but a fresh page load is the cleanest recovery for the
 * whole UI (all stores re-init; module chat cache resets).
 */
async function restartBackend(): Promise<void> {
  if (restarting.value) return;
  const confirmed = window.confirm(
    "重启后端（viewerd）？\n在途任务会先排空（最多 10 秒），完成后页面将自动刷新恢复。"
  );
  if (!confirmed) return;
  restarting.value = true;
  try {
    const resp = await fetch("/api/admin/restart", { method: "POST" });
    if (!resp.ok) {
      const body = await resp.text().catch(() => "");
      window.alert(`重启失败：HTTP ${resp.status} ${body}`);
      restarting.value = false;
      return;
    }
  } catch {
    // The 202 may be lost when the gateway closes right after accepting the
    // restart (connection reset before the body arrives) — treat it as
    // accepted and keep polling for the new gateway.
  }
  const deadline = Date.now() + RESTART_TIMEOUT_MS;
  for (;;) {
    try {
      const probe = await fetch("/", { cache: "no-store" });
      if (probe.ok) {
        location.reload();
        return;
      }
    } catch {
      // Gateway not up yet (old process draining / replacement binding).
    }
    if (Date.now() > deadline) {
      window.alert("重启超时，请检查服务状态后手动刷新页面。");
      restarting.value = false;
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, RESTART_POLL_MS));
  }
}

/**
 * Build & restart: POST /api/admin/build-restart → the gateway runs
 * web/build-release.sh in the background (the old server keeps serving
 * during the build) and takes the graceful restart path only when the build
 * succeeds. Poll in two phases: first until GET / FAILS (build done, restart
 * began), then until it answers again, then reload. A failed build never
 * downs the server, so phase 1 times out with a pointer to the build log.
 */
async function buildAndRestart(): Promise<void> {
  if (building.value || restarting.value) return;
  const confirmed = window.confirm(
    "构建并重启（viewerd）？\n先在后台跑 web/build-release.sh（约 1-2 分钟，期间服务不中断），构建成功后自动重启，完成后页面自动刷新。构建失败则保持现状不动。"
  );
  if (!confirmed) return;
  building.value = true;
  try {
    const resp = await fetch("/api/admin/build-restart", { method: "POST" });
    if (!resp.ok) {
      const body = await resp.text().catch(() => "");
      window.alert(`构建请求失败：HTTP ${resp.status} ${body}`);
      building.value = false;
      return;
    }
  } catch {
    window.alert("构建请求未能送达，请检查服务状态。");
    building.value = false;
    return;
  }
  const deadline = Date.now() + BUILD_TIMEOUT_MS;
  // Phase 1: wait for the gateway to go down (build finished, restart began).
  for (;;) {
    try {
      await fetch("/", { cache: "no-store" });
    } catch {
      break; // server down — restart in progress
    }
    if (Date.now() > deadline) {
      window.alert("构建超时：后端一直在运行，构建可能失败了（详情见 /tmp/viewerd-build.log）。");
      building.value = false;
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, BUILD_POLL_MS));
  }
  // Phase 2: wait for the replacement to come up, then reload.
  for (;;) {
    try {
      const probe = await fetch("/", { cache: "no-store" });
      if (probe.ok) {
        location.reload();
        return;
      }
    } catch {
      // replacement still binding
    }
    if (Date.now() > deadline) {
      window.alert("重启超时，请检查服务状态后手动刷新页面。");
      building.value = false;
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, BUILD_POLL_MS));
  }
}
</script>

<template>
  <section class="settings-pane">
    <div class="settings-group">
      <div class="settings-group-title"><i class="bi bi-layout-split"></i> 布局</div>
      <label class="settings-field">
        <span>打开方式</span>
        <span class="settings-choice">
          <button
            type="button"
            class="settings-choice-btn"
            :class="{ active: layout.openMode === 'new' }"
            title="新面板：分屏打开新面板"
            @click="layout.setOpenMode('new')"
          ><i class="bi bi-window-plus"></i> 新面板</button>
          <button
            type="button"
            class="settings-choice-btn"
            :class="{ active: layout.openMode === 'replace' }"
            title="原位替换：直接替换当前面板内容（适合手机单面板）"
            @click="layout.setOpenMode('replace')"
          ><i class="bi bi-front"></i> 原位替换</button>
        </span>
      </label>
      <div class="settings-hint">按浏览器本地保存。</div>
    </div>

    <div class="settings-group">
      <div class="settings-group-title"><i class="bi bi-chat-left-text"></i> 聊天</div>
      <label class="settings-field">
        <span>阅读留白（消息末尾留一屏空白）</span>
        <button
          type="button"
          class="settings-choice-btn"
          :class="{ active: chatSettings.virtualSpace }"
          :title="chatSettings.virtualSpace ? '已开启，点击关闭' : '已关闭，点击开启'"
          @click="chatSettings.toggleVirtualSpace()"
        ><i class="bi bi-distribute-vertical"></i> {{ chatSettings.virtualSpace ? "开启" : "关闭" }}</button>
      </label>
    </div>

    <div class="settings-group">
      <div class="settings-group-title"><i class="bi bi-dock-left"></i> Dock</div>
      <label class="settings-field">
        <span>悬停展开延迟（ms）</span>
        <input
          type="number"
          min="0"
          step="100"
          :value="dockSettings.hoverExpandMs"
          @change="dockSettings.setHoverExpandMs(Number(($event.target as HTMLInputElement).value))"
        >
      </label>
      <div class="settings-hint">0 = 立即展开。</div>
    </div>

    <div class="settings-group">
      <div class="settings-group-title">
        <span><i class="bi bi-palette"></i> 消息样式</span>
        <button type="button" class="btn btn-sm btn-outline-secondary" @click="markdownStyle.reset()">恢复默认</button>
      </div>
      <label class="settings-field">
        <span>正文字号</span>
        <input type="number" min="10" max="24" step="1" :value="overrideValue('bodyFontSize')" @change="setOverride('bodyFontSize', ($event.target as HTMLInputElement).value)">
      </label>
      <label class="settings-field">
        <span>正文行高</span>
        <input type="number" min="1.1" max="2.4" step="0.05" :value="overrideValue('bodyLineHeight')" @change="setOverride('bodyLineHeight', ($event.target as HTMLInputElement).value)">
      </label>
      <label class="settings-field">
        <span>正文颜色</span>
        <input type="color" :value="overrideValue('bodyColor')" @input="setOverride('bodyColor', ($event.target as HTMLInputElement).value)">
      </label>
      <label class="settings-field">
        <span>加粗颜色</span>
        <input type="color" :value="overrideValue('strongColor')" @input="setOverride('strongColor', ($event.target as HTMLInputElement).value)">
      </label>
      <label class="settings-field">
        <span>链接颜色</span>
        <input type="color" :value="overrideValue('linkColor')" @input="setOverride('linkColor', ($event.target as HTMLInputElement).value)">
      </label>
      <label class="settings-field">
        <span>代码字号</span>
        <input type="number" min="9" max="20" step="1" :value="overrideValue('codeFontSize')" @change="setOverride('codeFontSize', ($event.target as HTMLInputElement).value)">
      </label>
      <label class="settings-field">
        <span>行内代码底色</span>
        <input type="color" :value="overrideValue('codeBackground')" @input="setOverride('codeBackground', ($event.target as HTMLInputElement).value)">
      </label>
      <label class="settings-field">
        <span>代码块底色</span>
        <input type="color" :value="overrideValue('syntaxBackground')" @input="setOverride('syntaxBackground', ($event.target as HTMLInputElement).value)">
      </label>
      <label class="settings-field">
        <span>边框颜色</span>
        <input type="color" :value="overrideValue('borderColor')" @input="setOverride('borderColor', ($event.target as HTMLInputElement).value)">
      </label>
    </div>

    <div class="settings-group">
      <div class="settings-group-title"><i class="bi bi-hdd-network"></i> 后端</div>
      <div class="settings-field">
        <span>服务（viewerd）</span>
        <span class="settings-choice">
          <button
            type="button"
            class="settings-choice-btn"
            :disabled="restarting || building"
            title="重启后端（viewerd）：排空在途任务后重启整个服务，完成后页面自动刷新"
            @click="restartBackend"
          ><i class="bi" :class="restarting ? 'bi-arrow-repeat' : 'bi-arrow-clockwise'"></i> {{ restarting ? "重启中…" : "重启后端" }}</button>
          <button
            type="button"
            class="settings-choice-btn"
            :disabled="building || restarting"
            title="构建并重启：后台运行 web/build-release.sh，构建成功后自动重启并刷新页面；构建失败则保持现状"
            @click="buildAndRestart"
          ><i class="bi" :class="building ? 'bi-hourglass-split' : 'bi-hammer'"></i> {{ building ? "构建中…" : "构建并重启" }}</button>
        </span>
      </div>
    </div>
  </section>
</template>

<style scoped>
.settings-pane {
  display: flex;
  flex-direction: column;
  gap: 18px;
  height: 100%;
  overflow-y: auto;
  padding: 16px 20px;
}

.settings-group {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-width: 460px;
}

.settings-group-title {
  align-items: center;
  border-bottom: 1px solid var(--color-border);
  color: var(--color-text);
  display: flex;
  font-size: var(--font-size-ui);
  font-weight: 700;
  gap: 6px;
  justify-content: space-between;
  padding-bottom: 4px;
}

.settings-field {
  align-items: center;
  display: grid;
  font-size: var(--font-size-ui);
  gap: 8px;
  grid-template-columns: 1fr auto;
  margin: 0;
}

.settings-field > span:first-child {
  color: var(--color-text-muted);
}

.settings-field input[type="number"] {
  width: 84px;
}

.settings-field input[type="color"] {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  height: 22px;
  padding: 1px;
  width: 42px;
}

.settings-hint {
  color: color-mix(in srgb, var(--color-text-muted) 72%, transparent);
  font-size: 11px;
}

.settings-choice {
  display: inline-flex;
  gap: 6px;
}

.settings-choice-btn {
  align-items: center;
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  color: var(--color-text-muted);
  display: inline-flex;
  font-size: var(--font-size-ui);
  gap: 5px;
  padding: 3px 10px;
}

.settings-choice-btn:hover:not(:disabled) {
  background: var(--color-surface-hover);
  color: var(--color-text);
}

.settings-choice-btn.active {
  background: var(--color-surface-selected);
  border-color: var(--color-accent);
  color: var(--color-accent);
}

.settings-choice-btn:disabled {
  opacity: 0.55;
}
</style>
