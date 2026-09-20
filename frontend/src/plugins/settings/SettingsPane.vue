<script setup lang="ts">
/**
 * Settings pane: the unified, full-page configuration surface (opened from
 * the Dock gear). All toggles are browser-local (localStorage) and take
 * effect immediately; the backend section drives the gateway admin API.
 * Sections: 布局 (open mode), 聊天 (virtual space), Dock (hover expand
 * delay), 外观 (theme list + 4 base colors + advanced overrides,
 * `stores/theme.ts`), 后端 (restart / build-restart / scheduled
 * restart toggle with live status). The chat
 * dispatch/summary LLM lives in 聊天管理 → 模型 (chat-manager LLMPanel).
 */
import { computed, inject, onBeforeUnmount, onMounted, ref } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import { useChatSettingsStore } from "../../stores/chatSettings";
import { useDockSettingsStore } from "../../stores/dockSettings";
import { useLayoutStore } from "../../stores/layout";
import { useThemeStore } from "../../stores/theme";
import type { BaseVars, OverrideKind } from "../../stores/theme";
import { OVERRIDE_VARS } from "../../stores/theme";
import ThemePreview from "./ThemePreview.vue";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("SettingsPane requires PluginPaneHost");
const ctx: PluginCtx = injectedCtx;

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

interface AdvancedField {
  key: string;
  label: string;
  kind: OverrideKind;
  min?: number;
  max?: number;
  step?: number;
}

const ADVANCED_GROUPS: Array<{ title: string; fields: AdvancedField[] }> = [
  {
    title: "界面",
    fields: [
      { key: "surfaceRaised", label: "浮起面底色", kind: "color" },
      { key: "surfaceMuted", label: "柔和面底色", kind: "color" },
      { key: "surfaceHover", label: "悬停底色", kind: "color" },
      { key: "surfaceSelected", label: "选中底色", kind: "color" },
      { key: "titlebar", label: "抬头底色", kind: "color" },
      { key: "titlebarText", label: "抬头文字", kind: "color" },
      { key: "textMuted", label: "次要文字", kind: "color" },
      { key: "textSubtle", label: "弱化文字", kind: "color" },
      { key: "textInverse", label: "反色文字", kind: "color" },
      { key: "border", label: "边框", kind: "color" },
      { key: "borderStrong", label: "强调边框", kind: "color" },
      { key: "accentHover", label: "主题色（悬停）", kind: "color" },
      { key: "accentSoft", label: "主题色（浅底）", kind: "color" },
      { key: "focus", label: "焦点色", kind: "color" },
      { key: "overlay", label: "遮罩层", kind: "color" },
    ],
  },
  {
    title: "语义色",
    fields: [
      { key: "success", label: "成功色", kind: "color" },
      { key: "warning", label: "警告色", kind: "color" },
      { key: "danger", label: "危险色", kind: "color" },
      { key: "info", label: "信息色", kind: "color" },
    ],
  },
  {
    title: "消息",
    fields: [
      { key: "bodyFontSize", label: "正文字号", kind: "px", min: 10, max: 24, step: 1 },
      { key: "bodyLineHeight", label: "正文行高", kind: "number", min: 1.1, max: 2.4, step: 0.05 },
      { key: "codeFontSize", label: "代码字号", kind: "px", min: 9, max: 20, step: 1 },
      { key: "markdownBody", label: "正文颜色", kind: "color" },
      { key: "markdownStrong", label: "加粗颜色", kind: "color" },
      { key: "markdownLink", label: "链接颜色", kind: "color" },
      { key: "markdownCodeColor", label: "行内代码文字", kind: "color" },
      { key: "markdownCodeBackground", label: "行内代码底色", kind: "color" },
      { key: "syntaxText", label: "代码块文字", kind: "color" },
      { key: "syntaxBackground", label: "代码块底色", kind: "color" },
      { key: "markdownBorder", label: "消息边框", kind: "color" },
    ],
  },
];

/** Seed values for the numeric overrides (mirror the styles.css constants). */
const NUMBER_SEEDS: Record<string, string> = { bodyFontSize: "15", bodyLineHeight: "1.65", codeFontSize: "13" };

const overrideCount = computed(() => Object.keys(theme.active.overrides).length);

function isOverridden(key: string): boolean {
  return theme.active.overrides[key] !== undefined;
}

const HEX_COLOR_RE = /^#[0-9a-fA-F]{6}$/;

/** <input type="color"> only accepts #rrggbb; fall back for rgb()/named values. */
function colorInputValue(value: string | undefined): string {
  return value !== undefined && HEX_COLOR_RE.test(value) ? value : "#888888";
}

/** Resolve a derived CSS var to a concrete #rrggbb by probing a real
 *  element: color-mix() defaults only evaluate in a used property. */
function resolveCssColor(cssVar: string): string {
  const shell = document.querySelector(".app-shell");
  if (!shell) return "#888888";
  const probe = document.createElement("span");
  probe.style.color = `var(${cssVar})`;
  probe.style.display = "none";
  shell.appendChild(probe);
  const computedColor = getComputedStyle(probe).color;
  probe.remove();
  const match = /rgba?\(\s*(\d+)[,\s]+(\d+)[,\s]+(\d+)/.exec(computedColor);
  if (!match) return "#888888";
  const hex = (n: string | undefined): string => Number(n ?? 0).toString(16).padStart(2, "0");
  return `#${hex(match[1])}${hex(match[2])}${hex(match[3])}`;
}

/** Begin customizing an advanced field: seed it with the currently
 *  computed default so the palette doesn't jump on the first edit. */
function startOverride(field: AdvancedField): void {
  const seed = field.kind === "color" ? resolveCssColor(OVERRIDE_VARS[field.key].cssVar) : NUMBER_SEEDS[field.key] ?? "15";
  theme.setOverride(theme.activeId, field.key, seed);
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
      <label class="settings-field">
        <span>轮次完成系统通知（仅当页面不可见时推送；点击通知切回并打开对应聊天）</span>
        <button
          type="button"
          class="settings-choice-btn"
          :class="{ active: chatSettings.turnNotifications }"
          :title="chatSettings.turnNotifications ? '已开启，点击关闭' : '已关闭，点击开启（首次会请求浏览器通知权限）'"
          @click="toggleTurnNotifications"
        ><i class="bi bi-bell"></i> {{ chatSettings.turnNotifications ? "开启" : "关闭" }}</button>
      </label>
      <div v-if="notificationHint" class="settings-hint">{{ notificationHint }}</div>
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
        <span><i class="bi bi-palette2"></i> 外观</span>
        <span class="settings-choice">
          <button
            v-if="theme.active.builtin"
            type="button"
            class="btn btn-sm btn-outline-secondary"
            title="恢复该内置主题的默认名称与配色"
            @click="theme.resetTheme(theme.activeId)"
          >恢复默认</button>
          <button
            v-else
            type="button"
            class="btn btn-sm btn-outline-secondary"
            title="清空该主题的全部高级定制（保留基色）"
            @click="theme.clearOverrides(theme.activeId)"
          >清空定制</button>
          <button
            type="button"
            class="btn btn-sm btn-outline-secondary"
            title="以当前主题为模板新建一个自定义主题"
            @click="addTheme"
          ><i class="bi bi-plus-lg"></i> 新建主题</button>
        </span>
      </div>
      <label class="settings-field">
        <span>主题</span>
        <span class="settings-choice">
          <select
            class="form-select form-select-sm theme-select"
            :value="theme.activeId"
            @change="theme.setActive(($event.target as HTMLSelectElement).value)"
          >
            <option v-for="t in theme.themes" :key="t.id" :value="t.id">{{ t.name }}</option>
          </select>
          <button
            v-if="!theme.active.builtin"
            type="button"
            class="settings-choice-btn"
            title="重命名该主题"
            @click="renameActive"
          ><i class="bi bi-pencil"></i></button>
          <button
            v-if="!theme.active.builtin"
            type="button"
            class="settings-choice-btn"
            title="删除该主题"
            @click="removeActive"
          ><i class="bi bi-trash"></i></button>
        </span>
      </label>
      <label v-for="field in BASE_FIELDS" :key="field.key" class="settings-field">
        <span>{{ field.label }}</span>
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
      <div class="settings-hint">其余颜色（悬停、边框、消息、代码块等）由这四个基色自动计算；明暗模式随背景底色自动切换。</div>
      <ThemePreview />
      <details class="settings-advanced">
        <summary>
          高级定制<span v-if="overrideCount > 0" class="advanced-count">（{{ overrideCount }} 项已定制）</span>
        </summary>
        <div class="settings-hint">以下各项默认跟随基色自动计算；点「定制」单独修改，× 恢复自动。</div>
        <div v-for="group in ADVANCED_GROUPS" :key="group.title" class="advanced-group">
          <div class="advanced-group-title">{{ group.title }}</div>
          <div v-for="field in group.fields" :key="field.key" class="settings-field">
            <span>{{ field.label }}</span>
            <span v-if="isOverridden(field.key)" class="theme-color-inputs">
              <template v-if="field.kind === 'color'">
                <input
                  type="color"
                  :value="colorInputValue(theme.active.overrides[field.key])"
                  @input="theme.setOverride(theme.activeId, field.key, ($event.target as HTMLInputElement).value)"
                >
                <input
                  type="text"
                  class="theme-hex"
                  :value="theme.active.overrides[field.key]"
                  @change="theme.setOverride(theme.activeId, field.key, ($event.target as HTMLInputElement).value)"
                >
              </template>
              <input
                v-else
                type="number"
                :min="field.min"
                :max="field.max"
                :step="field.step"
                :value="theme.active.overrides[field.key]"
                @change="theme.setOverride(theme.activeId, field.key, ($event.target as HTMLInputElement).value)"
              >
              <button
                type="button"
                class="advanced-reset"
                title="恢复自动（跟随基色计算）"
                @click="theme.setOverride(theme.activeId, field.key, undefined)"
              ><i class="bi bi-x-lg"></i></button>
            </span>
            <span v-else class="settings-choice">
              <span class="advanced-auto">自动</span>
              <button type="button" class="settings-choice-btn" @click="startOverride(field)">定制</button>
            </span>
          </div>
        </div>
      </details>
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
          <button
            type="button"
            class="settings-choice-btn"
            :class="{ active: schedStatus === 'building' || schedStatus === 'armed' }"
            :disabled="restarting || building"
            :title="schedStatus === 'building' || schedStatus === 'armed'
              ? '已计划重启，点击取消（进行中的构建会跑完但不触发重启）'
              : '空闲时构建并重启：后台构建（约 1-2 分钟，期间服务不中断）成功后，等所有 agent 任务跑完、没有录音时自动重启；构建失败则保持现状'"
            @click="toggleScheduledRestart"
          ><i class="bi" :class="schedStatus === 'building' || schedStatus === 'armed' ? 'bi-clock-history' : 'bi-clock'"></i> {{ schedStatus === "building" || schedStatus === "armed" ? "取消计划重启" : "空闲时重启" }}</button>
        </span>
      </div>
      <div class="settings-hint">
        计划重启状态：{{ schedStatusText }}。agent 可通过 HTTP API 控制：POST /api/admin/schedule-restart 计划、GET 查询、DELETE 取消。
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

.settings-field input[type="text"],
.settings-field input[type="password"] {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  color: var(--color-text);
  font-size: var(--font-size-ui);
  padding: 2px 6px;
  width: 170px;
}

.theme-select {
  font-size: var(--font-size-ui);
  width: 170px;
}

.settings-advanced {
  border-top: 1px solid var(--color-border);
  padding-top: 6px;
}

.settings-advanced > summary {
  color: var(--color-text-muted);
  cursor: pointer;
  font-size: var(--font-size-ui);
  font-weight: 700;
  user-select: none;
}

.settings-advanced > summary:hover {
  color: var(--color-text);
}

.settings-advanced[open] {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.advanced-count {
  color: var(--color-text-subtle);
  font-weight: 400;
}

.advanced-group {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.advanced-group-title {
  color: var(--color-text-subtle);
  font-size: var(--font-size-ui-small);
  font-weight: 700;
  margin-top: 4px;
}

.advanced-auto {
  color: var(--color-text-subtle);
  font-size: var(--font-size-ui-small);
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

/* Phone layout: stack field label above its control, let inputs and button
   rows take the full width instead of overflowing a narrow screen. */
@media (max-width: 560px) {
  .settings-pane {
    padding: 12px 14px;
  }

  .settings-group {
    max-width: none;
  }

  .settings-field {
    gap: 4px;
    grid-template-columns: 1fr;
  }

  .settings-field input[type="number"],
  .settings-field input[type="text"],
  .settings-field input[type="password"] {
    width: 100%;
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
