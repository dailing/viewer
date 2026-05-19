<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { restartServer, stopServer } from "../api/client";
import { DEFAULT_CODEX_CONFIG, DEFAULT_MARKDOWN_THEME, useFilesStore } from "../stores/files";
import { useUsersStore } from "../stores/users";
import { useWorkspacesStore } from "../stores/workspaces";
import type { AppearanceConfig, CodexConfig, DagConfig, MarkdownConfig, MarkdownElementStyle, MarkdownTheme, WorkspaceConfig } from "../types/files";

const emit = defineEmits<{ close: [] }>();
const files = useFilesStore();
const users = useUsersStore();
const workspaces = useWorkspacesStore();
const saving = ref(false);
const restarting = ref(false);
const stopping = ref(false);
const error = ref("");
const serverNotice = ref("");
const openSections = reactive({ server: true, users: true, appearance: true, workspace: true, codex: true, dag: true, markdown: true, syntax: false, json: false });
const jsonDraft = ref("");
const draft = reactive({
  appearance: clone(files.appearance) as AppearanceConfig,
  workspace: clone(files.workspaceConfig) as WorkspaceConfig,
  codex: clone(files.codexConfig) as CodexConfig,
  dag: clone(files.dagConfig) as DagConfig,
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

const fullConfigJson = computed(() =>
  JSON.stringify(
    {
      appearance: draft.appearance,
      workspace: draft.workspace,
      codex: draft.codex,
      dag: draft.dag,
      markdown: draft.markdown,
    },
    null,
    2,
  ),
);

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
  () => files.dagConfig,
  (dag) => {
    Object.assign(draft.dag, clone(dag));
    jsonDraft.value = fullConfigJson.value;
  },
  { deep: true },
);

watch(
  () => files.workspaceConfig,
  (workspace) => {
    Object.assign(draft.workspace, clone(workspace));
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
  if (!openSections.json) jsonDraft.value = value;
});

jsonDraft.value = fullConfigJson.value;

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function sectionToggle(section: keyof typeof openSections) {
  openSections[section] = !openSections[section];
  if (section === "json" && openSections.json) jsonDraft.value = fullConfigJson.value;
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
  restarting.value = false;
}

async function restart() {
  if (restarting.value) return;
  if (!window.confirm("Restart the viewer server now?")) return;
  restarting.value = true;
  error.value = "";
  serverNotice.value = "";
  try {
    const response = await restartServer();
    await waitForServer(response.pid);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
    restarting.value = false;
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

function setDefaultModel(value: string) {
  const model = value.trim();
  draft.codex.default_model = model;
  if (model && !draft.codex.available_models.includes(model)) {
    draft.codex.available_models = [model, ...draft.codex.available_models];
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

async function save() {
  saving.value = true;
  error.value = "";
  try {
    await files.saveFullViewerConfig(draft.appearance, draft.markdown, draft.codex, draft.workspace, draft.dag);
    await workspaces.load();
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
    if (parsed.workspace) Object.assign(draft.workspace, parsed.workspace);
    if (parsed.codex) Object.assign(draft.codex, parsed.codex);
    if (parsed.dag) Object.assign(draft.dag, parsed.dag);
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
          <span>~/.view/config.json</span>
        </div>
        <button class="btn btn-outline-secondary icon-button" type="button" title="Close configuration" @click="emit('close')">
          <i class="bi bi-x"></i>
        </button>
      </header>

      <div class="config-content">
        <section class="config-section">
          <button class="section-toggle" type="button" @click="sectionToggle('server')">
            <i class="bi" :class="openSections.server ? 'bi-chevron-down' : 'bi-chevron-right'"></i>
            <span>Server</span>
          </button>
          <div v-if="openSections.server" class="section-body">
            <div class="server-actions">
              <button class="btn btn-sm btn-outline-danger" type="button" :disabled="restarting || stopping" @click="restart">
                <span v-if="restarting" class="spinner-border spinner-border-sm"></span>
                <i v-else class="bi bi-arrow-clockwise"></i>
                <span>{{ restarting ? "Restarting" : "Restart server" }}</span>
              </button>
              <button class="btn btn-sm btn-outline-danger" type="button" :disabled="stopping || restarting" @click="stop">
                <span v-if="stopping" class="spinner-border spinner-border-sm"></span>
                <i v-else class="bi bi-stop-fill"></i>
                <span>{{ stopping ? "Stopping" : "Stop server" }}</span>
              </button>
            </div>
            <div v-if="serverNotice" class="server-notice">{{ serverNotice }}</div>
          </div>
        </section>

        <section class="config-section">
          <button class="section-toggle" type="button" @click="sectionToggle('users')">
            <i class="bi" :class="openSections.users ? 'bi-chevron-down' : 'bi-chevron-right'"></i>
            <span>User Profile</span>
          </button>
          <div v-if="openSections.users" class="section-body">
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

        <section class="config-section">
          <button class="section-toggle" type="button" @click="sectionToggle('appearance')">
            <i class="bi" :class="openSections.appearance ? 'bi-chevron-down' : 'bi-chevron-right'"></i>
            <span>Appearance</span>
          </button>
          <div v-if="openSections.appearance" class="section-body">
            <label class="setting-row">
              <span>Nav bar size</span>
              <input v-model.number="draft.appearance.navbar_size" class="form-range" type="range" min="22" max="56" step="1" />
              <input v-model.number="draft.appearance.navbar_size" class="form-control form-control-sm number-input" type="number" min="22" max="56" />
            </label>
          </div>
        </section>

        <section class="config-section">
          <button class="section-toggle" type="button" @click="sectionToggle('workspace')">
            <i class="bi" :class="openSections.workspace ? 'bi-chevron-down' : 'bi-chevron-right'"></i>
            <span>Workspace</span>
          </button>
          <div v-if="openSections.workspace" class="section-body">
            <label class="setting-row">
              <span>Workspace count</span>
              <input v-model.number="draft.workspace.count" class="form-range" type="range" min="1" max="20" step="1" />
              <input v-model.number="draft.workspace.count" class="form-control form-control-sm number-input" type="number" min="1" max="20" />
            </label>
            <label class="setting-row">
              <span>Heat interval</span>
              <input v-model.number="draft.workspace.heat_interval_seconds" class="form-range" type="range" min="1" max="300" step="1" />
              <input v-model.number="draft.workspace.heat_interval_seconds" class="form-control form-control-sm number-input" type="number" min="1" max="300" step="1" />
            </label>
            <label class="setting-row">
              <span>Heat step %</span>
              <input v-model.number="draft.workspace.heat_step_percent" class="form-range" type="range" min="0.1" max="100" step="0.1" />
              <input v-model.number="draft.workspace.heat_step_percent" class="form-control form-control-sm number-input" type="number" min="0.1" max="100" step="0.1" />
            </label>
          </div>
        </section>

        <section class="config-section">
          <button class="section-toggle" type="button" @click="sectionToggle('codex')">
            <i class="bi" :class="openSections.codex ? 'bi-chevron-down' : 'bi-chevron-right'"></i>
            <span>Codex Models</span>
          </button>
          <div v-if="openSections.codex" class="section-body">
            <label class="compact-field">
              <span>Default model</span>
              <input
                class="form-control form-control-sm"
                list="codex-model-options"
                :value="draft.codex.default_model"
                @input="setDefaultModel(($event.target as HTMLInputElement).value)"
              />
            </label>
            <datalist id="codex-model-options">
              <option v-for="model in draft.codex.available_models" :key="model" :value="model"></option>
            </datalist>
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
            <label class="compact-field model-list-field">
              <span>Auto commit prompt</span>
              <textarea
                v-model="draft.codex.auto_commit_prompt"
                class="form-control form-control-sm model-list"
                spellcheck="false"
              ></textarea>
            </label>
          </div>
        </section>

        <section class="config-section">
          <button class="section-toggle" type="button" @click="sectionToggle('dag')">
            <i class="bi" :class="openSections.dag ? 'bi-chevron-down' : 'bi-chevron-right'"></i>
            <span>Task DAG</span>
          </button>
          <div v-if="openSections.dag" class="section-body">
            <label class="compact-field">
              <span>API base URL</span>
              <input
                v-model.trim="draft.dag.base_url"
                class="form-control form-control-sm"
                placeholder="http://127.0.0.1:8000"
              />
            </label>
          </div>
        </section>

        <section class="config-section">
          <button class="section-toggle" type="button" @click="sectionToggle('markdown')">
            <i class="bi" :class="openSections.markdown ? 'bi-chevron-down' : 'bi-chevron-right'"></i>
            <span>Markdown</span>
          </button>
          <div v-if="openSections.markdown" class="section-body">
            <div class="theme-toolbar">
              <select v-model="draft.markdown.active_theme" class="form-select form-select-sm">
                <option v-for="theme in draft.markdown.themes" :key="theme.name" :value="theme.name">{{ theme.name }}</option>
              </select>
              <button class="btn btn-sm btn-outline-secondary" type="button" @click="duplicateTheme">
                <i class="bi bi-copy"></i>
              </button>
              <button class="btn btn-sm btn-outline-secondary" type="button" @click="resetActiveTheme">
                <i class="bi bi-arrow-counterclockwise"></i>
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
                  :value="(item[1] as MarkdownElementStyle).color || '#172033'"
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

        <section class="config-section">
          <button class="section-toggle" type="button" @click="sectionToggle('syntax')">
            <i class="bi" :class="openSections.syntax ? 'bi-chevron-down' : 'bi-chevron-right'"></i>
            <span>Syntax Highlighting</span>
          </button>
          <div v-if="openSections.syntax" class="section-body color-grid syntax-grid">
            <label v-for="key in Object.keys(activeTheme.syntax)" :key="key">
              <span>{{ key }}</span>
              <input v-model="activeTheme.syntax[key as keyof MarkdownTheme['syntax']]" class="form-control form-control-sm form-control-color" type="color" />
            </label>
          </div>
        </section>

        <section class="config-section">
          <button class="section-toggle" type="button" @click="sectionToggle('json')">
            <i class="bi" :class="openSections.json ? 'bi-chevron-down' : 'bi-chevron-right'"></i>
            <span>JSON</span>
          </button>
          <div v-if="openSections.json" class="section-body">
            <textarea v-model="jsonDraft" class="json-editor" spellcheck="false"></textarea>
            <button class="btn btn-sm btn-outline-secondary" type="button" @click="applyJson">Apply JSON</button>
          </div>
        </section>
      </div>

      <footer class="config-footer">
        <span v-if="error" class="config-error">{{ error }}</span>
        <button class="btn btn-sm btn-outline-secondary" type="button" @click="emit('close')">Cancel</button>
        <button class="btn btn-sm btn-primary" type="button" :disabled="saving" @click="save">
          <span v-if="saving" class="spinner-border spinner-border-sm"></span>
          <span v-else>Save</span>
        </button>
      </footer>
    </section>
  </div>
</template>

<style scoped>
.config-page {
  background: #f6f8fb;
  height: 100%;
  overflow: hidden;
}

.config-panel {
  background: #ffffff;
  border-left: 1px solid var(--border);
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  height: 100%;
  margin: 0 auto;
  max-width: 100vw;
  width: min(960px, 100vw);
}

.config-header,
.config-footer {
  align-items: center;
  border-bottom: 1px solid var(--border);
  display: flex;
  flex: 0 0 auto;
  gap: 10px;
  justify-content: space-between;
  padding: 10px 12px;
}

.config-footer {
  border-bottom: 0;
  border-top: 1px solid var(--border);
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
  color: var(--text-muted);
  font-size: 12px;
}

.config-error {
  color: #a33;
  margin-right: auto;
}

.config-content {
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
}

.config-section {
  border-bottom: 1px solid var(--border);
}

.section-toggle {
  align-items: center;
  background: #ffffff;
  border: 0;
  display: flex;
  font-size: 13px;
  font-weight: 700;
  gap: 8px;
  padding: 10px 12px;
  text-align: left;
  width: 100%;
}

.section-body {
  padding: 0 12px 12px;
}

.server-actions {
  align-items: center;
  display: flex;
  gap: 8px;
}

.server-actions .btn {
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
  gap: 10px;
  grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
  margin-top: 12px;
}

.style-card {
  border: 1px solid var(--border);
  border-radius: 8px;
  display: grid;
  gap: 6px;
  grid-template-columns: 62px 1fr;
  padding: 10px;
}

.style-card strong {
  grid-column: 1 / -1;
}

.style-card span,
.color-grid span,
.compact-field span,
.setting-row span {
  color: var(--text-muted);
  font-size: 12px;
}

.color-grid {
  display: grid;
  gap: 10px;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  margin-top: 12px;
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
  border: 1px solid var(--border);
  border-radius: 6px;
  display: block;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  height: 300px;
  margin-bottom: 8px;
  padding: 10px;
  resize: vertical;
  width: 100%;
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
  .setting-row,
  .compact-field {
    grid-template-columns: 1fr;
  }
}
</style>
