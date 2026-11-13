<script setup lang="ts">
/**
 * Settings pane: the unified, full-page configuration surface (opened from
 * the Dock gear). Toggles are browser-local (localStorage) and take effect
 * immediately — except theme DEFINITIONS, which persist server-side via
 * instance-store (cross-browser/machine; the active theme id stays
 * browser-local); the backend section drives the gateway admin API.
 * Sections: 通用 (open mode + Dock hover delay), 外观与字号 (theme
 * selection/management, optional custom colors, live preview, browser-local
 * base/detail sizes), 聊天与通知 (virtual space + completion notifications),
 * 插件设置 (entry to 插件管理), 系统维护 (restart / build-restart / scheduled
 * restart toggle with live status). Content stays in one readable column;
 * category navigation moves above it when the pane itself is narrow.
 */
import { computed, inject, onBeforeUnmount, onMounted, ref } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import { useChatSettingsStore } from "../../stores/chatSettings";
import { useDockSettingsStore } from "../../stores/dockSettings";
import { useLayoutStore } from "../../stores/layout";
import { useThemeStore } from "../../stores/theme";
import type { BaseVars } from "../../stores/theme";
import { useTypographyStore } from "../../stores/typography";
import { CONTENT_SIZE_RANGE, DETAIL_SIZE_RANGE, TYPO_DETAILS, UI_SIZE_RANGE } from "../../stores/typography";
import ThemePreview from "./ThemePreview.vue";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("SettingsPane requires PluginPaneHost");
const ctx: PluginCtx = injectedCtx;

const SETTINGS_SECTIONS = [
  { id: "general", label: "通用", icon: "bi-sliders" },
  { id: "appearance", label: "外观与字号", icon: "bi-palette2" },
  { id: "chat", label: "聊天与通知", icon: "bi-chat-left-text" },
  { id: "plugins", label: "插件设置", icon: "bi-puzzle" },
  { id: "maintenance", label: "系统维护", icon: "bi-hdd-network" },
] as const;
type SettingsSection = (typeof SETTINGS_SECTIONS)[number]["id"];
const activeSection = ref<SettingsSection>("general");

function openPluginManager(): void {
  void ctx.shell.open({ paneType: "plugin-manager", instanceId: "main" });
}

const layout = useLayoutStore();
const chatSettings = useChatSettingsStore();

/** Turn-completion system notifications: enabling requests the browser
 *  Notification permission (a user gesture is required — the toggle click
 *  qualifies). A denial is surfaced next to the toggle instead of silently
 *  flipping on. */
const notificationHint = ref("");
async function toggleTurnNotifications(): Promise<void> {
  notificationHint.value = "";
  if (chatSettings.turnNotifications) {
    chatSettings.setTurnNotifications(false);
    return;
  }
  if (!("Notification" in window)) {
    notificationHint.value = "此浏览器不支持系统通知。";
    return;
  }
  if (Notification.permission === "denied") {
    notificationHint.value = "通知权限被浏览器拒绝——请在地址栏的站点设置中允许通知后再开启。";
    return;
  }
  if (Notification.permission !== "granted") {
    const result = await Notification.requestPermission();
    if (result !== "granted") {
      notificationHint.value = "未授予通知权限，开关未开启。";
      return;
    }
  }
  chatSettings.setTurnNotifications(true);
}
const dockSettings = useDockSettingsStore();
const theme = useThemeStore();
const themeMenu = ref<HTMLDetailsElement | null>(null);

function runThemeAction(action: () => void): void {
  action();
  if (themeMenu.value !== null) themeMenu.value.open = false;
}

onMounted(() => {
  ctx.setChrome({ title: "设置" });
  void refreshSchedStatus();
  schedPollTimer = setInterval(refreshSchedStatus, 5000);
});
onBeforeUnmount(() => {
  if (schedPollTimer !== null) clearInterval(schedPollTimer);
});

/* ---- 外观 (themes: base colors + advanced overrides) ---- */

const BASE_FIELDS: Array<{ key: keyof BaseVars; label: string }> = [
  { key: "canvas", label: "背景底色" },
  { key: "surface", label: "面板底色" },
  { key: "text", label: "文字颜色" },
  { key: "accent", label: "主题色" },
];

const HEX_COLOR_RE = /^#[0-9a-fA-F]{6}$/;

/** <input type="color"> only accepts #rrggbb; fall back for rgb()/named values. */
function colorInputValue(value: string | undefined): string {
  return value !== undefined && HEX_COLOR_RE.test(value) ? value : "#888888";
}

const codeColor = computed(() => theme.active.overrides.codeAccent ?? theme.active.base.accent);
function setCodeColor(value: string): void {
  theme.setOverride(theme.activeId, "codeAccent", value);
}
function resetCodeColor(): void {
  theme.setOverride(theme.activeId, "codeAccent", undefined);
}

function addTheme(): void {
  const name = window.prompt("新主题名称（以当前主题为模板复制）：", "自定义主题");
  if (name === null) return;
  theme.createTheme(name);
}

function renameActive(): void {
  const name = window.prompt("主题名称：", theme.active.name);
  if (name === null) return;
  theme.renameTheme(theme.activeId, name);
}

function removeActive(): void {
  if (!window.confirm(`删除主题「${theme.active.name}」？此操作不可撤销。`)) return;
  theme.deleteTheme(theme.activeId);
}

/* ---- 字号 (browser-local typography: 2 bases + pinned details) ---- */

const typography = useTypographyStore();

const TYPO_DETAIL_FIELDS = Object.entries(TYPO_DETAILS).map(([key, spec]) => ({ key, label: spec.label }));

/** Empty/invalid input restores the default (null); valid px are clamped
 *  by the store. */
function setUiSize(raw: string): void {
  typography.setUi(raw === "" ? null : Number(raw));
}

function setContentSize(raw: string): void {
  typography.setContent(raw === "" ? null : Number(raw));
}

function setDetailSize(key: string, raw: string): void {
  typography.setDetail(key, raw === "" ? null : Number(raw));
}

/* ---- 后端 (gateway admin API) ---- */

const restarting = ref(false);
const building = ref(false);

/**
 * Scheduled-restart state mirrors the gateway's in-memory state machine
 * (framework v0.44): none | building | armed | failed. Fetched on mount and
 * polled so the toggle also reflects schedules armed by agents over the
 * HTTP API (POST /api/admin/schedule-restart), not just this pane.
 */
type SchedStatus = "none" | "building" | "armed" | "failed";
const schedStatus = ref<SchedStatus>("none");
// True while this pane is running the wait-for-restart polling loops below.
const schedWaiting = ref(false);

const SCHED_STATUS_TEXT: Record<SchedStatus, string> = {
  none: "未计划",
  building: "已计划 · 后台构建中",
  armed: "已计划 · 等系统空闲后自动重启",
  failed: "构建失败 · 保持现状（可重试或取消）",
};
const schedStatusText = computed(() => SCHED_STATUS_TEXT[schedStatus.value]);

async function refreshSchedStatus(): Promise<void> {
  try {
    const probe = await fetch("/api/admin/schedule-restart", { cache: "no-store" });
    if (!probe.ok) return;
    const body = (await probe.json()) as { status?: string };
    if (body.status === "building" || body.status === "armed" || body.status === "failed") {
      schedStatus.value = body.status;
    } else {
      schedStatus.value = "none";
    }
  } catch {
    // Gateway momentarily unreachable (a restart firing) — keep last state.
  }
}

let schedPollTimer: ReturnType<typeof setInterval> | null = null;

const RESTART_POLL_MS = 800;
const SCHEDULED_POLL_MS = 3000;
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
 * replacement binds the ports only after the old pid is gone. We compare the
 * root response's X-Viewer-Process header with the pre-restart process, so a
 * fast old-server response cannot trigger a premature reload and a short
 * down/up window cannot be missed. The TS BusClient auto-reconnects, but a
 * fresh page load is the cleanest recovery for the whole UI (all stores
 * re-init; module chat cache resets).
 */
async function restartBackend(): Promise<void> {
  if (restarting.value) return;
  const confirmed = window.confirm(
    "重启后端（viewerd）？\n在途任务会先排空（最多 10 秒），完成后页面将自动刷新恢复。"
  );
  if (!confirmed) return;
  restarting.value = true;
  const startedAt = Date.now();
  const before = await fetch("/", { cache: "no-store" }).catch(() => null);
  const previousProcess = before?.ok ? before.headers.get("x-viewer-process") ?? "" : "";
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
        const currentProcess = probe.headers.get("x-viewer-process") ?? "";
        const isReplacement = previousProcess !== "" && currentProcess !== ""
          ? currentProcess !== previousProcess
          : Date.now() - startedAt >= 1500;
        if (isReplacement) {
          location.reload();
          return;
        }
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

/**
 * Scheduled build+restart: POST /api/admin/schedule-restart starts the
 * release build in the background (the gateway stays up); on success it
 * arms a one-shot deferred restart whose watchdog fires the graceful
 * restart path once the system is idle (no chat turn in flight, no voice
 * relay active). The gateway stays up while waiting, so phase 1 has no
 * deadline — waiting hours for a long agent turn is the whole point. A
 * failed build never arms: the GET status endpoint reports "failed" so we
 * stop waiting instead of polling forever; a "none" report means the
 * schedule was cancelled (here or by an agent over the HTTP API) and we
 * likewise stop waiting. Once the gateway goes down (restart fired), poll
 * until the replacement answers, then reload.
 */
async function scheduleRestart(): Promise<void> {
  if (schedWaiting.value || restarting.value || building.value) return;
  const confirmed = window.confirm(
    "空闲时构建并重启（viewerd）？\n先在后台跑构建（约 1-2 分钟，期间服务不中断）；构建成功后，等所有 agent 任务跑完、且没有录音进行中时自动重启；构建失败则保持现状不动。重启完成后页面自动刷新。可随时再次点击该按钮取消。"
  );
  if (!confirmed) return;
  const resp = await fetch("/api/admin/schedule-restart", { method: "POST" }).catch(() => null);
  if (resp === null || !resp.ok) {
    const body = resp === null ? "" : await resp.text().catch(() => "");
    window.alert(resp === null ? "计划重启请求未能送达，请检查服务状态。" : `计划重启失败：HTTP ${resp.status} ${body}`);
    return;
  }
  schedStatus.value = "building";
  schedWaiting.value = true;
  // Phase 1: wait for the gateway to go down (build done, idle reached,
  // restart fired). A failed build or a cancel surfaces via the status
  // endpoint.
  for (;;) {
    try {
      await fetch("/", { cache: "no-store" });
    } catch {
      break; // server down — restart in progress
    }
    try {
      const probe = await fetch("/api/admin/schedule-restart", { cache: "no-store" });
      if (probe.ok) {
        const body = (await probe.json()) as { status?: string };
        if (body.status === "failed") {
          schedStatus.value = "failed";
          schedWaiting.value = false;
          window.alert("构建失败：保持现状不动（详情见 /tmp/viewerd-build.log）。");
          return;
        }
        if (body.status === "none") {
          // Cancelled (this pane or an agent over the HTTP API).
          schedStatus.value = "none";
          schedWaiting.value = false;
          return;
        }
        if (body.status === "building" || body.status === "armed") {
          schedStatus.value = body.status;
        }
      }
    } catch {
      // status probe unreachable — the main probe above decides the outcome
    }
    await new Promise((resolve) => setTimeout(resolve, SCHEDULED_POLL_MS));
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
    await new Promise((resolve) => setTimeout(resolve, RESTART_POLL_MS));
  }
}

/**
 * Cancel a pending scheduled restart: DELETE /api/admin/schedule-restart
 * disarms the watchdog. A build already in flight finishes but can no
 * longer arm (the gateway's building→armed transition is a CAS), so
 * cancelling mid-build is safe.
 */
async function cancelScheduledRestart(): Promise<void> {
  const confirmed = window.confirm("取消已计划的重启？\n正在后台跑的构建会跑完，但不会触发重启。");
  if (!confirmed) return;
  const resp = await fetch("/api/admin/schedule-restart", { method: "DELETE" }).catch(() => null);
  if (resp === null || !resp.ok) {
    window.alert(resp === null ? "取消请求未能送达，请检查服务状态。" : `取消失败：HTTP ${resp.status}`);
    return;
  }
  schedStatus.value = "none";
}

/** The 空闲时重启 button is a toggle: arm when idle, cancel when scheduled. */
function toggleScheduledRestart(): void {
  if (schedStatus.value === "building" || schedStatus.value === "armed") {
    void cancelScheduledRestart();
    return;
  }
  void scheduleRestart();
}
</script>

<template>
  <section class="settings-pane">
    <div class="settings-layout">
      <nav class="settings-nav" aria-label="设置分类">
        <button
          v-for="section in SETTINGS_SECTIONS"
          :key="section.id"
          type="button"
          class="settings-nav-item"
          :class="{ active: activeSection === section.id }"
          :aria-current="activeSection === section.id ? 'page' : undefined"
          @click="activeSection = section.id"
        ><i class="bi" :class="section.icon"></i><span>{{ section.label }}</span></button>
      </nav>

      <div class="settings-content">
        <section v-if="activeSection === 'general'" class="settings-category" aria-labelledby="settings-general">
          <header class="settings-category-header">
            <h2 id="settings-general">通用</h2>
            <p>面板打开方式与 Dock 行为，按浏览器本地保存。</p>
          </header>
          <div class="settings-group">
            <div class="settings-field">
              <div class="setting-copy">
                <span class="setting-title">打开方式</span>
                <span class="setting-description">从 Dock 打开内容时，使用新分屏或替换当前面板。</span>
              </div>
              <span class="settings-choice settings-segmented">
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
            </div>
            <label class="settings-field">
              <span class="setting-copy">
                <span class="setting-title">Dock 悬停展开延迟</span>
                <span class="setting-description">鼠标悬停多久后展开 Dock；0 表示立即展开。</span>
              </span>
              <span class="settings-number">
                <input
                  type="number"
                  min="0"
                  step="100"
                  :value="dockSettings.hoverExpandMs"
                  @change="dockSettings.setHoverExpandMs(Number(($event.target as HTMLInputElement).value))"
                >
                <span>ms</span>
              </span>
            </label>
          </div>
        </section>

        <section v-else-if="activeSection === 'appearance'" class="settings-category" aria-labelledby="settings-appearance">
          <header class="settings-category-header">
            <h2 id="settings-appearance">外观与字号</h2>
            <p>选择主题并调整阅读字号，修改会立即生效。</p>
          </header>

          <section class="settings-subsection" aria-labelledby="settings-theme">
            <div class="settings-subsection-header">
              <h3 id="settings-theme">主题</h3>
              <details ref="themeMenu" class="settings-action-menu">
                <summary class="settings-icon-btn" title="主题管理" aria-label="主题管理"><i class="bi bi-three-dots"></i></summary>
                <div class="settings-action-menu-body">
                  <button type="button" class="settings-menu-item" @click="runThemeAction(addTheme)"><i class="bi bi-plus-lg"></i> 新建主题</button>
                  <button
                    v-if="theme.active.builtin"
                    type="button"
                    class="settings-menu-item"
                    @click="runThemeAction(() => theme.resetTheme(theme.activeId))"
                  ><i class="bi bi-arrow-counterclockwise"></i> 恢复默认</button>
                  <template v-else>
                    <button type="button" class="settings-menu-item" @click="runThemeAction(renameActive)"><i class="bi bi-pencil"></i> 重命名</button>
                    <button type="button" class="settings-menu-item" @click="runThemeAction(() => theme.clearOverrides(theme.activeId))"><i class="bi bi-eraser"></i> 清空定制</button>
                    <button type="button" class="settings-menu-item danger" @click="runThemeAction(removeActive)"><i class="bi bi-trash"></i> 删除主题</button>
                  </template>
                </div>
              </details>
            </div>
            <div class="theme-list">
              <button
                v-for="t in theme.themes"
                :key="t.id"
                type="button"
                class="theme-option"
                :class="{ active: theme.activeId === t.id }"
                :aria-pressed="theme.activeId === t.id"
                @click="theme.setActive(t.id)"
              >
                <span class="theme-swatches" aria-hidden="true">
                  <span v-for="field in BASE_FIELDS" :key="field.key" :style="{ backgroundColor: t.base[field.key] }"></span>
                </span>
                <span class="theme-option-name">{{ t.name }}</span>
                <i v-if="theme.activeId === t.id" class="bi bi-check2"></i>
              </button>
            </div>
            <p class="settings-hint">主题定义跨浏览器同步；当前选择与字号仅保存在本浏览器。</p>

            <details class="settings-advanced">
              <summary>自定义配色</summary>
              <div class="settings-advanced-body">
                <label v-for="field in BASE_FIELDS" :key="field.key" class="settings-field">
                  <span class="setting-copy"><span class="setting-title">{{ field.label }}</span></span>
                  <span class="theme-color-inputs">
                    <input
                      type="color"
                      :value="colorInputValue(theme.active.base[field.key])"
                      @input="theme.setBase(theme.activeId, field.key, ($event.target as HTMLInputElement).value)"
                    >
                    <input
                      type="text"
                      class="theme-hex"
                      :value="theme.active.base[field.key]"
                      @change="theme.setBase(theme.activeId, field.key, ($event.target as HTMLInputElement).value)"
                    >
                  </span>
                </label>
                <label class="settings-field">
                  <span class="setting-copy"><span class="setting-title">代码颜色</span></span>
                  <span class="theme-color-inputs">
                    <input type="color" :value="colorInputValue(codeColor)" @input="setCodeColor(($event.target as HTMLInputElement).value)">
                    <input type="text" class="theme-hex" :value="codeColor" @change="setCodeColor(($event.target as HTMLInputElement).value)">
                    <button
                      v-if="theme.active.overrides.codeAccent !== undefined"
                      type="button"
                      class="advanced-reset"
                      title="跟随主题色"
                      @click="resetCodeColor"
                    ><i class="bi bi-x-lg"></i></button>
                  </span>
                </label>
                <p class="settings-hint">四个基色自动派生界面颜色；代码高亮额外使用代码颜色。明暗模式随背景底色切换。</p>
              </div>
            </details>
            <ThemePreview class="settings-preview" />
          </section>

          <section class="settings-subsection typography-section" aria-labelledby="settings-typography">
            <div class="settings-subsection-header">
              <h3 id="settings-typography">字号</h3>
              <button
                v-if="typography.customized"
                type="button"
                class="settings-text-btn"
                title="恢复全部字号为默认值（UI 12px / 内容 15px）"
                @click="typography.resetAll()"
              >恢复默认</button>
            </div>
            <div class="settings-group">
              <div class="setting-block">
                <label class="settings-field">
                  <span class="setting-copy">
                    <span class="setting-title">UI 字号</span>
                    <span class="setting-description">界面、菜单与控件文字。</span>
                  </span>
                  <span class="settings-number">
                    <input
                      type="number"
                      :min="UI_SIZE_RANGE.min"
                      :max="UI_SIZE_RANGE.max"
                      step="1"
                      :value="typography.uiSize"
                      @change="setUiSize(($event.target as HTMLInputElement).value)"
                    >
                    <span>px</span>
                  </span>
                </label>
                <div class="type-sample" :style="{ fontSize: `${typography.uiSize}px` }">界面文字与按钮示例</div>
              </div>
              <div class="setting-block">
                <label class="settings-field">
                  <span class="setting-copy">
                    <span class="setting-title">内容字号</span>
                    <span class="setting-description">聊天消息与文档正文。</span>
                  </span>
                  <span class="settings-number">
                    <input
                      type="number"
                      :min="CONTENT_SIZE_RANGE.min"
                      :max="CONTENT_SIZE_RANGE.max"
                      step="1"
                      :value="typography.contentSize"
                      @change="setContentSize(($event.target as HTMLInputElement).value)"
                    >
                    <span>px</span>
                  </span>
                </label>
                <div class="type-sample" :style="{ fontSize: `${typography.contentSize}px` }">内容正文与阅读示例</div>
              </div>
            </div>
            <details class="settings-advanced">
              <summary>细节字号<span v-if="typography.detailCount > 0" class="advanced-count">（{{ typography.detailCount }} 项已定制）</span></summary>
              <div class="settings-advanced-body">
                <p class="settings-hint">默认按比例跟随基础字号；点「定制」固定为像素值，× 恢复自动。</p>
                <div v-for="field in TYPO_DETAIL_FIELDS" :key="field.key" class="settings-field">
                  <span class="setting-copy"><span class="setting-title">{{ field.label }}</span></span>
                  <span v-if="typography.details[field.key] !== undefined" class="theme-color-inputs">
                    <span class="settings-number">
                      <input
                        type="number"
                        :min="DETAIL_SIZE_RANGE.min"
                        :max="DETAIL_SIZE_RANGE.max"
                        step="0.5"
                        :value="typography.details[field.key]"
                        @change="setDetailSize(field.key, ($event.target as HTMLInputElement).value)"
                      >
                      <span>px</span>
                    </span>
                    <button
                      type="button"
                      class="advanced-reset"
                      title="恢复自动（跟随基础字号按比例计算）"
                      @click="typography.setDetail(field.key, null)"
                    ><i class="bi bi-x-lg"></i></button>
                  </span>
                  <span v-else class="settings-choice">
                    <span class="advanced-auto">自动（{{ typography.detailSize(field.key) }}px）</span>
                    <button type="button" class="settings-text-btn" @click="typography.pinDetail(field.key)">定制</button>
                  </span>
                </div>
              </div>
            </details>
          </section>
        </section>

        <section v-else-if="activeSection === 'chat'" class="settings-category" aria-labelledby="settings-chat">
          <header class="settings-category-header">
            <h2 id="settings-chat">聊天与通知</h2>
            <p>阅读体验与轮次完成提醒，按浏览器本地保存。</p>
          </header>
          <div class="settings-group">
            <div class="settings-field">
              <div class="setting-copy">
                <span class="setting-title">阅读留白</span>
                <span class="setting-description">在消息末尾保留一屏空白，便于把最近内容滚动到舒适位置。</span>
              </div>
              <button
                type="button"
                class="settings-switch"
                role="switch"
                :aria-checked="chatSettings.virtualSpace"
                aria-label="阅读留白"
                :title="chatSettings.virtualSpace ? '已开启，点击关闭' : '已关闭，点击开启'"
                @click="chatSettings.toggleVirtualSpace()"
              ><span></span></button>
            </div>
            <div class="settings-field">
              <div class="setting-copy">
                <span class="setting-title">轮次完成系统通知</span>
                <span class="setting-description">仅在页面不可见时推送；点击通知切回并打开对应聊天。</span>
                <span v-if="notificationHint" class="setting-warning">{{ notificationHint }}</span>
              </div>
              <button
                type="button"
                class="settings-switch"
                role="switch"
                :aria-checked="chatSettings.turnNotifications"
                aria-label="轮次完成系统通知"
                :title="chatSettings.turnNotifications ? '已开启，点击关闭' : '已关闭，点击开启（首次会请求浏览器通知权限）'"
                @click="toggleTurnNotifications"
              ><span></span></button>
            </div>
          </div>
        </section>

        <section v-else-if="activeSection === 'plugins'" class="settings-category" aria-labelledby="settings-plugins">
          <header class="settings-category-header">
            <h2 id="settings-plugins">插件设置</h2>
            <p>插件专属配置统一放在插件管理中。</p>
          </header>
          <div class="settings-group">
            <div class="settings-field">
              <div class="setting-copy">
                <span class="setting-title">管理插件与配置</span>
                <span class="setting-description">配置聊天、语言模型、语音控制及外部插件，并管理外部插件运行状态。</span>
              </div>
              <button type="button" class="settings-choice-btn settings-action-btn" @click="openPluginManager"><i class="bi bi-puzzle"></i> 打开插件管理</button>
            </div>
          </div>
        </section>

        <section v-else class="settings-category" aria-labelledby="settings-maintenance">
          <header class="settings-category-header">
            <h2 id="settings-maintenance">系统维护</h2>
            <p>重启或更新 viewerd 服务，操作前会再次确认。</p>
          </header>
          <div class="settings-group">
            <div class="settings-field">
              <div class="setting-copy">
                <span class="setting-title">重启后端</span>
                <span class="setting-description">排空在途任务后重启服务，完成后页面自动刷新。</span>
              </div>
              <button
                type="button"
                class="settings-choice-btn settings-action-btn"
                :disabled="restarting || building"
                @click="restartBackend"
              ><i class="bi" :class="restarting ? 'bi-arrow-repeat' : 'bi-arrow-clockwise'"></i> {{ restarting ? "重启中…" : "重启后端" }}</button>
            </div>
            <div class="settings-field">
              <div class="setting-copy">
                <span class="setting-title">构建并重启</span>
                <span class="setting-description">后台构建成功后自动重启；构建失败时保持当前服务。</span>
              </div>
              <button
                type="button"
                class="settings-choice-btn settings-action-btn"
                :disabled="building || restarting"
                @click="buildAndRestart"
              ><i class="bi" :class="building ? 'bi-hourglass-split' : 'bi-hammer'"></i> {{ building ? "构建中…" : "构建并重启" }}</button>
            </div>
            <div class="settings-field">
              <div class="setting-copy">
                <span class="setting-title">空闲时构建并重启</span>
                <span class="setting-description">构建完成后，等 Agent 任务与录音结束再自动重启。</span>
                <span class="setting-status" :class="{ failed: schedStatus === 'failed' }">{{ schedStatusText }}</span>
              </div>
              <button
                type="button"
                class="settings-choice-btn settings-action-btn"
                :class="{ active: schedStatus === 'building' || schedStatus === 'armed' }"
                :disabled="restarting || building"
                @click="toggleScheduledRestart"
              ><i class="bi" :class="schedStatus === 'building' || schedStatus === 'armed' ? 'bi-clock-history' : 'bi-clock'"></i> {{ schedStatus === "building" || schedStatus === "armed" ? "取消计划重启" : "空闲时重启" }}</button>
            </div>
          </div>
        </section>
      </div>
    </div>
  </section>
</template>

<style scoped>
.settings-pane {
  container-type: inline-size;
  height: 100%;
  overflow-y: auto;
  width: 100%;
}

.settings-layout {
  align-items: start;
  display: grid;
  gap: 32px;
  grid-template-columns: 148px minmax(0, 720px);
  justify-content: center;
  margin: 0 auto;
  max-width: 1040px;
  padding: 20px 24px 36px;
  width: 100%;
}

.settings-nav {
  display: flex;
  flex-direction: column;
  gap: 2px;
  position: sticky;
  top: 0;
}

.settings-nav-item {
  align-items: center;
  background: transparent;
  border: 0;
  border-left: 2px solid transparent;
  border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
  color: var(--color-text-muted);
  display: flex;
  font-size: var(--font-size-ui);
  gap: 8px;
  padding: 7px 10px;
  text-align: left;
  white-space: nowrap;
}

.settings-nav-item:hover {
  background: var(--color-surface-hover);
  color: var(--color-text);
}

.settings-nav-item.active {
  background: var(--color-surface-selected);
  border-left-color: var(--color-accent);
  color: var(--color-text);
  font-weight: 600;
}

.settings-content,
.settings-category {
  min-width: 0;
  width: 100%;
}

.settings-category-header {
  margin-bottom: 18px;
}

.settings-category-header h2,
.settings-subsection-header h3 {
  color: var(--color-text);
  margin: 0;
}

.settings-category-header h2 {
  font-size: calc(var(--font-size-ui) * 4 / 3);
  font-weight: 650;
}

.settings-category-header p {
  color: var(--color-text-muted);
  font-size: var(--font-size-ui-small);
  margin: 5px 0 0;
}

.settings-group {
  border-top: 1px solid var(--color-border);
}

.settings-field {
  align-items: center;
  border-bottom: 1px solid var(--color-border);
  display: grid;
  font-size: var(--font-size-ui);
  gap: 20px;
  grid-template-columns: minmax(0, 1fr) auto;
  margin: 0;
  padding: 14px 0;
}

.setting-copy {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}

.setting-title {
  color: var(--color-text);
}

.setting-description,
.setting-status,
.setting-warning {
  color: var(--color-text-muted);
  font-size: var(--font-size-ui-small);
  line-height: 1.5;
}

.setting-warning,
.setting-status.failed {
  color: var(--color-danger);
}

.settings-choice {
  align-items: center;
  display: inline-flex;
  gap: 8px;
}

.settings-choice-btn,
.settings-text-btn,
.settings-icon-btn {
  align-items: center;
  background: transparent;
  color: var(--color-text-muted);
  display: inline-flex;
  font-size: var(--font-size-ui);
  gap: 5px;
}

.settings-choice-btn {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  padding: 4px 10px;
  white-space: nowrap;
}

.settings-choice-btn:hover:not(:disabled),
.settings-text-btn:hover,
.settings-icon-btn:hover {
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

.settings-segmented {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  gap: 0;
  overflow: hidden;
}

.settings-segmented .settings-choice-btn {
  border: 0;
  border-radius: 0;
}

.settings-segmented .settings-choice-btn + .settings-choice-btn {
  border-left: 1px solid var(--color-border);
}

.settings-action-btn {
  justify-content: center;
  min-width: 118px;
}

.settings-text-btn {
  border: 0;
  border-radius: var(--radius-sm);
  padding: 2px 5px;
}

.settings-switch {
  background: var(--color-surface-muted);
  border: 1px solid var(--color-border-strong);
  border-radius: 12px;
  height: 22px;
  padding: 2px;
  transition: background 120ms ease;
  width: 38px;
}

.settings-switch span {
  background: var(--color-text-muted);
  border-radius: 50%;
  display: block;
  height: 16px;
  transform: translateX(0);
  transition: transform 120ms ease;
  width: 16px;
}

.settings-switch[aria-checked="true"] {
  background: var(--color-accent);
  border-color: var(--color-accent);
}

.settings-switch[aria-checked="true"] span {
  background: var(--color-text-inverse);
  transform: translateX(16px);
}

.settings-number {
  align-items: center;
  color: var(--color-text-muted);
  display: inline-flex;
  font-size: var(--font-size-ui-small);
  gap: 6px;
}

.settings-number input[type="number"] {
  width: 76px;
}

.settings-subsection-header {
  align-items: center;
  display: flex;
  justify-content: space-between;
  margin-bottom: 8px;
  min-height: 28px;
}

.settings-subsection-header h3 {
  font-size: var(--font-size-ui);
  font-weight: 700;
}

.typography-section {
  border-top: 1px solid var(--color-border);
  margin-top: 24px;
  padding-top: 18px;
}

.settings-icon-btn {
  border: 0;
  border-radius: var(--radius-sm);
  cursor: pointer;
  justify-content: center;
  list-style: none;
  padding: 4px 7px;
}

.settings-icon-btn::-webkit-details-marker {
  display: none;
}

.settings-action-menu {
  position: relative;
}

.settings-action-menu-body {
  background: var(--color-surface);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  display: flex;
  flex-direction: column;
  min-width: 150px;
  padding: 4px;
  position: absolute;
  right: 0;
  top: calc(100% + 4px);
  z-index: 20;
}

.settings-menu-item {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: var(--radius-sm);
  color: var(--color-text);
  display: flex;
  font-size: var(--font-size-ui);
  gap: 7px;
  padding: 6px 8px;
  text-align: left;
  white-space: nowrap;
}

.settings-menu-item:hover {
  background: var(--color-surface-hover);
}

.settings-menu-item.danger {
  color: var(--color-danger);
}

.theme-list {
  border-top: 1px solid var(--color-border);
}

.theme-option {
  align-items: center;
  background: transparent;
  border: 0;
  border-bottom: 1px solid var(--color-border);
  color: var(--color-text);
  display: flex;
  font-size: var(--font-size-ui);
  gap: 12px;
  padding: 10px 8px;
  text-align: left;
  width: 100%;
}

.theme-option:hover {
  background: var(--color-surface-hover);
}

.theme-option.active {
  background: var(--color-surface-selected);
}

.theme-option-name {
  flex: 1;
}

.theme-option > i {
  color: var(--color-accent);
}

.theme-swatches {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  display: inline-flex;
  overflow: hidden;
}

.theme-swatches > span {
  height: 18px;
  width: 18px;
}

.settings-advanced {
  border-bottom: 1px solid var(--color-border);
  margin-top: 12px;
}

.settings-advanced > summary {
  color: var(--color-text-muted);
  cursor: pointer;
  font-size: var(--font-size-ui);
  padding: 4px 0 12px;
  user-select: none;
}

.settings-advanced > summary:hover {
  color: var(--color-text);
}

.settings-advanced-body > .settings-field:first-of-type {
  border-top: 1px solid var(--color-border);
}

.settings-advanced-body > .settings-hint:last-child {
  padding: 10px 0;
}

.advanced-count,
.advanced-auto {
  color: var(--color-text-subtle);
  font-size: var(--font-size-ui-small);
  font-weight: 400;
}

.advanced-reset {
  background: transparent;
  border: none;
  border-radius: var(--radius-sm);
  color: var(--color-text-subtle);
  padding: 3px 5px;
}

.advanced-reset:hover {
  background: var(--color-surface-hover);
  color: var(--color-danger);
}

.theme-color-inputs {
  align-items: center;
  display: inline-flex;
  gap: 6px;
}

.theme-hex {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  width: 130px;
}

.settings-field input[type="text"] {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  color: var(--color-text);
  font-size: var(--font-size-ui);
  padding: 2px 6px;
}

.settings-field input[type="color"] {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  height: 24px;
  padding: 1px;
  width: 42px;
}

.settings-hint {
  color: var(--color-text-muted);
  font-size: var(--font-size-ui-small);
  line-height: 1.5;
  margin: 10px 0 0;
}

.settings-preview {
  margin-top: 16px;
}

.type-sample {
  border-bottom: 1px solid var(--color-border);
  color: var(--color-text-muted);
  line-height: 1.5;
  padding: 9px 0 12px;
}

@container (max-width: 720px) {
  .settings-layout {
    gap: 16px;
    grid-template-columns: minmax(0, 1fr);
  }

  .settings-nav {
    border-bottom: 1px solid var(--color-border);
    flex-direction: row;
    gap: 2px;
    overflow-x: auto;
    padding-bottom: 6px;
    position: static;
  }

  .settings-nav-item {
    border-bottom: 2px solid transparent;
    border-left: 0;
    border-radius: var(--radius-sm) var(--radius-sm) 0 0;
  }

  .settings-nav-item.active {
    border-bottom-color: var(--color-accent);
  }
}

@container (max-width: 520px) {
  .settings-layout {
    padding: 12px 14px 28px;
  }

  .settings-field {
    align-items: start;
    gap: 10px;
    grid-template-columns: minmax(0, 1fr);
  }

  .settings-choice {
    flex-wrap: wrap;
  }

  .theme-color-inputs {
    width: 100%;
  }

  .theme-hex {
    flex: 1;
    width: auto;
  }
}
</style>
