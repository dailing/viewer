<script setup lang="ts">
import { computed, ref } from "vue";
import type { Decision, DecisionOption } from "./types";
import {
  decisionResponseIntent,
  decisionVoiceContextId,
  decisionVoiceTextForSend,
  type DecisionResponseIntent,
} from "./decisionInput";
import VoiceInputButton from "../voice/VoiceInputButton.vue";
import { useVoiceStore } from "../voice/voiceStore";

const props = defineProps<{ decision: Decision; draft: string; busy?: boolean }>();
const emit = defineEmits<{
  "update:draft": [value: string];
  respond: [intent: DecisionResponseIntent];
  cancel: [];
  open: [];
}>();
const voice = useVoiceStore();
const responding = ref(false);
const responseError = ref("");
const voiceContextId = computed(() => decisionVoiceContextId(props.decision.id));
const title = computed(() => props.decision.question.trim() || props.decision.task_title || "本轮后续");
const interactionBusy = computed(() => props.busy === true || responding.value);
const draft = computed({
  get: () => props.draft,
  set: (value: string) => emit("update:draft", value),
});
const voiceError = computed(() => {
  if (responseError.value) return responseError.value;
  const state = voice.context(voiceContextId.value);
  return state.status === "error" ? state.error : "";
});
async function respond(option?: DecisionOption): Promise<void> {
  if (interactionBusy.value) return;
  const target = { id: props.decision.id, version: props.decision.version };
  responding.value = true;
  responseError.value = "";
  try {
    let text = props.draft;
    if (option) {
      if (voice.isBusy(voiceContextId.value)) await voice.cancel(voiceContextId.value);
    } else {
      text = await decisionVoiceTextForSend(
        props.draft,
        voice.context(voiceContextId.value),
        () => voice.finishForSend(voiceContextId.value),
      );
      if (text !== props.draft) emit("update:draft", text);
    }
    const intent = decisionResponseIntent(target, text, option);
    if (intent) emit("respond", intent);
  } catch (cause) {
    responseError.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    responding.value = false;
  }
}
async function cancel(): Promise<void> {
  if (interactionBusy.value) return;
  responding.value = true;
  responseError.value = "";
  try {
    await voice.cancel(voiceContextId.value);
    emit("cancel");
  } catch (cause) {
    responseError.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    responding.value = false;
  }
}
function dismissVoiceError(): void {
  responseError.value = "";
  voice.dismissError(voiceContextId.value);
}
</script>

<template>
  <section class="decision-card" :class="[decision.tone, decision.status]">
    <div class="decision-heading"><strong>{{ title }}</strong><span class="decision-status">{{ decision.status }}</span></div>
    <div class="decision-source">{{ decision.chat_name || decision.chat_id }} / {{ decision.task_title || decision.task_id }} · {{ decision.role_name || decision.role_id }}</div>
    <p v-if="decision.parse_warning" class="parse-warning">{{ decision.parse_warning }}</p>
    <template v-if="decision.status === 'pending'">
      <div class="decision-options">
        <span v-for="option in decision.options" :key="option.id" class="decision-option"><button type="button" :class="{ 'task-end-option': option.action === 'end_current_task' }" :title="option.action === 'end_current_task' ? '结束当前 Task，不再发送给 Agent' : undefined" :disabled="interactionBusy" @click="respond(option)"><i v-if="option.action === 'end_current_task'" class="bi bi-check2-circle" /> {{ option.label }}</button><button class="edit-option" type="button" title="修改选项后回复" :disabled="interactionBusy" @click="emit('update:draft', option.label)"><i class="bi bi-pencil" /></button></span>
      </div>
      <div v-if="voiceError" class="decision-voice-error"><i class="bi bi-exclamation-triangle" /><span :title="voiceError">{{ voiceError }}</span><button type="button" title="关闭语音错误" @click="dismissVoiceError"><i class="bi bi-x" /></button></div>
      <div class="decision-free"><input v-model="draft" :disabled="interactionBusy" placeholder="自由回复" @keyup.enter="respond()"><VoiceInputButton v-model="draft" :context-id="voiceContextId" :disabled="interactionBusy" @start="responseError = ''" /><button type="button" :disabled="interactionBusy" @click="respond()">发送</button><button type="button" :disabled="interactionBusy" @click="cancel">取消</button></div>
    </template>
    <details v-if="decision.status !== 'pending'" class="decision-details"><summary>来源与处理结果</summary><p v-if="decision.response">已回复：{{ decision.response }}</p><p v-if="decision.resolved_dispatch_id">Dispatch：{{ decision.resolved_dispatch_id }}</p></details>
    <button class="source-link" type="button" @click="emit('open')">定位来源</button>
  </section>
</template>

<style scoped>
.decision-card{border-left:2px solid var(--color-border);display:flex;flex-direction:column;gap:5px;margin:6px 0;padding:5px 8px}.decision-card.warning{border-left-color:var(--bs-warning)}.decision-heading{align-items:center;display:flex;gap:8px}.decision-heading strong{font-size:var(--font-size-ui)}.decision-status,.decision-source{color:var(--color-text-muted);font-size:var(--font-size-ui-small)}.decision-status{margin-left:auto}.decision-source{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.parse-warning{color:var(--bs-warning);font-size:var(--font-size-ui-small);margin:0}.decision-options{display:flex;flex-wrap:wrap;gap:5px}.decision-option{display:inline-flex;gap:1px}.decision-card button{background:transparent;border:1px solid var(--color-border);border-radius:var(--radius-sm);color:var(--color-text);cursor:pointer;font-size:var(--font-size-ui-small);padding:3px 6px}.decision-card button:hover{background:var(--color-surface-hover)}.decision-card button:disabled{opacity:.45}.decision-card .edit-option{border-left:0;padding:3px 4px}.decision-card .task-end-option{border-color:var(--color-danger);color:var(--color-danger)}.decision-free{display:flex;gap:5px}.decision-free input{background:var(--color-surface);border:1px solid var(--color-border);color:var(--color-text);flex:1;min-width:0;padding:4px 6px}.decision-details{color:var(--color-text-muted);font-size:var(--font-size-ui-small)}.decision-details p{margin:3px 0}.decision-card .source-link{align-self:flex-start;border:0;color:var(--color-text-muted);padding:0}
.decision-voice-error{align-items:center;color:var(--bs-danger-text-emphasis,var(--bs-danger));display:flex;font-size:var(--font-size-ui-small);gap:5px}.decision-voice-error span{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.decision-card .decision-voice-error button{border:0;color:inherit;padding:0 3px}
.decision-card.pending{border-left-color:var(--bs-primary)}.decision-card.dispatching{border-left-color:var(--bs-warning)}.decision-card.resolved{border-left-color:var(--bs-success)}.decision-card.cancelled{border-left-color:var(--color-text-muted);opacity:.78}.decision-card.invalid{border-left-color:var(--bs-danger)}
</style>
