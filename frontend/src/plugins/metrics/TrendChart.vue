<script setup lang="ts">
import { computed } from "vue";
import { formatCompact } from "./metricsFormat";

export interface TrendSeries {
  name: string;
  color: string;
  values: number[];
}

const props = defineProps<{ labels: string[]; series: TrendSeries[]; height?: number }>();
const width = 760;
const height = computed(() => props.height ?? 190);
const left = 48;
const right = 16;
const top = 18;
const bottom = 32;
const maximum = computed(() => Math.max(1, ...props.series.flatMap((series) => series.values)));
const plotWidth = width - left - right;
const plotHeight = computed(() => height.value - top - bottom);
function x(index: number): number {
  return left + (props.labels.length <= 1 ? plotWidth / 2 : index * plotWidth / (props.labels.length - 1));
}
function y(value: number): number {
  return top + plotHeight.value * (1 - value / maximum.value);
}
function path(values: number[]): string {
  return values.map((value, index) => `${index === 0 ? "M" : "L"}${x(index).toFixed(2)},${y(value).toFixed(2)}`).join(" ");
}
const tickIndexes = computed(() => {
  if (props.labels.length <= 5) return props.labels.map((_, index) => index);
  return [...new Set([0, Math.floor((props.labels.length - 1) / 4), Math.floor((props.labels.length - 1) / 2), Math.floor((props.labels.length - 1) * 3 / 4), props.labels.length - 1])];
});
</script>

<template>
  <div class="trend">
    <div class="legend"><span v-for="item in series" :key="item.name"><i :style="{ background: item.color }" />{{ item.name }}</span></div>
    <svg :viewBox="`0 0 ${width} ${height}`" role="img" aria-label="Token trend">
      <line v-for="tick in 4" :key="tick" :x1="left" :x2="width - right" :y1="top + plotHeight * (tick - 1) / 4" :y2="top + plotHeight * (tick - 1) / 4" class="grid" />
      <text v-for="tick in 4" :key="`label-${tick}`" :x="left - 7" :y="top + plotHeight * (tick - 1) / 4 + 3" text-anchor="end" class="axis">{{ formatCompact(maximum * (5 - tick) / 4) }}</text>
      <path v-for="item in series" :key="item.name" :d="path(item.values)" :stroke="item.color" class="line"><title>{{ item.name }}</title></path>
      <template v-for="index in tickIndexes" :key="`date-${index}`"><text :x="x(index)" :y="height - 8" text-anchor="middle" class="axis">{{ labels[index]?.slice(5) }}</text></template>
    </svg>
  </div>
</template>

<style scoped>
.trend { min-width: 0; }
.legend { display: flex; gap: 14px; justify-content: flex-end; color: var(--color-text-muted); font-size: var(--font-size-ui-small); }
.legend span { align-items: center; display: inline-flex; gap: 5px; }
.legend i { border-radius: 50%; height: 7px; width: 7px; }
svg { display: block; height: auto; overflow: visible; width: 100%; }
.grid { stroke: var(--color-border); stroke-width: 1; }
.axis { fill: var(--color-text-subtle); font-size: 10px; }
.line { fill: none; stroke-linecap: round; stroke-linejoin: round; stroke-width: 2; }
</style>
