<script setup lang="ts">
import { computed, inject, ref } from "vue";
import type { PluginCtx } from "../../shell/ctx";
import DecisionCard from "../chat/DecisionCard.vue";
import TurnContent from "../chat/TurnContent.vue";
import type { ChatTimelineItem } from "../chat/turnContent";
import type { DecisionResponseIntent } from "../chat/decisionInput";
import type { ChatMessage, Decision } from "../chat/types";
import { errorText } from "../chat/types";

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
const items = computed<ChatTimelineItem[]>(() => messages.value.map((message) => ({ id: message.id, at: message.created_at, kind: "message", message })));

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
  <article class="decision-entry">
    <DecisionCard :decision="decision" :draft="draft" :busy="busy" @update:draft="emit('update:draft', $event)" @respond="emit('respond', $event)" @cancel="emit('cancel')" @open="emit('open')" />
    <details class="decision-context" @toggle="toggle">
      <summary><i class="bi bi-chevron-right" /> 查看这一轮的消息</summary>
      <div class="decision-context-body">
        <div v-if="loading" class="decision-context-note">加载中…</div>
        <div v-else-if="error" class="decision-context-error">{{ error }}</div>
        <TurnContent v-else-if="loaded && items.length > 0" :items="items" :decisions="[decision]" :show-activities="false" show-message-labels />
        <div v-else-if="loaded" class="decision-context-note">这一轮没有可见消息。</div>
      </div>
    </details>
  </article>
</template>

<style scoped>
.decision-entry { border-bottom: 1px solid color-mix(in srgb, var(--color-border) 58%, transparent); padding: 2px 0 7px; }
.decision-entry:last-child { border-bottom: 0; }
.decision-context { margin: 0 8px; }
.decision-context summary { align-items: center; color: var(--color-text-muted); cursor: pointer; display: flex; font-size: var(--font-size-ui-small); gap: 4px; list-style: none; padding: 3px 0; user-select: none; }
.decision-context summary::-webkit-details-marker { display: none; }
.decision-context[open] summary .bi-chevron-right { transform: rotate(90deg); }
.decision-context summary .bi-chevron-right { transition: transform 120ms ease; }
.decision-context-body { border-left: 1px solid var(--color-border); margin: 2px 0 4px 4px; max-height: 60vh; overflow: auto; padding: 2px 10px; }
.decision-context-note { color: var(--color-text-muted); font-size: var(--font-size-ui-small); padding: 8px 0; }
.decision-context-error { color: var(--bs-danger); font-size: var(--font-size-ui-small); padding: 8px 0; }
</style>
