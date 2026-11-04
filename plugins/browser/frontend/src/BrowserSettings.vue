<script setup lang="ts">
import { inject, onMounted, ref } from "vue";
import type { PluginCtx } from "@viewer/bus-sdk";

interface MCPStatus {
  enabled: boolean;
  running: boolean;
  host: string;
  port: number;
  url?: string;
  last_error?: string;
}

const injected = inject<PluginCtx>("pluginCtx");
if (injected === undefined) throw new Error("BrowserSettings requires plugin ctx");
const ctx: PluginCtx = injected;
const enabled = ref(true);
const port = ref(18732);
const status = ref<MCPStatus | null>(null);
const busy = ref(false);
const message = ref("");
const failed = ref(false);

function applyStatus(value: MCPStatus): void {
  status.value = value;
  enabled.value = value.enabled;
  port.value = value.port;
}

async function refresh(): Promise<void> {
  try {
    applyStatus(await ctx.bus.request<MCPStatus>("viewer.browser:_:mcp_status", {}));
  } catch (cause) {
    failed.value = true;
    message.value = cause instanceof Error ? cause.message : String(cause);
  }
}

async function save(): Promise<void> {
  busy.value = true;
  failed.value = false;
  message.value = "";
  try {
    applyStatus(await ctx.bus.request<MCPStatus>("viewer.browser:_:mcp_configure", {
      enabled: enabled.value,
      port: Number(port.value),
    }));
    message.value = "已应用，无需重启 Browser。";
  } catch (cause) {
    failed.value = true;
    message.value = cause instanceof Error ? cause.message : String(cause);
    await refresh();
  } finally {
    busy.value = false;
  }
}

onMounted(() => void refresh());
</script>

<template>
  <div class="browser-mcp-settings">
    <div class="d-flex flex-wrap align-items-end gap-2">
      <label class="form-check form-switch mb-1">
        <input v-model="enabled" class="form-check-input" type="checkbox">
        <span class="form-check-label small">启用 Agent MCP</span>
      </label>
      <label class="form-label small mb-0">端口
        <input v-model.number="port" type="number" min="1" max="65535" step="1" class="form-control form-control-sm mt-1 mcp-port">
      </label>
      <button type="button" class="btn btn-sm btn-primary" :disabled="busy" @click="save">
        {{ busy ? "应用中…" : "应用" }}
      </button>
      <span class="small" :class="failed ? 'text-danger' : 'text-secondary'">{{ message }}</span>
    </div>
    <div class="small mt-2" :class="status?.last_error ? 'text-danger' : 'text-secondary'">
      <template v-if="status?.running">运行中：<code>{{ status.url }}</code></template>
      <template v-else>当前未监听。</template>
      <template v-if="status?.last_error"> {{ status.last_error }}</template>
    </div>
    <div class="small text-secondary mt-1">
      仅监听 127.0.0.1。启停或换端口只影响 MCP，不重启 Chrome、页面或视频流；已有 Agent 的 MCP 客户端配置仍需指向这里显示的地址。
    </div>
  </div>
</template>

<style scoped>
.browser-mcp-settings {
  max-width: 52rem;
}
.mcp-port {
  width: 8rem;
}
</style>
