<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { restartServer, stopServer } from "../api/client";
import { DARK_MARKDOWN_THEME, DEFAULT_CODEX_CONFIG, DEFAULT_DISPATCH_PROFILES, DEFAULT_DISPATCH_PROMPT_TEMPLATE, DEFAULT_MARKDOWN_THEME, DEFAULT_SUPER_WORKSPACE_CONFIG, DEFAULT_VOICE_CONFIG, useFilesStore } from "../stores/files";
import { useUsersStore } from "../stores/users";
import type { AppearanceConfig, CodexConfig, MarkdownConfig, MarkdownElementStyle, MarkdownTheme, SuperWorkspaceConfig, SuperWorkspaceDispatchProfile, VoiceConfig } from "../types/files";

const emit = defineEmits<{
  close: [];
  preview: [appearance: AppearanceConfig, markdown: MarkdownConfig];
}>();
const files = useFilesStore();
const users = useUsersStore();
const saving = ref(false);
const restarting = ref<"backend" | "all" | "">("");
const stopping = ref(false);
const error = ref("");
const serverNotice = ref("");
const dispatchPromptVariables = ["{{message}}", "{{history}}", "{{roles_json}}", "{{roles_table}}"];
const jsonDraft = ref("");
const settingsSearch = ref("");
const activeSettingSection = ref("appearance");
const settingSections = [
  { id: "appearance", label: "Appearance", icon: "bi-palette" },
  { id: "users", label: "User Profile", icon: "bi-person" },
  { id: "codex", label: "Codex Models", icon: "bi-cpu" },
  { id: "superWorkspace", label: "Super Workspace", icon: "bi-diagram-3" },
  { id: "voice", label: "Voice", icon: "bi-mic" },
  { id: "markdown", label: "Markdown", icon: "bi-markdown" },
  { id: "syntax", label: "Syntax", icon: "bi-code-slash" },
  { id: "server", label: "Server", icon: "bi-hdd-rack" },
  { id: "json", label: "Advanced", icon: "bi-braces" },
] as const;
const draft = reactive({
  appearance: clone(files.appearance) as AppearanceConfig,
  codex: clone(files.codexConfig) as CodexConfig,
  superWorkspace: clone(files.superWorkspaceConfig) as SuperWorkspaceConfig,
  voice: clone(files.voiceConfig) as VoiceConfig,
  markdown: clone(files.markdown) as MarkdownConfig,
});

const activeTheme = computed(() => {
  const theme = draft.markdown.themes.find((item) => item.name === draft.markdown.active_theme);
  if (theme) return theme;
  const fallback = draft.markdown.themes[0] ?? clone(DEFAULT_MARKDOWN_THEME);
  draft.markdown.active_theme = fallback.name;
  if (!draft.markdown.themes.length) draft.markdown.themes.push(fallback);
  return fallback;
});

const activeDispatchProfile = computed<SuperWorkspaceDispatchProfile>(() => {
  if (!draft.superWorkspace.dispatch_profiles.length) {
    draft.superWorkspace.dispatch_profiles.push(...clone(DEFAULT_DISPATCH_PROFILES));
  }
  let profile = draft.superWorkspace.dispatch_profiles.find((item) => item.id === draft.superWorkspace.active_dispatch_profile_id);
  if (!profile) {
    profile = draft.superWorkspace.dispatch_profiles[0];
    draft.superWorkspace.active_dispatch_profile_id = profile.id;
  }
  return profile as SuperWorkspaceDispatchProfile;
});

const fullConfigJson = computed(() =>
  JSON.stringify(
    {
      appearance: draft.appearance,
      codex: draft.codex,
      super_workspace: draft.superWorkspace,
      voice: draft.voice,
      markdown: draft.markdown,
    },
    null,
    2,
  ),
);
const initialConfigJson = ref(fullConfigJson.value);
const hasUnsavedChanges = computed(() => fullConfigJson.value !== initialConfigJson.value);
const filteredSettingSections = computed(() => {
  const query = settingsSearch.value.trim().toLowerCase();
  return query ? settingSections.filter((section) => section.label.toLowerCase().includes(query)) : settingSections;
});

watch(
  () => files.appearance,
  (appearance) => Object.assign(draft.appearance, clone(appearance)),
  { deep: true },
);

watch(
  () => files.codexConfig,
  (codex) => {
    Object.assign(draft.codex, clone(codex));
    jsonDraft.value = fullConfigJson.value;
  },
  { deep: true },
);

watch(
  () => files.superWorkspaceConfig,
  (superWorkspace) => {
    Object.assign(draft.superWorkspace, clone(superWorkspace));
    jsonDraft.value = fullConfigJson.value;
  },
  { deep: true },
);

watch(
  () => files.voiceConfig,
  (voice) => {
    Object.assign(draft.voice, clone(voice));
    jsonDraft.value = fullConfigJson.value;
  },
  { deep: true },
);

watch(
  () => files.markdown,
  (markdown) => {
    Object.assign(draft.markdown, clone(markdown));
    jsonDraft.value = fullConfigJson.value;
  },
  { deep: true },
);

watch(fullConfigJson, (value) => {
  if (activeSettingSection.value !== "json") jsonDraft.value = value;
});

watch(
  [() => draft.appearance, () => draft.markdown],
  () => emit("preview", clone(draft.appearance), clone(draft.markdown)),
  { deep: true, immediate: true },
);

jsonDraft.value = fullConfigJson.value;

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function selectSettingSection(section: string) {
  activeSettingSection.value = section;
  if (section === "json") jsonDraft.value = fullConfigJson.value;
}

function sleep(ms: number) {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

function switchUser(userId: string) {
  if (!userId || userId === users.activeUserId) return;
  users.select(userId);
  window.location.reload();
}

async function waitForServer(previousPid: number) {
  await sleep(1000);
  for (let attempt = 0; attempt < 60; attempt += 1) {
    try {
      const response = await fetch(`/api/health?t=${Date.now()}`, { cache: "no-store" });
      if (response.ok) {
        const health = (await response.json()) as { pid?: number };
        if (health.pid && health.pid !== previousPid) {
          window.location.reload();
          return;
        }
      }
    } catch {
      // The server is expected to be unavailable while the helper restarts it.
    }
    await sleep(1000);
  }
  error.value = "Restart was requested, but the server did not come back within 60 seconds.";
  restarting.value = "";
}

async function restart(includeWorker: boolean) {
  if (restarting.value) return;
  const message = includeWorker
    ? "Restart the viewer server and Super Workspace worker now?"
    : "Restart only the viewer backend server now?";
  if (!window.confirm(message)) return;
  restarting.value = includeWorker ? "all" : "backend";
  error.value = "";
  serverNotice.value = "";
  try {
    const response = await restartServer(includeWorker);
    await waitForServer(response.pid);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
    restarting.value = "";
  }
}

async function stop() {
  if (stopping.value) return;
  if (!window.confirm("Stop the viewer server now? Use scripts/manage_viewer.py start to bring it back.")) return;
  stopping.value = true;
  error.value = "";
  serverNotice.value = "";
  try {
    await stopServer();
    serverNotice.value = "Stop requested. Restart from the command line with scripts/manage_viewer.py start.";
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
    stopping.value = false;
  }
}

function normalizeModelList(value: string) {
  const seen = new Set<string>();
  const available = value
    .split(/\r?\n|,/)
    .map((model) => model.trim())
    .filter((model) => {
      if (!model || seen.has(model)) return false;
      seen.add(model);
      return true;
    });
  draft.codex.available_models = available.length ? available : [...DEFAULT_CODEX_CONFIG.available_models];
  if (!draft.codex.default_model || !draft.codex.available_models.includes(draft.codex.default_model)) {
    draft.codex.default_model = draft.codex.available_models[0] ?? DEFAULT_CODEX_CONFIG.default_model;
  }
}

function setActiveDispatchProfile(profileId: string) {
  const profile = draft.superWorkspace.dispatch_profiles.find((item) => item.id === profileId);
  if (profile) draft.superWorkspace.active_dispatch_profile_id = profile.id;
}

function addDispatchProfile() {
  const baseId = "dispatch";
  let index = draft.superWorkspace.dispatch_profiles.length + 1;
  let id = `${baseId}-${index}`;
  while (draft.superWorkspace.dispatch_profiles.some((profile) => profile.id === id)) {
    index += 1;
    id = `${baseId}-${index}`;
  }
  const profile: SuperWorkspaceDispatchProfile = {
    id,
    name: `Dispatch ${index}`,
    api_url: DEFAULT_DISPATCH_PROFILES[0].api_url,
    model: "",
    api_key: "",
  };
  draft.superWorkspace.dispatch_profiles.push(profile);
  draft.superWorkspace.active_dispatch_profile_id = profile.id;
}

function removeActiveDispatchProfile() {
  if (draft.superWorkspace.dispatch_profiles.length <= 1) return;
  const id = activeDispatchProfile.value.id;
  draft.superWorkspace.dispatch_profiles = draft.superWorkspace.dispatch_profiles.filter((profile) => profile.id !== id);
  draft.superWorkspace.active_dispatch_profile_id = draft.superWorkspace.dispatch_profiles[0]?.id ?? DEFAULT_SUPER_WORKSPACE_CONFIG.active_dispatch_profile_id;
}

function applyDispatchPreset(presetId: string) {
  const preset = DEFAULT_DISPATCH_PROFILES.find((profile) => profile.id === presetId);
  if (!preset) return;
  const index = draft.superWorkspace.dispatch_profiles.findIndex((profile) => profile.id === preset.id);
  const replacement = clone(preset);
  if (index === -1) {
    draft.superWorkspace.dispatch_profiles.push(replacement);
  } else {
    draft.superWorkspace.dispatch_profiles[index] = {
      ...replacement,
      api_key: draft.superWorkspace.dispatch_profiles[index].api_key || replacement.api_key,
    };
  }
  draft.superWorkspace.active_dispatch_profile_id = replacement.id;
}

function resetDispatchPromptTemplate() {
  draft.superWorkspace.dispatch_prompt_template = DEFAULT_DISPATCH_PROMPT_TEMPLATE;
}

function normalizeVoiceModelList(value: string) {
  const seen = new Set<string>();
  const available = value
    .split(/\r?\n|,/)
    .map((model) => model.trim())
    .filter((model) => {
      if (!model || seen.has(model)) return false;
      seen.add(model);
      return true;
    });
  draft.voice.available_models = available.length ? available : [...DEFAULT_VOICE_CONFIG.available_models];
  if (!draft.voice.model || !draft.voice.available_models.includes(draft.voice.model)) {
    draft.voice.model = draft.voice.available_models[0] ?? DEFAULT_VOICE_CONFIG.model;
  }
}

function setVoiceModel(value: string) {
  const model = value.trim();
  draft.voice.model = model;
  if (model && !draft.voice.available_models.includes(model)) {
    draft.voice.available_models = [model, ...draft.voice.available_models];
  }
}

function normalizeLanguage(value: string) {
  const cleaned = value.trim().toLowerCase();
  return cleaned === "cn" ? "zh" : cleaned;
}

function setVoiceLanguage(value: string) {
  const language = normalizeLanguage(value);
  draft.voice.language = language || DEFAULT_VOICE_CONFIG.language;
  if (!draft.voice.available_languages.includes(draft.voice.language)) {
    draft.voice.available_languages = [draft.voice.language, ...draft.voice.available_languages];
  }
}

function setVoiceTargetLanguage(value: string) {
  const language = normalizeLanguage(value);
  draft.voice.target_language = language === "auto" || !language ? DEFAULT_VOICE_CONFIG.target_language : language;
  if (!draft.voice.available_target_languages.includes(draft.voice.target_language)) {
    draft.voice.available_target_languages = [draft.voice.target_language, ...draft.voice.available_target_languages];
  }
}

function numberValue(value: number | null, fallback: number) {
  return value ?? fallback;
}

function updateStyleNumber(style: MarkdownElementStyle, key: "font_size" | "line_height", value: string) {
  const next = Number(value);
  style[key] = Number.isFinite(next) ? next : null;
}

function updateStyleText(style: MarkdownElementStyle, key: "color" | "font_weight", value: string) {
  style[key] = value.trim() || null;
}

function duplicateTheme() {
  const next = clone(activeTheme.value);
  const baseName = `${next.name} Copy`;
  let name = baseName;
  let index = 2;
  while (draft.markdown.themes.some((theme) => theme.name === name)) {
    name = `${baseName} ${index}`;
    index += 1;
  }
  next.name = name;
  draft.markdown.themes.push(next);
  draft.markdown.active_theme = name;
}

function renameActiveTheme(name: string) {
  const clean = name.trim() || "Custom";
  activeTheme.value.name = clean;
  draft.markdown.active_theme = clean;
}

function resetActiveTheme() {
  const replacement = clone(DEFAULT_MARKDOWN_THEME);
  const index = draft.markdown.themes.findIndex((theme) => theme.name === activeTheme.value.name);
  if (index === -1) {
    draft.markdown.themes.push(replacement);
  } else {
    draft.markdown.themes[index] = replacement;
  }
  draft.markdown.active_theme = replacement.name;
}

function applyDarkMarkdownTheme() {
  const replacement = clone(DARK_MARKDOWN_THEME);
  const index = draft.markdown.themes.findIndex((theme) => theme.name === replacement.name);
  if (index === -1) {
    draft.markdown.themes.push(replacement);
  } else {
    draft.markdown.themes[index] = replacement;
  }
  draft.markdown.active_theme = replacement.name;
}

async function save() {
  saving.value = true;
  error.value = "";
  try {
    await files.saveFullViewerConfig(draft.appearance, draft.markdown, draft.codex, draft.voice, draft.superWorkspace);
    emit("close");
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    saving.value = false;
  }
}

async function applyJson() {
  try {
    const parsed = JSON.parse(jsonDraft.value);
    if (parsed.appearance) Object.assign(draft.appearance, parsed.appearance);
    if (parsed.codex) Object.assign(draft.codex, parsed.codex);
    if (parsed.super_workspace) Object.assign(draft.superWorkspace, parsed.super_workspace);
    if (parsed.voice) Object.assign(draft.voice, parsed.voice);
    if (parsed.markdown) Object.assign(draft.markdown, parsed.markdown);
    await save();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}
</script>

<template>
  <div class="config-page">
    <section class="config-panel" aria-label="Configuration">
      <header class="config-header">
        <div>
          <h2>Configuration</h2>
          <span>{{ hasUnsavedChanges ? "Unsaved changes" : "~/.view/config.json" }}</span>
        </div>
        <button class="btn btn-outline-secondary icon-button" type="button" title="Close configuration" @click="emit('close')">
          <i class="bi bi-x"></i>
        </button>
      </header>

      <div class="config-main">
        <aside class="config-nav" aria-label="Settings categories">
          <label class="config-search">
            <i class="bi bi-search"></i>
            <input v-model="settingsSearch" type="search" placeholder="Search settings" aria-label="Search settings" />
          </label>
          <button
            v-for="section in filteredSettingSections"
            :key="section.id"
            class="config-nav-item"
            :class="{ active: activeSettingSection === section.id }"
            type="button"
            @click="selectSettingSection(section.id)"
          >
            <i class="bi" :class="section.icon"></i>
            <span>{{ section.label }}</span>
          </button>
        </aside>
        <div class="config-content">
        <section v-show="activeSettingSection === 'server'" class="config-section">
          <div class="section-heading"><i class="bi bi-hdd-rack"></i><h2>Server</h2></div>
          <div class="section-body">
            <div class="server-actions">
              <button class="btn btn-sm btn-outline-danger" type="button" :disabled="!!restarting || stopping" @click="restart(false)">
                <span v-if="restarting === 'backend'" class="spinner-border spinner-border-sm"></span>
                <i v-else class="bi bi-arrow-clockwise"></i>
                <span>{{ restarting === "backend" ? "Restarting" : "Restart backend" }}</span>
              </button>
              <button class="btn btn-sm btn-outline-danger" type="button" :disabled="!!restarting || stopping" @click="restart(true)">
                <span v-if="restarting === 'all'" class="spinner-border spinner-border-sm"></span>
                <i v-else class="bi bi-arrow-repeat"></i>
                <span>{{ restarting === "all" ? "Restarting all" : "Restart all" }}</span>
              </button>
              <button class="btn btn-sm btn-outline-danger" type="button" :disabled="stopping || !!restarting" @click="stop">
                <span v-if="stopping" class="spinner-border spinner-border-sm"></span>
                <i v-else class="bi bi-stop-fill"></i>
                <span>{{ stopping ? "Stopping" : "Stop server" }}</span>
              </button>
            </div>
            <div v-if="serverNotice" class="server-notice">{{ serverNotice }}</div>
          </div>
        </section>

        <section v-show="activeSettingSection === 'users'" class="config-section">
          <div class="section-heading"><i class="bi bi-person"></i><h2>User Profile</h2></div>
          <div class="section-body">
            <label class="compact-field">
              <span>Active profile</span>
              <select class="form-select form-select-sm" :value="users.activeUserId" @change="switchUser(($event.target as HTMLSelectElement).value)">
                <option v-for="profile in users.profiles" :key="profile.id" :value="profile.id">
                  {{ profile.name || profile.id }} - {{ profile.home || "/" }}
                </option>
              </select>
            </label>
          </div>
        </section>

        <section v-show="activeSettingSection === 'appearance'" class="config-section">
          <div class="section-heading"><i class="bi bi-palette"></i><h2>Appearance</h2></div>
          <div class="section-body">
            <label class="compact-field">
              <span>Theme</span>
              <select v-model="draft.appearance.color_theme" class="form-select form-select-sm">
                <option value="system">System</option>
                <option value="light">Light</option>
                <option value="dark">Dark</option>
              </select>
            </label>
            <label class="compact-field">
              <span>Density</span>
              <select v-model="draft.appearance.density" class="form-select form-select-sm">
                <option value="compact">Compact</option>
                <option value="comfortable">Comfortable</option>
              </select>
            </label>
          </div>
        </section>

        <section v-show="activeSettingSection === 'codex'" class="config-section">
          <div class="section-heading"><i class="bi bi-cpu"></i><h2>Codex Models</h2></div>
          <div class="section-body">
            <label class="compact-field">
              <span>Default model</span>
              <select v-model="draft.codex.default_model" class="form-select form-select-sm">
                <option v-for="model in draft.codex.available_models" :key="model" :value="model">{{ model }}</option>
              </select>
            </label>
            <label class="compact-field model-list-field">
              <span>Available models</span>
              <textarea
                class="form-control form-control-sm model-list"
                :value="draft.codex.available_models.join('\n')"
                spellcheck="false"
                @change="normalizeModelList(($event.target as HTMLTextAreaElement).value)"
              ></textarea>
            </label>
            <label class="setting-row">
              <span>Muted message alpha</span>
              <input v-model.number="draft.codex.muted_message_alpha" class="form-range" type="range" min="0.15" max="1" step="0.01" />
              <input v-model.number="draft.codex.muted_message_alpha" class="form-control form-control-sm number-input" type="number" min="0.15" max="1" step="0.01" />
            </label>
            <label class="compact-field">
              <span>Proxy</span>
              <input
                v-model.trim="draft.codex.proxy"
                class="form-control form-control-sm"
                placeholder="http://localhost:7890"
              />
            </label>
          </div>
        </section>

        <section v-show="activeSettingSection === 'superWorkspace'" class="config-section">
          <div class="section-heading"><i class="bi bi-diagram-3"></i><h2>Super Workspace</h2></div>
          <div class="section-body">
            <label class="compact-field">
              <span>Dispatch profile</span>
              <select class="form-select form-select-sm" :value="draft.superWorkspace.active_dispatch_profile_id" @change="setActiveDispatchProfile(($event.target as HTMLSelectElement).value)">
                <option v-for="profile in draft.superWorkspace.dispatch_profiles" :key="profile.id" :value="profile.id">
                  {{ profile.name || profile.id }}
                </option>
              </select>
            </label>
            <div class="inline-actions">
              <button class="btn btn-sm btn-outline-secondary" type="button" @click="applyDispatchPreset('local-vllm')">
                <i class="bi bi-cpu"></i>
                <span>Local vLLM</span>
              </button>
              <button class="btn btn-sm btn-outline-secondary" type="button" @click="applyDispatchPreset('deepseek')">
                <i class="bi bi-cloud"></i>
                <span>DeepSeek</span>
              </button>
              <button class="btn btn-sm btn-outline-secondary" type="button" @click="addDispatchProfile">
                <i class="bi bi-plus-lg"></i>
                <span>Add</span>
              </button>
              <button class="btn btn-sm btn-outline-danger" type="button" :disabled="draft.superWorkspace.dispatch_profiles.length <= 1" @click="removeActiveDispatchProfile">
                <i class="bi bi-trash"></i>
              </button>
            </div>
            <label class="compact-field">
              <span>Profile name</span>
              <input v-model.trim="activeDispatchProfile.name" class="form-control form-control-sm" />
            </label>
            <label class="compact-field">
              <span>Chat completions URL</span>
              <input
                v-model.trim="activeDispatchProfile.api_url"
                class="form-control form-control-sm"
                placeholder="http://127.0.0.1:8010/v1/chat/completions"
              />
            </label>
            <label class="compact-field">
              <span>Model name</span>
              <input
                v-model.trim="activeDispatchProfile.model"
                class="form-control form-control-sm"
                placeholder="Leave empty to discover /v1/models"
              />
            </label>
            <label class="compact-field">
              <span>API key</span>
              <input
                v-model.trim="activeDispatchProfile.api_key"
                class="form-control form-control-sm"
                type="password"
                autocomplete="off"
                placeholder="Optional"
              />
            </label>
            <label class="setting-row">
              <span>Dispatch history budget</span>
              <input
                v-model.number="draft.superWorkspace.dispatch_history_word_budget"
                class="form-range"
                type="range"
                min="0"
                max="20000"
                step="500"
              />
              <input
                v-model.number="draft.superWorkspace.dispatch_history_word_budget"
                class="form-control form-control-sm number-input"
                type="number"
                min="0"
                max="50000"
                step="500"
              />
            </label>
            <label class="compact-field prompt-template-field">
              <span>Dispatch prompt</span>
              <div class="prompt-template-editor">
                <textarea
                  v-model="draft.superWorkspace.dispatch_prompt_template"
                  class="form-control form-control-sm dispatch-prompt-template"
                  spellcheck="false"
                ></textarea>
                <div class="template-footer">
                  <span>Variables: {{ dispatchPromptVariables.join(", ") }}</span>
                  <button class="btn btn-sm btn-outline-secondary" type="button" @click="resetDispatchPromptTemplate">
                    <i class="bi bi-arrow-counterclockwise"></i>
                    <span>Reset</span>
                  </button>
                </div>
              </div>
            </label>
            <label class="compact-field checkbox-field">
              <span>Hindsight retain</span>
              <input v-model="draft.superWorkspace.hindsight_retain_enabled" class="form-check-input" type="checkbox" />
            </label>
            <label class="compact-field">
              <span>Hindsight API URL</span>
              <input
                v-model.trim="draft.superWorkspace.hindsight_api_url"
                class="form-control form-control-sm"
                placeholder="Use ~/.hindsight/codex.json"
              />
            </label>
            <label class="compact-field">
              <span>Memory bank prefix</span>
              <input
                v-model.trim="draft.superWorkspace.hindsight_bank_prefix"
                class="form-control form-control-sm"
                :placeholder="DEFAULT_SUPER_WORKSPACE_CONFIG.hindsight_bank_prefix"
              />
            </label>
            <label class="compact-field checkbox-field">
              <span>Chat history bootstrap</span>
              <input v-model="draft.superWorkspace.chat_history_bootstrap_enabled" class="form-check-input" type="checkbox" />
            </label>
            <label class="setting-row">
              <span>History token budget</span>
              <input
                v-model.number="draft.superWorkspace.chat_history_bootstrap_tokens"
                class="form-range"
                type="range"
                min="0"
                max="20000"
                step="500"
              />
              <input
                v-model.number="draft.superWorkspace.chat_history_bootstrap_tokens"
                class="form-control form-control-sm number-input"
                type="number"
                min="0"
                max="50000"
                step="500"
              />
            </label>
          </div>
        </section>

        <section v-show="activeSettingSection === 'voice'" class="config-section">
          <div class="section-heading"><i class="bi bi-mic"></i><h2>Voice</h2></div>
          <div class="section-body">
            <label class="compact-field checkbox-field">
              <span>Enabled</span>
              <input v-model="draft.voice.enabled" class="form-check-input" type="checkbox" />
            </label>
            <label class="compact-field">
              <span>Whisper model</span>
              <input
                class="form-control form-control-sm"
                list="voice-model-options"
                :value="draft.voice.model"
                @input="setVoiceModel(($event.target as HTMLInputElement).value)"
              />
            </label>
            <datalist id="voice-model-options">
              <option v-for="model in draft.voice.available_models" :key="model" :value="model"></option>
            </datalist>
            <label class="compact-field model-list-field">
              <span>Model options</span>
              <textarea
                class="form-control form-control-sm model-list"
                :value="draft.voice.available_models.join('\n')"
                spellcheck="false"
                @change="normalizeVoiceModelList(($event.target as HTMLTextAreaElement).value)"
              ></textarea>
            </label>
            <label class="compact-field">
              <span>Language</span>
              <input
                class="form-control form-control-sm"
                list="voice-language-options"
                :value="draft.voice.language"
                placeholder="auto"
                @input="setVoiceLanguage(($event.target as HTMLInputElement).value)"
              />
            </label>
            <datalist id="voice-language-options">
              <option v-for="language in draft.voice.available_languages" :key="language" :value="language"></option>
            </datalist>
            <label class="compact-field checkbox-field">
              <span>Translate</span>
              <input v-model="draft.voice.translation_enabled" class="form-check-input" type="checkbox" />
            </label>
            <label class="compact-field">
              <span>Target language</span>
              <input
                class="form-control form-control-sm"
                list="voice-target-language-options"
                :disabled="!draft.voice.translation_enabled"
                :value="draft.voice.target_language"
                @input="setVoiceTargetLanguage(($event.target as HTMLInputElement).value)"
              />
            </label>
            <datalist id="voice-target-language-options">
              <option v-for="language in draft.voice.available_target_languages" :key="language" :value="language"></option>
            </datalist>
          </div>
        </section>

        <section v-show="activeSettingSection === 'markdown'" class="config-section">
          <div class="section-heading"><i class="bi bi-markdown"></i><h2>Markdown</h2></div>
          <div class="section-body">
            <label class="compact-field checkbox-field">
              <span>Follow app theme</span>
              <input v-model="draft.markdown.follow_app_theme" class="form-check-input" type="checkbox" />
            </label>
            <div class="theme-toolbar">
              <select v-model="draft.markdown.active_theme" class="form-select form-select-sm" :disabled="draft.markdown.follow_app_theme">
                <option v-for="theme in draft.markdown.themes" :key="theme.name" :value="theme.name">{{ theme.name }}</option>
              </select>
              <button class="btn btn-sm btn-outline-secondary" type="button" @click="duplicateTheme">
                <i class="bi bi-copy"></i>
              </button>
              <button class="btn btn-sm btn-outline-secondary" type="button" @click="resetActiveTheme">
                <i class="bi bi-arrow-counterclockwise"></i>
              </button>
              <button class="btn btn-sm btn-outline-secondary" type="button" @click="applyDarkMarkdownTheme">
                Dark
              </button>
            </div>
            <label class="compact-field">
              <span>Theme name</span>
              <input class="form-control form-control-sm" :value="activeTheme.name" @input="renameActiveTheme(($event.target as HTMLInputElement).value)" />
            </label>

            <div class="style-grid">
              <label v-for="item in [
                ['Body', activeTheme.body],
                ['Heading 1', activeTheme.h1],
                ['Heading 2', activeTheme.h2],
                ['Heading 3', activeTheme.h3],
                ['Heading 4', activeTheme.h4],
                ['Paragraph', activeTheme.paragraph],
                ['Code', activeTheme.code],
              ]" :key="item[0] as string" class="style-card">
                <strong>{{ item[0] }}</strong>
                <span>Size</span>
                <input
                  class="form-control form-control-sm"
                  type="number"
                  min="9"
                  max="72"
                  :value="numberValue((item[1] as MarkdownElementStyle).font_size, 15)"
                  @input="updateStyleNumber(item[1] as MarkdownElementStyle, 'font_size', ($event.target as HTMLInputElement).value)"
                />
                <span>Color</span>
                <input
                  class="form-control form-control-sm form-control-color"
                  type="color"
                  :value="(item[1] as MarkdownElementStyle).color || '#404449'"
                  @input="updateStyleText(item[1] as MarkdownElementStyle, 'color', ($event.target as HTMLInputElement).value)"
                />
                <span>Weight</span>
                <input
                  class="form-control form-control-sm"
                  :value="(item[1] as MarkdownElementStyle).font_weight || ''"
                  placeholder="normal"
                  @input="updateStyleText(item[1] as MarkdownElementStyle, 'font_weight', ($event.target as HTMLInputElement).value)"
                />
              </label>
            </div>

            <div class="color-grid">
              <label>
                <span>Link</span>
                <input v-model="activeTheme.link_color" class="form-control form-control-sm form-control-color" type="color" />
              </label>
              <label>
                <span>Code background</span>
                <input v-model="activeTheme.code_background" class="form-control form-control-sm form-control-color" type="color" />
              </label>
              <label>
                <span>Border</span>
                <input v-model="activeTheme.border_color" class="form-control form-control-sm form-control-color" type="color" />
              </label>
            </div>
          </div>
        </section>

        <section v-show="activeSettingSection === 'syntax'" class="config-section">
          <div class="section-heading"><i class="bi bi-code-slash"></i><h2>Syntax Highlighting</h2></div>
          <div class="section-body color-grid syntax-grid">
            <label v-for="key in Object.keys(activeTheme.syntax)" :key="key">
              <span>{{ key }}</span>
              <input v-model="activeTheme.syntax[key as keyof MarkdownTheme['syntax']]" class="form-control form-control-sm form-control-color" type="color" />
            </label>
          </div>
        </section>

        <section v-show="activeSettingSection === 'json'" class="config-section">
          <div class="section-heading"><i class="bi bi-braces"></i><h2>JSON</h2></div>
          <div class="section-body">
            <textarea v-model="jsonDraft" class="json-editor" spellcheck="false"></textarea>
            <button class="btn btn-sm btn-outline-secondary" type="button" @click="applyJson">Apply JSON</button>
          </div>
        </section>
        </div>
      </div>

      <footer class="config-footer">
        <span v-if="error" class="config-error">{{ error }}</span>
        <button class="btn btn-sm btn-outline-secondary" type="button" @click="emit('close')">Cancel</button>
        <span v-if="hasUnsavedChanges && !error" class="config-dirty"><i class="bi bi-circle-fill"></i> Unsaved</span>
        <button class="btn btn-sm btn-primary" type="button" :disabled="saving || !hasUnsavedChanges" @click="save">
          <span v-if="saving" class="spinner-border spinner-border-sm"></span>
          <span v-else>Save</span>
        </button>
      </footer>
    </section>
  </div>
</template>

<style scoped>
.config-page {
  background: var(--color-surface);
  height: 100%;
  overflow: hidden;
}

.config-panel {
  background: var(--color-surface);
  display: flex;
  flex-direction: column;
  height: 100%;
  margin: 0 auto;
  max-width: 100vw;
  min-width: 0;
  width: min(1080px, 100vw);
}

.config-header,
.config-footer {
  align-items: center;
  background: var(--color-surface-muted);
  display: flex;
  flex: 0 0 auto;
  gap: 6px;
  justify-content: space-between;
  padding: 7px 9px;
}

.config-footer {
  border: 0;
  justify-content: flex-end;
}

.config-header h2 {
  font-size: 16px;
  line-height: 1.2;
  margin: 0;
}

.config-header span,
.config-error,
.server-notice {
  color: var(--color-text-muted);
  font-size: 12px;
}

.config-error {
  color: var(--color-danger);
  margin-right: auto;
}

.config-main {
  display: grid;
  flex: 1 1 auto;
  grid-template-columns: 210px minmax(0, 1fr);
  min-height: 0;
}

.config-nav {
  background: var(--color-surface-muted);
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-height: 0;
  overflow-y: auto;
  padding: 7px 6px;
}

.config-search {
  align-items: center;
  background: var(--color-surface-raised);
  border: 0;
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  display: flex;
  gap: 7px;
  margin-bottom: 4px;
  padding: 4px 6px;
}

.config-search input {
  background: transparent;
  border: 0;
  color: var(--color-text);
  font-size: 12px;
  min-width: 0;
  outline: 0;
  width: 100%;
}

.config-nav-item {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  display: flex;
  font-size: 12px;
  gap: 7px;
  min-height: 34px;
  padding: 5px 7px;
  text-align: left;
}

.config-nav-item:hover {
  background: var(--color-surface-hover);
  color: var(--color-text);
}

.config-nav-item.active {
  background: var(--color-accent-soft);
  color: var(--color-accent-hover);
  font-weight: 700;
}

.config-content {
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
}

.config-section {
  border-bottom: 0;
  min-height: 100%;
}

.section-heading {
  align-items: center;
  display: flex;
  gap: 8px;
  padding: 10px 12px 6px;
}

.section-heading h2 {
  font-size: 15px;
  font-weight: 700;
  margin: 0;
}

.section-heading .bi {
  color: var(--color-text-muted);
}

.section-body {
  max-width: 780px;
  padding: 0 12px 14px;
}

.server-actions,
.inline-actions {
  align-items: center;
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.server-actions .btn,
.inline-actions .btn {
  align-items: center;
  display: inline-flex;
  gap: 6px;
}

.server-notice {
  margin-top: 8px;
}

.setting-row,
.compact-field {
  align-items: center;
  display: grid;
  gap: 8px;
  grid-template-columns: 120px 1fr auto;
}

.compact-field {
  grid-template-columns: 120px 1fr;
  margin-top: 10px;
}

.setting-row > *,
.compact-field > *,
.theme-toolbar > *,
.style-card > * {
  min-width: 0;
}

.checkbox-field {
  justify-content: start;
}

.checkbox-field .form-check-input {
  margin: 0;
}

.prompt-template-field {
  align-items: start;
}

.prompt-template-editor {
  display: grid;
  gap: 6px;
}

.dispatch-prompt-template {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  min-height: 220px;
  resize: vertical;
}

.template-footer {
  align-items: center;
  display: flex;
  gap: 8px;
  justify-content: space-between;
}

.template-footer span {
  color: var(--color-text-muted);
  font-size: 11px;
}

.template-footer .btn {
  align-items: center;
  display: inline-flex;
  gap: 5px;
  white-space: nowrap;
}

.number-input {
  width: 78px;
}

.theme-toolbar {
  display: grid;
  gap: 8px;
  grid-template-columns: 1fr auto auto;
}

.style-grid {
  display: grid;
  gap: 6px;
  grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
  margin-top: 7px;
}

.style-card {
  border: 0;
  background: var(--color-surface-muted);
  border-radius: var(--radius-md);
  display: grid;
  gap: 6px;
  grid-template-columns: 62px 1fr;
  padding: 7px;
}

.style-card strong {
  grid-column: 1 / -1;
}

.style-card span,
.color-grid span,
.compact-field span,
.setting-row span {
  color: var(--color-text-muted);
  font-size: 12px;
}

.color-grid {
  display: grid;
  gap: 7px;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  margin-top: 7px;
}

.color-grid label {
  align-items: center;
  display: flex;
  gap: 8px;
  justify-content: space-between;
}

.form-control-color {
  min-width: 44px;
  padding: 2px;
}

.json-editor {
  border: 1px solid var(--color-border);
  background: var(--color-surface-raised);
  color: var(--color-text);
  border-radius: var(--radius-sm);
  display: block;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  height: 300px;
  margin-bottom: 8px;
  padding: 10px;
  resize: vertical;
  width: 100%;
}

.config-dirty {
  align-items: center;
  color: var(--color-warning);
  display: inline-flex;
  font-size: 11px;
  gap: 5px;
  margin-right: auto;
}

.config-dirty .bi {
  font-size: 7px;
}

.model-list-field {
  align-items: start;
}

.model-list {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  min-height: 92px;
  resize: vertical;
}

@media (max-width: 640px) {
  .config-main {
    grid-template-columns: 1fr;
  }

  .config-nav {
    border-bottom: 1px solid var(--color-border);
    border-right: 0;
    flex-direction: row;
    min-height: auto;
    overflow-x: auto;
    padding: 7px;
  }

  .config-search {
    display: none;
  }

  .config-nav-item {
    flex: 0 0 auto;
  }

  .config-header,
  .config-footer {
    gap: 8px;
    padding: 8px;
  }

  .config-header h2 {
    font-size: 14px;
  }

  .config-footer {
    flex-wrap: wrap;
  }

  .config-error {
    flex: 1 0 100%;
  }

  .section-heading {
    padding: 9px 10px;
  }

  .section-body {
    padding: 0 10px 10px;
  }

  .setting-row,
  .compact-field {
    grid-template-columns: 1fr;
  }

  .setting-row {
    align-items: stretch;
  }

  .number-input,
  .setting-row .form-control,
  .setting-row .form-select,
  .compact-field .form-control,
  .compact-field .form-select {
    width: 100%;
  }

  .theme-toolbar {
    grid-template-columns: 1fr;
  }

  .style-card {
    grid-template-columns: 1fr;
  }

  .color-grid label {
    align-items: stretch;
    flex-direction: column;
  }

  .form-control-color {
    width: 100%;
  }
}
</style>
