<script setup lang="ts">
defineProps<{
  items: Array<{ id: string; name: string }>;
  selectedId: string;
  createLabel: string;
}>();

defineEmits<{
  select: [id: string];
  create: [];
}>();
</script>

<template>
  <section class="master-detail">
    <aside class="master-column">
      <div class="master-list">
        <button
          v-for="item in items"
          :key="item.id"
          type="button"
          class="master-item"
          :class="{ selected: item.id === selectedId }"
          :title="item.name"
          @click="$emit('select', item.id)"
        >
          {{ item.name }}
        </button>
        <div v-if="items.length === 0" class="master-empty">暂无项目</div>
      </div>
      <button type="button" class="master-create" @click="$emit('create')">{{ createLabel }}</button>
    </aside>
    <main class="detail-column">
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
</style>
