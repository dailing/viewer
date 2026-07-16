<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import ConfigPanel from "./components/ConfigPanel.vue";
import SuperWorkspacePage from "./components/SuperWorkspacePage.vue";
import { connectEvents } from "./api/events";
import { DARK_MARKDOWN_THEME, DEFAULT_MARKDOWN_THEME, useFilesStore } from "./stores/files";
import { useTerminalsStore } from "./stores/terminals";
import { useUsersStore } from "./stores/users";
import { useVoiceStore } from "./stores/voice";
import type { AppearanceConfig, MarkdownConfig } from "./types/files";

const files = useFilesStore();
const terminals = useTerminalsStore();
const users = useUsersStore();
const voice = useVoiceStore();

const appReady = ref(false);
const selectingUser = ref(false);
const activePage = ref<"super" | "settings">("super");
const systemPrefersDark = ref(false);
const previewAppearance = ref<AppearanceConfig | null>(null);
const previewMarkdown = ref<MarkdownConfig | null>(null);
let colorSchemeQuery: MediaQueryList | null = null;

const effectiveAppearance = computed(() => previewAppearance.value ?? files.appearance);
const effectiveMarkdown = computed(() => previewMarkdown.value ?? files.markdown);
const effectiveColorTheme = computed<"light" | "dark">(() => {
  if (effectiveAppearance.value.color_theme === "system") return systemPrefersDark.value ? "dark" : "light";
  return effectiveAppearance.value.color_theme;
});
const effectiveDensity = computed(() => effectiveAppearance.value.density ?? "compact");

watch(
  () => files.voiceConfig.language_model_refine,
  (enabled) => voice.setLanguageModelRefine(enabled),
  { immediate: true },
);

const appStyle = computed(() => {
  const titlebarSize = effectiveDensity.value === "comfortable" ? 34 : 28;
  const buttonSize = Math.max(18, titlebarSize - 4);
  const iconSize = Math.max(11, Math.round(titlebarSize * 0.48));
  const configuredTheme = effectiveMarkdown.value.themes.find((item) => item.name === effectiveMarkdown.value.active_theme) ?? DEFAULT_MARKDOWN_THEME;
  const theme = effectiveMarkdown.value.follow_app_theme
    ? effectiveColorTheme.value === "dark" ? DARK_MARKDOWN_THEME : DEFAULT_MARKDOWN_THEME
    : configuredTheme;
  return {
    "--pane-titlebar-height": `${titlebarSize}px`,
    "--nav-button-size": `${buttonSize}px`,
    "--nav-icon-size": `${iconSize}px`,
    "--markdown-body-font-size": `${theme.body.font_size ?? 15}px`,
    "--markdown-body-color": theme.body.color ?? "#404449",
    "--markdown-body-line-height": String(theme.body.line_height ?? 1.65),
    "--markdown-h1-font-size": `${theme.h1.font_size ?? 28}px`,
    "--markdown-h1-color": theme.h1.color ?? "#30343a",
    "--markdown-h1-font-weight": theme.h1.font_weight ?? "700",
    "--markdown-h1-line-height": String(theme.h1.line_height ?? 1.2),
    "--markdown-h2-font-size": `${theme.h2.font_size ?? 23}px`,
    "--markdown-h2-color": theme.h2.color ?? "#30343a",
    "--markdown-h2-font-weight": theme.h2.font_weight ?? "700",
    "--markdown-h2-line-height": String(theme.h2.line_height ?? 1.25),
    "--markdown-h3-font-size": `${theme.h3.font_size ?? 19}px`,
    "--markdown-h3-color": theme.h3.color ?? "#34383d",
    "--markdown-h3-font-weight": theme.h3.font_weight ?? "700",
    "--markdown-h3-line-height": String(theme.h3.line_height ?? 1.3),
    "--markdown-h4-font-size": `${theme.h4.font_size ?? 16}px`,
    "--markdown-h4-color": theme.h4.color ?? "#34383d",
    "--markdown-h4-font-weight": theme.h4.font_weight ?? "700",
    "--markdown-h4-line-height": String(theme.h4.line_height ?? 1.35),
    "--markdown-paragraph-font-size": `${theme.paragraph.font_size ?? 15}px`,
    "--markdown-paragraph-color": theme.paragraph.color ?? "#404449",
    "--markdown-paragraph-line-height": String(theme.paragraph.line_height ?? 1.65),
    "--markdown-code-font-size": `${theme.code.font_size ?? 13}px`,
    "--markdown-code-color": theme.code.color ?? "#4a4e53",
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

let source: EventSource | null = null;
let terminalRefresh: number | null = null;

onMounted(async () => {
  colorSchemeQuery = window.matchMedia("(prefers-color-scheme: dark)");
  systemPrefersDark.value = colorSchemeQuery.matches;
  colorSchemeQuery.addEventListener("change", handleColorSchemeChange);
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


onUnmounted(() => {
  colorSchemeQuery?.removeEventListener("change", handleColorSchemeChange);
  source?.close();
  if (terminalRefresh !== null) window.clearInterval(terminalRefresh);
});

function handleColorSchemeChange(event: MediaQueryListEvent) {
  systemPrefersDark.value = event.matches;
}

function previewConfig(appearance: AppearanceConfig, markdown: MarkdownConfig) {
  previewAppearance.value = JSON.parse(JSON.stringify(appearance)) as AppearanceConfig;
  previewMarkdown.value = JSON.parse(JSON.stringify(markdown)) as MarkdownConfig;
}

function closeSettings() {
  previewAppearance.value = null;
  previewMarkdown.value = null;
  activePage.value = "super";
}
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
  <div v-else-if="appReady" class="app-shell" :data-theme="effectiveColorTheme" :data-density="effectiveDensity" :style="appStyle">
    <main v-if="activePage === 'settings'" class="top-level-page">
      <ConfigPanel @close="closeSettings" @preview="previewConfig" />
    </main>
    <main v-else class="top-level-page">
      <SuperWorkspacePage @open-settings="activePage = 'settings'" />
    </main>
  </div>
</template>
