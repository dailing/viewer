<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { renderMarkdown, renderMermaidIn } from "../../utils/markdownRender";
import ToolActivity from "./ToolActivity.vue";
import { activityGroupSummary, groupActivityItems } from "./toolPresentation";
import { formatChatTime as formatTime } from "./chatTime";
import { visibleMessageText, visibleTurnItems, type ChatTimelineItem } from "./turnContent";
import type { Decision } from "./types";

const props = withDefaults(defineProps<{
  items: ChatTimelineItem[];
  decisions?: Decision[];
  showActivities?: boolean;
  showMessageLabels?: boolean;
}>(), { decisions: () => [], showActivities: true, showMessageLabels: false });
const root = ref<HTMLElement | null>(null);
const displayItems = computed(() => groupActivityItems(visibleTurnItems(props.items, props.showActivities)));
function markdown(text: string): string { return renderMarkdown(text); }
async function renderDiagrams(): Promise<void> {
  await nextTick();
  if (root.value) await renderMermaidIn(root.value);
}
onMounted(renderDiagrams);
watch(() => props.items, renderDiagrams);
</script>

<template>
  <div ref="root" class="turn-content">
    <template v-for="item in displayItems" :key="item.id">
      <div v-if="item.kind === 'message' && item.message" class="turn-message">
        <div v-if="showMessageLabels" class="turn-message-label">{{ item.message.role === 'user' ? 'User' : item.message.sender.role_name || 'Agent' }}</div>
        <div class="markdown-content" v-html="markdown(visibleMessageText(item.message, decisions))" />
      </div>
      <ToolActivity v-else-if="showActivities && item.kind === 'activity' && item.block" :block="item.block" :time="formatTime(item.at)" />
      <details v-else-if="showActivities && item.kind === 'activity-group'" class="chat-tool-group">
        <summary class="chat-tool-group-summary">
          <i class="bi bi-tools" aria-hidden="true" />
          <span class="chat-tool-group-label">Tools</span>
          <span class="chat-tool-group-text">{{ activityGroupSummary(item) }}</span>
          <span class="chat-tool-group-count">×{{ item.items.length }}</span>
          <span class="chat-tool-group-time">{{ formatTime(item.at) }}</span>
          <i class="bi bi-chevron-right chat-tool-group-chevron" aria-hidden="true" />
        </summary>
        <div class="chat-tool-group-body">
          <ToolActivity v-for="activity in item.items" :key="activity.id" :block="activity.block!" :time="formatTime(activity.at)" />
        </div>
      </details>
    </template>
  </div>
</template>

<style scoped>
.turn-content { min-width: 0; }
.markdown-content {
  --markdown-render-body-size: calc(var(--markdown-body-font-size) * 4 / 5);
  --markdown-render-paragraph-line-height: 1.48;
  --markdown-render-paragraph-size: calc(var(--markdown-body-font-size) * 4 / 5);
  --markdown-render-pre-padding: 8px;
  font-size: var(--markdown-render-body-size);
  line-height: var(--markdown-render-paragraph-line-height);
  max-width: 100%;
  min-width: 0;
  overflow-wrap: anywhere;
  user-select: text;
}
.markdown-content :deep(pre) { overflow: auto; }
.markdown-content :deep(p:last-child) { margin-bottom: 0; }
.turn-message-label { color: var(--color-text-muted); font-size: var(--font-size-ui-small); font-weight: 600; margin: 6px 0 2px; }
.chat-tool-group { color: color-mix(in srgb, var(--color-text-muted) 72%, transparent); min-width: 0; }
.chat-tool-group-summary { align-items: center; cursor: pointer; display: grid; font-size: calc(var(--font-size-ui) * 5 / 6); gap: 4px; grid-template-columns: auto auto minmax(0, 1fr) auto auto auto; line-height: 1.3; list-style: none; min-height: 16px; min-width: 0; padding: 0 2px; user-select: none; }
.chat-tool-group-summary::-webkit-details-marker { display: none; }
.chat-tool-group-summary:hover { color: var(--color-text-muted); }
.chat-tool-group-label { font-weight: 600; white-space: nowrap; }
.chat-tool-group-text { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.chat-tool-group-count { border: 1px solid color-mix(in srgb, currentColor 30%, transparent); border-radius: 999px; font-size: calc(var(--font-size-ui) * 3 / 4); font-weight: 600; line-height: 1.2; padding: 0 5px; white-space: nowrap; }
.chat-tool-group-time { white-space: nowrap; }
.chat-tool-group-chevron { font-size: calc(var(--font-size-ui) * 5 / 6); transition: transform 120ms ease; }
.chat-tool-group[open] .chat-tool-group-chevron { transform: rotate(90deg); }
.chat-tool-group-body { border-left: 1px solid color-mix(in srgb, currentColor 18%, transparent); display: flex; flex-direction: column; gap: 2px; margin: 2px 0 3px 6px; padding-left: 8px; }
</style>
