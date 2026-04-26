<script setup lang="ts">
import type { FileMeta } from "../../types/files";
import { rawUrl } from "../../api/client";

defineProps<{ meta: FileMeta }>();

function formatSize(size: number): string {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
  return `${(size / 1024 / 1024).toFixed(1)} MB`;
}
</script>

<template>
  <div class="unsupported">
    <i class="bi bi-file-earmark"></i>
    <h2>{{ meta.name }}</h2>
    <p>{{ meta.mime }} · {{ formatSize(meta.size) }}</p>
    <a class="btn btn-outline-primary" :href="rawUrl(meta.path)" target="_blank" rel="noreferrer">Open raw</a>
  </div>
</template>

<style scoped>
.unsupported {
  align-items: center;
  color: var(--text-muted);
  display: flex;
  flex-direction: column;
  gap: 8px;
  height: 100%;
  justify-content: center;
  padding: 18px;
  text-align: center;
}

.bi {
  font-size: 42px;
}

h2 {
  color: #172033;
  font-size: 18px;
  margin: 0;
  max-width: 100%;
  overflow-wrap: anywhere;
}

p {
  margin: 0 0 8px;
}
</style>

