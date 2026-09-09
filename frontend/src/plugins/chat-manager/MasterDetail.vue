<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";

defineProps<{
  items: Array<{ id: string; name: string }>;
  selectedId: string;
  createLabel: string;
}>();

const emit = defineEmits<{
  select: [id: string];
  create: [];
}>();

// Mobile drill-down (≤760px): the fixed 230px split leaves the detail form
// unusable on phones, so list and detail become two full-width screens —
// tapping an item (or ＋ 新建) opens the detail, ← 返回列表 goes back.
// Desktop layout is unchanged.
const narrowQuery = window.matchMedia("(max-width: 760px)");
const narrow = ref(narrowQuery.matches);
const detailOpen = ref(false);

function onNarrowChange(event: MediaQueryListEvent): void {
  narrow.value = event.matches;
}

onMounted(() => narrowQuery.addEventListener("change", onNarrowChange));
onBeforeUnmount(() => narrowQuery.removeEventListener("change", onNarrowChange));

function onSelect(id: string): void {
  emit("select", id);
  if (narrow.value) detailOpen.value = true;
}

function onCreate(): void {
  emit("create");
  if (narrow.value) detailOpen.value = true;
}
</script>

<template>
  <section class="master-detail">
    <aside v-if="!narrow || !detailOpen" class="master-column">
      <div class="master-list">
        <button
          v-for="item in items"
          :key="item.id"
          type="button"
          class="master-item"
          :class="{ selected: item.id === selectedId }"
          :title="item.name"
          @click="onSelect(item.id)"
        >
          {{ item.name }}
        </button>
        <div v-if="items.length === 0" class="master-empty">暂无项目</div>
      </div>
      <button type="button" class="master-create" @click="onCreate">{{ createLabel }}</button>
    </aside>
    <main v-if="!narrow || detailOpen" class="detail-column">
      <button v-if="narrow" type="button" class="detail-back" @click="detailOpen = false">
        <i class="bi bi-chevron-left"></i> 返回列表
      </button>
      <slot name="detail"></slot>
    </main>
  </section>
</template>

<style scoped>
.master-detail {
  display: flex;
  height: 100%;
  min-height: 0;
  min-width: 0;
}

.master-column {
  border-right: 1px solid var(--color-border);
  display: flex;
  flex: 0 0 230px;
  flex-direction: column;
  min-height: 0;
  width: 230px;
}

.master-list {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  padding: 6px;
}

.master-item,
.master-create {
  background: transparent;
  border: 0;
  border-radius: var(--radius-sm);
  color: var(--color-text);
  display: block;
  font-size: var(--font-size-ui);
  padding: 7px 8px;
  text-align: left;
  width: 100%;
}

.master-item {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.master-item:hover,
.master-create:hover {
  background: var(--color-surface-hover);
}

.master-item.selected {
  background: var(--color-surface-selected);
  color: var(--color-accent);
}

.master-create {
  border-top: 1px solid var(--color-border);
  border-radius: 0;
  flex: 0 0 auto;
  padding: 9px 12px;
}

.master-empty {
  color: var(--color-text-subtle);
  font-size: var(--font-size-ui-small);
  padding: 7px 8px;
}

.detail-column {
  flex: 1 1 auto;
  min-width: 0;
  overflow: auto;
  padding: 1rem;
}

.detail-back {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: var(--radius-sm);
  color: var(--color-accent);
  display: flex;
  font-size: var(--font-size-ui);
  gap: 2px;
  margin: -0.25rem 0 0.5rem -0.5rem;
  padding: 6px 10px;
}

.detail-back:hover {
  background: var(--color-surface-hover);
}

@media (max-width: 760px) {
  .master-column {
    border-right: 0;
    flex: 1 1 auto;
    width: auto;
  }

  .detail-column {
    padding: 0.75rem;
  }
}
</style>
