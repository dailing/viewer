<script setup lang="ts">
import { computed, inject, ref } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import DecisionCard from "../chat/DecisionCard.vue";
import TurnContent from "../chat/TurnContent.vue";
import type { DecisionResponseIntent } from "../chat/decisionInput";
import type { ChatMessage, Decision } from "../chat/types";
import { errorText } from "../chat/types";
import { decisionContextItems, decisionStatusIcon, decisionStatusLabel } from "./decisionPresentation";

const props = defineProps<{ decision: Decision; draft: string; busy?: boolean }>();
const emit = defineEmits<{
  "update:draft": [value: string];
  respond: [intent: DecisionResponseIntent];
  cancel: [];
  open: [];
}>();
const injected = inject<PluginCtx>("pluginCtx");
if (!injected) throw new Error("DecisionEntry requires PluginPaneHost");
const ctx = injected;
const messages = ref<ChatMessage[]>([]);
const loaded = ref(false);
const loading = ref(false);
const error = ref("");
const isHistory = computed(() => ["resolved", "cancelled", "invalid"].includes(props.decision.status));
const items = computed(() => decisionContextItems(messages.value, props.decision));
const source = computed(() => `${props.decision.chat_name || props.decision.chat_id} / ${props.decision.task_title || props.decision.task_id}`);
const statusLabel = computed(() => decisionStatusLabel(props.decision));
const statusIcon = computed(() => decisionStatusIcon(props.decision));

async function toggle(event: Event): Promise<void> {
  const details = event.currentTarget as HTMLDetailsElement;
  if (!details.open || loaded.value || loading.value) return;
  loading.value = true; error.value = "";
  try {
    const result = await ctx.bus.request("chat:_:decisions:context", { decision_id: props.decision.id }) as { messages: ChatMessage[] };
    messages.value = result.messages ?? [];
    loaded.value = true;
  } catch (cause) {
    error.value = errorText(cause);
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <article class="decision-entry" :class="{ historical: isHistory }">
    <template v-if="!isHistory">
      <details class="decision-context" @toggle="toggle">
        <summary><i class="bi bi-chevron-right" /> 查看这一轮的消息</summary>
        <div class="decision-context-body">
          <div v-if="loading" class="decision-context-note">加载中…</div>
          <div v-else-if="error" class="decision-context-error">{{ error }}</div>
          <TurnContent v-else-if="loaded && items.length > 0" :items="items" :show-activities="false" show-message-labels />
          <div v-else-if="loaded" class="decision-context-note">这一轮没有可见消息。</div>
        </div>
      </details>
      <DecisionCard :decision="decision" :draft="draft" :busy="busy" @update:draft="emit('update:draft', $event)" @respond="emit('respond', $event)" @cancel="emit('cancel')" @open="emit('open')" />
    </template>

    <details v-else class="decision-history" :class="decision.status" @toggle="toggle">
      <summary class="decision-history-summary">
        <i class="bi history-status-icon" :class="statusIcon" />
        <span class="history-status">{{ statusLabel }}</span>
        <strong class="history-question">{{ decision.question || '需要你的决定' }}</strong>
        <span class="history-source">{{ source }}</span>
        <i class="bi bi-chevron-right history-chevron" />
      </summary>
      <div class="decision-history-body">
        <div class="decision-history-meta">
          <span>{{ source }} · {{ decision.role_name || decision.role_id }}</span>
          <button type="button" @click="emit('open')">定位来源</button>
        </div>
        <p v-if="decision.parse_warning" class="decision-history-warning">{{ decision.parse_warning }}</p>
        <p v-if="decision.response" class="decision-history-response">已回复：{{ decision.response }}</p>
        <p v-if="decision.resolved_dispatch_id" class="decision-history-dispatch">Dispatch：{{ decision.resolved_dispatch_id }}</p>
        <div class="decision-context-body">
          <div v-if="loading" class="decision-context-note">加载中…</div>
          <div v-else-if="error" class="decision-context-error">{{ error }}</div>
          <TurnContent v-else-if="loaded && items.length > 0" :items="items" :show-activities="false" show-message-labels />
          <div v-else-if="loaded" class="decision-context-note">这一轮没有可见消息。</div>
        </div>
      </div>
    </details>
  </article>
</template>

<style scoped>
.decision-entry { border-bottom: 1px solid color-mix(in srgb, var(--color-border) 58%, transparent); padding: 2px 0 7px; }
.decision-entry:last-child { border-bottom: 0; }
.decision-entry.historical { padding: 0; }
.decision-context { margin: 0 8px; }
.decision-context summary { align-items: center; color: var(--color-text-muted); cursor: pointer; display: flex; font-size: var(--font-size-ui-small); gap: 4px; list-style: none; padding: 3px 0; user-select: none; }
.decision-context summary::-webkit-details-marker,.decision-history-summary::-webkit-details-marker { display: none; }
.decision-context[open] summary .bi-chevron-right { transform: rotate(90deg); }
.decision-context summary .bi-chevron-right,.history-chevron { transition: transform 120ms ease; }
.decision-context-body { border-left: 1px solid var(--color-border); margin: 2px 0 4px 4px; padding: 2px 10px; }
.decision-context-note { color: var(--color-text-muted); font-size: var(--font-size-ui-small); padding: 8px 0; }
.decision-context-error { color: var(--bs-danger); font-size: var(--font-size-ui-small); padding: 8px 0; }
.decision-history { min-width: 0; }
.decision-history-summary { align-items: center; color: var(--color-text-muted); cursor: pointer; display: grid; font-size: var(--font-size-ui-small); gap: 6px; grid-template-columns: auto auto minmax(0, 1fr) minmax(0, 1fr) auto; list-style: none; min-height: 28px; padding: 3px 6px; user-select: none; }
.decision-history-summary:hover { background: var(--color-surface-hover); }
.history-status { min-width: 48px; }
.history-question { color: var(--color-text); font-size: var(--font-size-ui-small); font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.history-source { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.decision-history.resolved .history-status-icon,.decision-history.resolved .history-status { color: var(--bs-success); }
.decision-history.invalid .history-status-icon,.decision-history.invalid .history-status { color: var(--bs-danger); }
.decision-history[open] .history-chevron { transform: rotate(90deg); }
.decision-history-body { border-left: 1px solid var(--color-border); margin: 0 6px 6px 12px; padding: 2px 8px; }
.decision-history-meta { align-items: center; color: var(--color-text-muted); display: flex; font-size: var(--font-size-ui-small); gap: 8px; justify-content: space-between; }
.decision-history-meta span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.decision-history-meta button { background: transparent; border: 0; color: var(--color-text-muted); cursor: pointer; flex: 0 0 auto; font-size: var(--font-size-ui-small); padding: 3px 5px; }
.decision-history-meta button:hover { background: var(--color-surface-hover); color: var(--color-text); }
.decision-history-warning { color: var(--bs-warning); font-size: var(--font-size-ui-small); margin: 4px 0; }
.decision-history-response,.decision-history-dispatch { font-size: var(--font-size-ui-small); margin: 4px 0; overflow-wrap: anywhere; }
.decision-history-dispatch { color: var(--color-text-muted); }
@media (max-width: 700px) {
  .decision-history-summary { grid-template-columns: auto auto minmax(0, 1fr) auto; }
  .history-source { display: none; }
}
</style>
