<script setup lang="ts">
import { computed } from "vue";
import type { ChatBlock } from "./types";
import { presentToolBlock } from "./toolPresentation";

const props = defineProps<{ block: ChatBlock; time: string }>();
const view = computed(() => presentToolBlock(props.block));
</script>

<template>
  <div class="chat-tool-activity">
    <details v-if="view.sections.length" class="chat-tool-details">
      <summary class="chat-tool-summary" :class="{ 'text-danger': block.kind === 'error' }">
        <i class="bi chat-tool-icon" :class="view.icon" aria-hidden="true" />
        <span class="chat-tool-label">{{ view.label }}</span>
        <span class="chat-tool-text">{{ view.summary }}</span>
        <span v-if="view.status" class="chat-tool-status">{{ view.status }}</span>
        <span class="chat-tool-time">{{ time }}</span>
        <i class="bi bi-chevron-right chat-tool-chevron" aria-hidden="true" />
      </summary>
      <div class="chat-tool-body">
        <section v-for="section in view.sections" :key="section.label" class="chat-tool-section">
          <div class="chat-tool-section-label">{{ section.label }}</div>
          <pre>{{ section.content }}</pre>
        </section>
      </div>
    </details>
    <div v-else class="chat-tool-summary chat-tool-flat" :class="{ 'text-danger': block.kind === 'error' }">
      <i class="bi chat-tool-icon" :class="view.icon" aria-hidden="true" />
      <span class="chat-tool-label">{{ view.label }}</span>
      <span class="chat-tool-text">{{ view.summary }}</span>
      <span v-if="view.status" class="chat-tool-status">{{ view.status }}</span>
      <span class="chat-tool-time">{{ time }}</span>
    </div>
  </div>
</template>

<style scoped>
.chat-tool-activity,
.chat-tool-details { color: color-mix(in srgb, var(--color-text-muted) 72%, transparent); min-width: 0; }
.chat-tool-summary { align-items: center; cursor: pointer; display: grid; font-size: 10px; gap: 4px; grid-template-columns: auto auto minmax(0, 1fr) auto auto auto; line-height: 1.3; list-style: none; min-height: 16px; min-width: 0; padding: 0 2px; user-select: none; }
.chat-tool-summary::-webkit-details-marker { display: none; }
.chat-tool-summary:hover { color: var(--color-text-muted); }
.chat-tool-flat { cursor: default; grid-template-columns: auto auto minmax(0, 1fr) auto auto; }
.chat-tool-icon { color: inherit; font-size: 9.5px; }
.chat-tool-label { color: inherit; font-weight: 600; white-space: nowrap; }
.chat-tool-text { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.chat-tool-status { border: 1px solid color-mix(in srgb, currentColor 30%, transparent); border-radius: 999px; font-size: 9px; line-height: 1.2; max-width: 78px; overflow: hidden; padding: 0 4px; text-overflow: ellipsis; white-space: nowrap; }
.chat-tool-time { font-size: 10px; white-space: nowrap; }
.chat-tool-chevron { font-size: 10px; transition: transform 120ms ease; }
.chat-tool-details[open] .chat-tool-chevron { transform: rotate(90deg); }
.chat-tool-body { max-height: min(420px, 55vh); overflow: auto; padding: 4px 2px 5px 18px; user-select: text; }
.chat-tool-section { margin-bottom: 6px; }
.chat-tool-section:last-child { margin-bottom: 0; }
.chat-tool-section-label { color: var(--color-text-muted); font-size: 9px; font-weight: 600; letter-spacing: .04em; margin: 0 0 2px 2px; text-transform: uppercase; }
.chat-tool-section pre { background: color-mix(in srgb, var(--color-surface-muted) 35%, transparent); border-radius: var(--radius-sm); color: var(--color-text-muted); font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace; font-size: 10.5px; margin: 0; overflow: auto; padding: 6px; white-space: pre-wrap; word-break: break-word; }
</style>
