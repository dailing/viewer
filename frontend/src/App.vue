<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import ConfigPanel from "./components/ConfigPanel.vue";
import SuperWorkspacePage from "./components/SuperWorkspacePage.vue";
import { connectEvents } from "./api/events";
import { DARK_MARKDOWN_THEME, DEFAULT_MARKDOWN_THEME, useFilesStore } from "./stores/files";
import { useInputSessionsStore } from "./stores/inputSessions";
import { useLayoutStore } from "./stores/layout";
import { usePaneToolbarStore } from "./stores/paneToolbar";
import { useTerminalsStore } from "./stores/terminals";
import { useUsersStore } from "./stores/users";
import type { PaneToolbarAction, PaneToolbarControl } from "./stores/paneToolbar";
import type { SplitDirection } from "./types/layout";

const files = useFilesStore();
const inputSessions = useInputSessionsStore();
const layout = useLayoutStore();
const paneToolbar = usePaneToolbarStore();
const terminals = useTerminalsStore();
const users = useUsersStore();

const appReady = ref(false);
const selectingUser = ref(false);
const activePage = ref<"super" | "settings">("super");
const mobileToolbarOpen = ref(false);

const appStyle = computed(() => {
  const navbarSize = files.appearance.navbar_size;
  const buttonSize = Math.max(18, navbarSize - 4);
  const iconSize = Math.max(11, Math.round(navbarSize * 0.48));
  const theme = files.appearance.color_theme === "dark" && files.markdown.active_theme === DEFAULT_MARKDOWN_THEME.name ? DARK_MARKDOWN_THEME : files.activeMarkdownTheme;
  return {
    "--topbar-height": `${navbarSize}px`,
    "--nav-button-size": `${buttonSize}px`,
    "--nav-icon-size": `${iconSize}px`,
    "--markdown-body-font-size": `${theme.body.font_size ?? 15}px`,
    "--markdown-body-color": theme.body.color ?? "#172033",
    "--markdown-body-line-height": String(theme.body.line_height ?? 1.65),
    "--markdown-h1-font-size": `${theme.h1.font_size ?? 28}px`,
    "--markdown-h1-color": theme.h1.color ?? "#172033",
    "--markdown-h1-font-weight": theme.h1.font_weight ?? "700",
    "--markdown-h1-line-height": String(theme.h1.line_height ?? 1.2),
    "--markdown-h2-font-size": `${theme.h2.font_size ?? 23}px`,
    "--markdown-h2-color": theme.h2.color ?? "#172033",
    "--markdown-h2-font-weight": theme.h2.font_weight ?? "700",
    "--markdown-h2-line-height": String(theme.h2.line_height ?? 1.25),
    "--markdown-h3-font-size": `${theme.h3.font_size ?? 19}px`,
    "--markdown-h3-color": theme.h3.color ?? "#172033",
    "--markdown-h3-font-weight": theme.h3.font_weight ?? "700",
    "--markdown-h3-line-height": String(theme.h3.line_height ?? 1.3),
    "--markdown-h4-font-size": `${theme.h4.font_size ?? 16}px`,
    "--markdown-h4-color": theme.h4.color ?? "#172033",
    "--markdown-h4-font-weight": theme.h4.font_weight ?? "700",
    "--markdown-h4-line-height": String(theme.h4.line_height ?? 1.35),
    "--markdown-paragraph-font-size": `${theme.paragraph.font_size ?? 15}px`,
    "--markdown-paragraph-color": theme.paragraph.color ?? "#172033",
    "--markdown-paragraph-line-height": String(theme.paragraph.line_height ?? 1.65),
    "--markdown-code-font-size": `${theme.code.font_size ?? 13}px`,
    "--markdown-code-color": theme.code.color ?? "#24292f",
    "--markdown-code-background": theme.code_background,
    "--markdown-link-color": theme.link_color,
    "--markdown-border-color": theme.border_color,
    "--syntax-background": theme.syntax.background,
    "--syntax-text": theme.syntax.text,
    "--syntax-keyword": theme.syntax.keyword,
    "--syntax-string": theme.syntax.string,
    "--syntax-number": theme.syntax.number,
    "--syntax-title": theme.syntax.title,
    "--syntax-comment": theme.syntax.comment,
    "--syntax-meta": theme.syntax.meta,
  };
});

const activePaneToolbar = computed(() => (layout.activePaneId ? paneToolbar.forPane(layout.activePaneId) : undefined));
const activePaneTitle = computed(() => {
  if (activePaneToolbar.value?.title) return activePaneToolbar.value.title;
  const pane = layout.activePane;
  if (!pane || pane.type !== "pane") return "Empty pane";
  if (pane.chatId) return "Chat";
  if (pane.terminalId) return "Terminal";
  if (pane.diffPath) return `Diff: ${pane.diffPath}`;
  return pane.filePath || "Empty pane";
});
const activePaneHasContent = computed(() => {
  const pane = layout.activePane;
  return Boolean(pane?.type === "pane" && (pane.filePath || pane.terminalId || pane.diffPath || pane.chatId));
});
const paneToolbarVisible = computed(() => activePage.value === "super");
const activePageTitle = computed(() => {
  if (activePage.value === "settings") return "Settings";
  return activePaneTitle.value;
});
const globalPaneActions = computed<PaneToolbarAction[]>(() => {
  if (!layout.activePaneId || activePage.value !== "super") return [];
  return [
    {
      id: "refresh-pane",
      title: "Refresh pane",
      icon: "bi-arrow-clockwise",
      run: () => refreshActivePane(),
    },
    ...(layout.activePaneCanGoBack
      ? [
          {
            id: "go-back",
            title: "Go back",
            icon: "bi-arrow-left",
            run: () => goBackActivePane(),
          },
        ]
      : []),
    {
      id: "split-vertical",
      title: "Split pane right",
      icon: "bi-layout-split",
      run: () => splitActivePane("vertical"),
    },
    {
      id: "split-horizontal",
      title: "Split pane down",
      icon: "bi-view-stacked",
      run: () => splitActivePane("horizontal"),
    },
    {
      id: "close-pane",
      title: activePaneHasContent.value ? "Clear pane content" : "Close empty pane",
      icon: "bi-x-lg",
      run: () => closeActivePane(),
    },
  ];
});
const activePaneActions = computed(() => activePaneToolbar.value?.actions ?? []);
const activePaneControls = computed(() => activePaneToolbar.value?.controls ?? []);
const hasMobilePaneToolbar = computed(() => activePaneActions.value.length > 0 || activePaneControls.value.length > 0);
const globalInputStatus = computed(() => inputSessions.globalStatus);

let source: EventSource | null = null;
let terminalRefresh: number | null = null;

onMounted(async () => {
  await users.load();
  if (users.needsSelection) {
    selectingUser.value = true;
    return;
  }
  await initializeApp();
});

async function initializeApp() {
  appReady.value = false;
  files.currentPath = "";
  await files.loadConfig();
  await Promise.all([files.loadDirectory(""), terminals.load()]);
  source = connectEvents(async (event) => {
    await files.refreshAffected(event.path, event.is_dir);
    window.dispatchEvent(new CustomEvent("viewer:file-changed", { detail: event }));
  });
  terminalRefresh = window.setInterval(() => {
    void terminals.load();
  }, 15000);
  appReady.value = true;
}

async function selectUserProfile(userId: string) {
  users.select(userId);
  selectingUser.value = false;
  await initializeApp();
}

function runPaneAction(action: PaneToolbarAction) {
  void action.run();
}

function runMobilePaneAction(action: PaneToolbarAction) {
  mobileToolbarOpen.value = false;
  runPaneAction(action);
}

function updatePaneControl(control: PaneToolbarControl, event: Event) {
  if (control.kind !== "select") return;
  const target = event.target as HTMLSelectElement | null;
  if (!target) return;
  void control.onChange(target.value);
}

function updateMobilePaneControl(control: PaneToolbarControl, event: Event) {
  updatePaneControl(control, event);
  mobileToolbarOpen.value = false;
}

function splitActivePane(direction: SplitDirection) {
  if (!layout.activePaneId) return;
  layout.splitPane(layout.activePaneId, direction);
}

function goBackActivePane() {
  layout.goBack();
}

function closeActivePane() {
  const paneId = layout.activePaneId;
  if (!paneId) return;
  if (activePaneHasContent.value) {
    layout.clearPane(paneId);
    return;
  }
  layout.closePane(paneId);
}

function refreshActivePane() {
  const paneId = layout.activePaneId;
  if (!paneId) return;
  window.dispatchEvent(new CustomEvent("viewer:pane-refresh", { detail: { paneId } }));
}

function runGlobalInputSend() {
  void inputSessions.requestGlobalSend();
}

onUnmounted(() => {
  source?.close();
  if (terminalRefresh !== null) window.clearInterval(terminalRefresh);
});
</script>

<template>
  <div v-if="selectingUser" class="user-select-page">
    <section class="user-select-panel" aria-label="Select user profile">
      <h1>Select Profile</h1>
      <button
        v-for="profile in users.profiles"
        :key="profile.id"
        class="user-profile-button"
        type="button"
        @click="selectUserProfile(profile.id)"
      >
        <span>{{ profile.name || profile.id }}</span>
        <small>{{ profile.home || "/" }}</small>
      </button>
    </section>
  </div>
  <div v-else-if="appReady" class="app-shell" :data-theme="files.appearance.color_theme" :style="appStyle">
    <header class="topbar">
      <button
        class="btn btn-outline-secondary icon-button"
        :class="{ active: activePage === 'settings' }"
        type="button"
        title="Settings"
        @click="activePage = activePage === 'settings' ? 'super' : 'settings'"
      >
        <i class="bi bi-gear"></i>
      </button>
      <div class="active-pane-title" :title="activePageTitle">
        {{ activePageTitle }}
      </div>
      <span v-if="paneToolbarVisible && activePaneToolbar?.status" class="pane-status" :class="activePaneToolbar.statusClass">
        {{ activePaneToolbar.status }}
      </span>
      <div v-if="globalInputStatus.visible" class="global-input-status" :class="`status-${globalInputStatus.status}`">
        <i class="bi" :class="globalInputStatus.busy ? 'bi-mic-fill' : globalInputStatus.status === 'failed' ? 'bi-exclamation-triangle-fill' : 'bi-check-circle-fill'"></i>
        <span class="global-input-status-text" :title="`${globalInputStatus.label}: ${globalInputStatus.detail}`">
          {{ globalInputStatus.detail || globalInputStatus.label }}
        </span>
        <button
          class="btn btn-sm btn-primary global-input-send"
          type="button"
          :disabled="!globalInputStatus.canSend"
          title="Finish voice input"
          aria-label="Finish voice input"
          @click="runGlobalInputSend"
        >
          <i class="bi bi-send"></i>
          <span>Send</span>
        </button>
      </div>
      <div v-if="paneToolbarVisible && hasMobilePaneToolbar" class="mobile-pane-menu">
        <button
          class="btn btn-outline-secondary icon-button toolbar-action"
          type="button"
          title="Pane controls"
          aria-label="Pane controls"
          :aria-expanded="mobileToolbarOpen"
          @click="mobileToolbarOpen = !mobileToolbarOpen"
        >
          <i class="bi bi-three-dots-vertical"></i>
        </button>
        <div v-if="mobileToolbarOpen" class="mobile-pane-menu-panel" role="menu">
          <button
            v-for="action in activePaneActions"
            :key="action.id"
            class="mobile-pane-menu-item"
            :class="[{ active: action.active }, action.variant === 'danger' ? 'danger' : '']"
            type="button"
            role="menuitem"
            @click="runMobilePaneAction(action)"
          >
            <i v-if="action.icon" class="bi" :class="action.icon"></i>
            <span v-else-if="action.label" class="mobile-pane-menu-label">{{ action.label }}</span>
            <span>{{ action.title }}</span>
          </button>
          <template v-for="control in activePaneControls" :key="control.id">
            <label v-if="control.kind === 'select'" class="mobile-pane-menu-control">
              <span>{{ control.title }}</span>
              <select
                class="form-select form-select-sm"
                :value="control.value"
                @change="updateMobilePaneControl(control, $event)"
              >
                <option v-for="option in control.options" :key="option" :value="option">{{ option }}</option>
              </select>
            </label>
            <div v-else class="mobile-pane-menu-control">
              <span>{{ control.title }}</span>
              <div class="mobile-pane-menu-chips">
                <span v-for="(item, index) in control.items" :key="`${index}:${item}`" class="pane-toolbar-chip">{{ item }}</span>
              </div>
            </div>
          </template>
        </div>
      </div>
      <div v-if="paneToolbarVisible && activePaneActions.length" class="pane-actions" aria-label="Active pane actions">
        <button
          v-for="action in activePaneActions"
          :key="action.id"
          class="btn btn-outline-secondary icon-button toolbar-action"
          :class="[{ active: action.active, 'has-label': action.label }, action.variant === 'danger' ? 'toolbar-action-danger' : '']"
          type="button"
          :title="action.title"
          :aria-label="action.title"
          @click="runPaneAction(action)"
        >
          <i v-if="action.icon" class="bi" :class="action.icon"></i>
          <span v-else-if="action.label">{{ action.label }}</span>
        </button>
      </div>
      <template v-if="paneToolbarVisible" v-for="control in activePaneControls" :key="control.id">
        <select
          v-if="control.kind === 'select'"
          class="form-select form-select-sm pane-toolbar-select"
          :class="{ 'pane-toolbar-select-compact': control.size === 'compact' }"
          :title="control.title"
          :value="control.value"
          @change="updatePaneControl(control, $event)"
        >
          <option v-for="option in control.options" :key="option" :value="option">{{ option }}</option>
        </select>
        <div v-else class="pane-toolbar-chips" :title="control.title">
          <span v-for="(item, index) in control.items" :key="`${index}:${item}`" class="pane-toolbar-chip">{{ item }}</span>
        </div>
      </template>
      <div v-if="paneToolbarVisible && globalPaneActions.length" class="pane-actions global-pane-actions" aria-label="Global pane actions">
        <button
          v-for="action in globalPaneActions"
          :key="action.id"
          class="btn btn-outline-secondary icon-button toolbar-action"
          :class="[{ active: action.active, 'has-label': action.label }, action.variant === 'danger' ? 'toolbar-action-danger' : '']"
          type="button"
          :title="action.title"
          :aria-label="action.title"
          @click="runPaneAction(action)"
        >
          <i v-if="action.icon" class="bi" :class="action.icon"></i>
          <span v-else-if="action.label">{{ action.label }}</span>
        </button>
      </div>
    </header>

    <main v-if="activePage === 'settings'" class="top-level-page">
      <ConfigPanel @close="activePage = 'super'" />
    </main>
    <main v-else class="top-level-page">
      <SuperWorkspacePage />
    </main>
  </div>
</template>
