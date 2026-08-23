<script setup lang="ts">
/**
 * 语言模型 pane: the global llm plugin's settings (framework: global LLM
 * layer). Every plugin's LLM usage (chat dispatch/summary, voice control,
 * …) goes through `llm:_:complete`, which re-reads the active config on
 * every call — 启用/保存 take effect immediately, no restart.
 *
 * Storage (config-store, plugin `plugins.llm`):
 * - `profiles`: array of saved configs {id, name, endpoint, key, model,
 *   timeout_seconds} — purely a frontend-managed library.
 * - `active`: the ACTIVE config {endpoint, key, model, timeout_seconds}.
 * - `http`: facade config {enabled, port, expose}.
 *
 * The endpoint may be the base `/v1` URL or the full `/v1/chat/completions`
 * URL (the llm plugin appends it); any OpenAI-compatible endpoint works.
 * timeout_seconds bounds one completion call (default 60): local servers
 * with few parallel slots queue under load, so it must cover queueing, not
 * just generation.
 */
import { computed, inject, onBeforeUnmount, onMounted, reactive, ref } from "vue";

import type { PluginCtx } from "../../shell/ctx";
import MasterDetail from "../chat-manager/MasterDetail.vue";

const injectedCtx = inject<PluginCtx>("pluginCtx");
if (injectedCtx === undefined) throw new Error("LLMPane requires PluginPaneHost");
const ctx: PluginCtx = injectedCtx;

interface LlmProfile {
  id: string;
  name: string;
  endpoint: string;
  key: string;
  model: string;
  timeout_seconds?: number;
}

interface LlmActive {
  endpoint?: string;
  key?: string;
  model?: string;
  timeout_seconds?: number;
}

interface LlmHttpStatus {
  enabled: boolean;
  running: boolean;
  host: string;
  port: number;
  base_url?: string;
  expose: boolean;
  last_error?: string;
}

/** Seeded only when `profiles` has never been stored; deleting all profiles
 *  is respected (an empty array is not re-seeded). */
const DEFAULT_PROFILES: LlmProfile[] = [
  {
    id: "llama-server",
    name: "llama-server（本机）",
    endpoint: "http://127.0.0.1:18090/v1",
    key: "",
    model: "qwen3.8-27b",
  },
  {
    id: "ollama",
    name: "Ollama（本机）",
    endpoint: "http://127.0.0.1:11434/v1",
    key: "",
    model: "qwen3.8:27b",
  },
];

const profiles = ref<LlmProfile[]>([]);
const active = ref<LlmActive>({});
const selectedId = ref("");
const form = reactive({ name: "", endpoint: "", model: "", key: "", timeout: "" });
const saving = ref(false);
const status = ref("");
const statusError = ref(false);
const httpEnabled = ref(false);
const httpExpose = ref(false);
const httpPort = ref("18731");
const httpSaving = ref(false);
const httpStatus = ref<LlmHttpStatus | null>(null);
const httpMessage = ref("");
const httpMessageError = ref(false);
let httpStatusTimer: number | undefined;

function setStatus(text: string, isError = false): void {
  status.value = text;
  statusError.value = isError;
}

/** Normalize for active matching: base `/v1` and full chat-completions URL are equivalent. */
function normalizeEndpoint(value: string): string {
  let result = value.trim().replace(/\/+$/, "");
  if (result.endsWith("/chat/completions")) result = result.slice(0, -"/chat/completions".length);
  return result;
}

function profileMatchesActive(profile: LlmProfile): boolean {
  return (
    normalizeEndpoint(profile.endpoint) === normalizeEndpoint(active.value.endpoint ?? "") &&
    profile.model === (active.value.model ?? "") &&
    normalizeEndpoint(profile.endpoint) !== ""
  );
}

const activeProfileId = computed(() => profiles.value.find(profileMatchesActive)?.id ?? "");
const items = computed(() =>
  profiles.value.map((profile) => ({
    id: profile.id,
    name: profile.id === activeProfileId.value ? `● ${profile.name}` : profile.name,
  })),
);

async function persistProfiles(): Promise<void> {
  await ctx.bus.request("config:_:set", {
    plugin: "plugins.llm",
    key: "profiles",
    value: profiles.value,
  });
}

function select(id: string): void {
  const profile = profiles.value.find((item) => item.id === id);
  if (profile === undefined) return;
  selectedId.value = profile.id;
  form.name = profile.name;
  form.endpoint = profile.endpoint;
  form.model = profile.model;
  form.key = profile.key;
  form.timeout = profile.timeout_seconds === undefined ? "" : String(profile.timeout_seconds);
  setStatus("");
}

async function load(): Promise<void> {
  try {
    const [llm, stored, httpConfig] = await Promise.all([
      ctx.bus.request("config:_:get", { plugin: "plugins.llm", key: "active" }) as Promise<LlmActive | null>,
      ctx.bus.request("config:_:get", { plugin: "plugins.llm", key: "profiles" }) as Promise<LlmProfile[] | null>,
      ctx.bus.request("config:_:get", { plugin: "plugins.llm", key: "http" }) as Promise<{ enabled?: boolean; port?: number; expose?: boolean } | null>,
    ]);
    active.value = llm ?? {};
    if (stored === null) {
      profiles.value = DEFAULT_PROFILES.map((profile) => ({ ...profile }));
      await persistProfiles();
    } else {
      profiles.value = stored;
    }
    httpEnabled.value = httpConfig?.enabled ?? false;
    httpExpose.value = httpConfig?.expose ?? false;
    httpPort.value = String(httpConfig?.port ?? 18731);
    await refreshHttpStatus();
    // Preselect the active profile when there is one, else the first.
    select(activeProfileId.value || profiles.value[0]?.id || "");
  } catch (error) {
    setStatus(`读取失败：${String(error)}`, true);
  }
}

async function refreshHttpStatus(): Promise<void> {
  try {
    httpStatus.value = await ctx.bus.request("llm:_:http:status", {}) as LlmHttpStatus;
  } catch (error) {
    httpMessage.value = `读取 HTTP 状态失败：${String(error)}`;
    httpMessageError.value = true;
  }
}

async function saveHttpConfig(): Promise<void> {
  if (httpSaving.value) return;
  const port = Number(httpPort.value);
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    httpMessage.value = "端口必须是 1–65535 之间的整数。";
    httpMessageError.value = true;
    return;
  }
  httpSaving.value = true;
  httpMessage.value = "";
  httpMessageError.value = false;
  try {
    httpStatus.value = await ctx.bus.request("llm:_:http:configure", {
      enabled: httpEnabled.value,
      port,
      expose: httpExpose.value,
    }) as LlmHttpStatus;
    httpMessage.value = httpStatus.value.running ? "HTTP 服务已生效" : "HTTP 服务已关闭";
  } catch (error) {
    httpMessage.value = `保存失败：${String(error)}`;
    httpMessageError.value = true;
    await refreshHttpStatus();
  } finally {
    httpSaving.value = false;
  }
}

async function createProfile(): Promise<void> {
  const profile: LlmProfile = {
    id: `p-${crypto.randomUUID().slice(0, 8)}`,
    name: "新配置",
    endpoint: "",
    key: "",
    model: "",
  };
  profiles.value.push(profile);
  try {
    await persistProfiles();
    select(profile.id);
  } catch (error) {
    setStatus(`新建失败：${String(error)}`, true);
  }
}

function formTimeoutSeconds(): number | undefined {
  const parsed = Number(form.timeout);
  return form.timeout.trim() !== "" && Number.isFinite(parsed) && parsed > 0 ? Math.floor(parsed) : undefined;
}

async function saveProfile(): Promise<void> {
  if (saving.value) return;
  const profile = profiles.value.find((item) => item.id === selectedId.value);
  if (profile === undefined) return;
  const endpoint = form.endpoint.trim();
  const model = form.model.trim();
  const name = form.name.trim() || profile.name;
  if (endpoint === "" || model === "") {
    setStatus("接口地址和模型不能为空。", true);
    return;
  }
  saving.value = true;
  setStatus("");
  Object.assign(profile, { name, endpoint, model, key: form.key.trim(), timeout_seconds: formTimeoutSeconds() });
  try {
    await persistProfiles();
    // Editing the config that is currently in use must take effect
    // immediately — the llm plugin re-reads `active` per call.
    if (profile.id === activeProfileId.value) await activate(profile);
    select(profile.id);
    setStatus("已保存");
  } catch (error) {
    setStatus(`保存失败：${String(error)}`, true);
  } finally {
    saving.value = false;
  }
}

async function activate(profile: LlmProfile): Promise<void> {
  const value: LlmActive = {
    endpoint: profile.endpoint.trim(),
    key: profile.key.trim(),
    model: profile.model.trim(),
    timeout_seconds: profile.timeout_seconds,
  };
  await ctx.bus.request("config:_:set", { plugin: "plugins.llm", key: "active", value });
  active.value = value;
}

async function activateSelected(): Promise<void> {
  const profile = profiles.value.find((item) => item.id === selectedId.value);
  if (profile === undefined) return;
  if (profile.endpoint.trim() === "" || profile.model.trim() === "") {
    setStatus("先保存：接口地址和模型不能为空。", true);
    return;
  }
  saving.value = true;
  setStatus("");
  try {
    await activate(profile);
    setStatus("已启用，所有插件下次调用即生效");
  } catch (error) {
    setStatus(`启用失败：${String(error)}`, true);
  } finally {
    saving.value = false;
  }
}

async function removeProfile(): Promise<void> {
  const profile = profiles.value.find((item) => item.id === selectedId.value);
  if (profile === undefined) return;
  if (!window.confirm(`删除配置「${profile.name}」？`)) return;
  profiles.value = profiles.value.filter((item) => item.id !== profile.id);
  try {
    await persistProfiles();
    select(activeProfileId.value || profiles.value[0]?.id || "");
    setStatus("已删除");
  } catch (error) {
    setStatus(`删除失败：${String(error)}`, true);
  }
}

onMounted(() => {
  void load();
  httpStatusTimer = window.setInterval(() => void refreshHttpStatus(), 5000);
});
onBeforeUnmount(() => {
  if (httpStatusTimer !== undefined) window.clearInterval(httpStatusTimer);
});
</script>

<template>
  <div class="http-facade border-bottom pb-3 mb-3">
    <div class="fw-semibold mb-2">统一 HTTP 接口</div>
    <div class="d-flex flex-wrap align-items-end gap-2">
      <label class="form-check form-switch mb-1">
        <input v-model="httpEnabled" class="form-check-input" type="checkbox">
        <span class="form-check-label small">启用本机 HTTP 服务</span>
      </label>
      <label class="form-label small mb-0">端口
        <input v-model="httpPort" type="number" min="1" max="65535" step="1" class="form-control form-control-sm mt-1 http-port">
      </label>
      <label class="form-check mb-1">
        <input v-model="httpExpose" class="form-check-input" type="checkbox">
        <span class="form-check-label small">允许局域网和容器访问</span>
      </label>
      <button type="button" class="btn btn-sm btn-primary" :disabled="httpSaving" @click="saveHttpConfig">
        {{ httpSaving ? "应用中…" : "应用" }}
      </button>
      <span class="small" :class="httpMessageError ? 'text-danger' : 'text-secondary'">{{ httpMessage }}</span>
    </div>
    <div class="small mt-2" :class="httpStatus?.last_error ? 'text-danger' : 'text-secondary'">
      <template v-if="httpStatus?.running">
        运行中：<code>{{ httpStatus.base_url }}</code>（POST /chat/completions，GET /models）
      </template>
      <template v-else>当前未监听。</template>
      <template v-if="httpStatus?.last_error"> {{ httpStatus.last_error }}</template>
    </div>
    <div class="small text-secondary mt-1">
      默认仅监听 127.0.0.1；允许外部访问后绑定 0.0.0.0，Docker 内使用 <code>http://host.docker.internal:{{ httpStatus?.port ?? httpPort }}/v1</code>。调用方请求中的 model 会被当前启用模型覆盖，因此在这里切换一次，Viewer、Hindsight、infod、gaokao 和语音服务会一起切换。每次 HTTP 调用的原始参数、实际转发参数和响应都写入 Viewer 日志（API Key 除外）。
    </div>
  </div>
  <MasterDetail :items="items" :selected-id="selectedId" create-label="＋ 新建模型配置" @select="select" @create="createProfile">
    <template #detail>
      <div v-if="selectedId !== ''" class="llm-detail">
        <label class="form-label small w-100">名称
          <input v-model="form.name" type="text" class="form-control form-control-sm mt-1" placeholder="配置名称">
        </label>
        <label class="form-label small w-100">接口地址
          <input v-model="form.endpoint" type="text" class="form-control form-control-sm mt-1" placeholder="http://host:port/v1">
        </label>
        <label class="form-label small w-100">模型
          <input v-model="form.model" type="text" class="form-control form-control-sm mt-1" placeholder="model-name">
        </label>
        <label class="form-label small w-100">API Key（可空）
          <input v-model="form.key" type="password" class="form-control form-control-sm mt-1" placeholder="留空则不发送 Authorization">
        </label>
        <label class="form-label small w-100">超时（秒，可空）
          <input v-model="form.timeout" type="number" min="1" step="1" class="form-control form-control-sm mt-1" placeholder="默认 60；本地小并发服务建议 ≥60">
        </label>
        <div class="d-flex align-items-center gap-2 mt-1">
          <button
            type="button"
            class="btn btn-sm btn-primary"
            :disabled="saving"
            title="保存该配置；若它是当前使用的配置，同步立即生效"
            @click="saveProfile"
          >{{ saving ? "保存中…" : "保存" }}</button>
          <button
            type="button"
            class="btn btn-sm btn-outline-secondary"
            :disabled="saving || selectedId === activeProfileId"
            title="将该配置设为全局当前使用的模型（plugins.llm.active），立即生效"
            @click="activateSelected"
          >设为当前</button>
          <button
            type="button"
            class="btn btn-sm btn-outline-danger"
            :disabled="saving"
            title="删除该配置（不影响正在使用的 active 配置本身）"
            @click="removeProfile"
          >删除</button>
          <span class="small" :class="statusError ? 'text-danger' : 'text-secondary'">{{ status }}</span>
        </div>
        <div class="small text-secondary mt-2">
          左侧 ● 为当前使用中的配置。这是全局语言模型转发层：所有插件（dispatch、摘要、语音控制）都经由 llm 插件调用它。配置保存在后端 config-store（plugins.llm.profiles / active），启用后立即生效，无需重启。自定义接口需兼容 OpenAI /chat/completions。
        </div>
      </div>
      <div v-else class="small text-secondary">暂无配置，点击左下角「＋ 新建模型配置」。</div>
    </template>
  </MasterDetail>
</template>

<style scoped>
.llm-detail {
  max-width: 34rem;
}
.http-facade {
  max-width: 52rem;
}
.http-port {
  width: 8rem;
}
</style>
