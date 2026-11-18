<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";

import { bus, busState } from "./bus";
import {
  accountWindow,
  formatCountdown,
  paceExplanation,
  paceLabel,
  remainingPercent,
  selectKimiAccount,
  shortAccountName,
  usagePace,
  type KimiUsageReply,
  type UsageAccount,
  type UsagePace,
  type UsageWindow,
} from "./usage";

const props = defineProps<{ expanded: boolean }>();
const emit = defineEmits<{ "details-change": [open: boolean] }>();

const KIMI_PREFERENCE_KEY = "viewer.usage.kimiProfile.v1";
const kimiAccounts = ref<UsageAccount[]>([]);
const activeKimiID = ref("");
const codexAccount = ref<UsageAccount | null>(null);
const kimiPreference = ref(localStorage.getItem(KIMI_PREFERENCE_KEY) ?? "active");
const kimiError = ref("");
const codexError = ref("");
const loading = ref(false);
const now = ref(Date.now());
const detailsID = ref<string | null>(null);
let generation = 0;
let pollTimer: ReturnType<typeof setInterval> | null = null;
let clockTimer: ReturnType<typeof setInterval> | null = null;

const selectedKimi = computed(() => selectKimiAccount(kimiAccounts.value, activeKimiID.value, kimiPreference.value));
const visibleAccounts = computed(() => [selectedKimi.value, codexAccount.value].filter((account): account is UsageAccount => account !== null));
const detailsAccount = computed(() => visibleAccounts.value.find((account) => account.id === detailsID.value) ?? null);

function mainWindow(account: UsageAccount): UsageWindow | null {
  return accountWindow(account, "7d") ?? accountWindow(account, "5h");
}

function paceFor(window: UsageWindow | null): UsagePace | null {
  return usagePace(window, now.value);
}

function trackTitle(window: UsageWindow | null): string {
  if (window === null) return "暂无额度窗口";
  const pace = paceFor(window);
  const used = `已用 ${Math.round(window.used_percent)}%`;
  return pace === null ? used : `${used} / 时间已过 ${Math.round(pace.elapsedPercent)}%`;
}

function openDetails(account: UsageAccount): void {
  detailsID.value = detailsID.value === account.id ? null : account.id;
}

function closeDetails(): void {
  detailsID.value = null;
}

function handleKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape") closeDetails();
}

function setKimiPreference(): void {
  localStorage.setItem(KIMI_PREFERENCE_KEY, kimiPreference.value);
  if (detailsID.value !== null && selectedKimi.value !== null) detailsID.value = selectedKimi.value.id;
}

async function refresh(force = false): Promise<void> {
  if (!busState.connected) return;
  const current = ++generation;
  loading.value = true;
  const [kimi, codex] = await Promise.allSettled([
    bus.request("llm:_:usage", { force }),
    bus.request("viewer.agent-codex:_:usage", { force }),
  ]);
  if (current !== generation) return;
  if (kimi.status === "fulfilled") {
    const reply = kimi.value as KimiUsageReply;
    kimiAccounts.value = Array.isArray(reply.accounts) ? reply.accounts : [];
    activeKimiID.value = typeof reply.active_profile_id === "string" ? reply.active_profile_id : "";
    kimiError.value = "";
  } else {
    kimiError.value = kimi.reason instanceof Error ? kimi.reason.message : "Kimi 配额读取失败";
  }
  if (codex.status === "fulfilled") {
    codexAccount.value = codex.value as UsageAccount;
    codexError.value = "";
  } else {
    codexError.value = codex.reason instanceof Error ? codex.reason.message : "Codex 配额读取失败";
  }
  loading.value = false;
}

function exactReset(window: UsageWindow | null): string {
  if (window?.reset_at === undefined || window.reset_at === "") return "重置时间未知";
  const date = new Date(window.reset_at);
  return Number.isFinite(date.getTime()) ? `重置于 ${date.toLocaleString()}` : "重置时间未知";
}

watch(detailsID, (value) => emit("details-change", value !== null));
watch(detailsAccount, (account) => {
  if (detailsID.value !== null && account === null) closeDetails();
});
watch(() => props.expanded, (expanded) => {
  if (expanded) void refresh();
  else closeDetails();
}, { immediate: true });
watch(() => busState.connected, (connected) => {
  if (connected && props.expanded) void refresh();
});

onMounted(() => {
  window.addEventListener("keydown", handleKeydown);
  pollTimer = setInterval(() => {
    if (props.expanded) void refresh();
  }, 5 * 60 * 1000);
  clockTimer = setInterval(() => { now.value = Date.now(); }, 30 * 1000);
});

onBeforeUnmount(() => {
  generation++;
  window.removeEventListener("keydown", handleKeydown);
  if (pollTimer !== null) clearInterval(pollTimer);
  if (clockTimer !== null) clearInterval(clockTimer);
  emit("details-change", false);
});
</script>

<template>
  <section v-if="expanded" class="usage-dock" aria-label="额度节奏">
    <button
      v-for="account in visibleAccounts"
      :key="account.id"
      type="button"
      class="usage-account"
      :class="{ 'usage-account-open': detailsID === account.id }"
      :title="`${shortAccountName(account)}：${trackTitle(mainWindow(account))}`"
      @click="openDetails(account)"
    >
      <span class="usage-heading">
        <span class="usage-name">{{ shortAccountName(account) }}</span>
        <span v-if="mainWindow(account)" class="usage-remaining">
          {{ accountWindow(account, '7d') === null ? '5h ' : '' }}余 {{ remainingPercent(mainWindow(account)!) }}%
        </span>
        <span v-else class="usage-remaining">—</span>
      </span>
      <span v-if="mainWindow(account) && paceFor(mainWindow(account))" class="usage-track" :class="`usage-${paceFor(mainWindow(account))!.tone}`" role="img" :aria-label="trackTitle(mainWindow(account)!)">
        <span class="usage-track-fill" :style="{ width: `${mainWindow(account)!.used_percent}%` }"></span>
        <span class="usage-track-time" :style="{ left: `${paceFor(mainWindow(account))!.elapsedPercent}%` }"></span>
      </span>
    </button>

    <button v-if="visibleAccounts.length === 0" type="button" class="usage-empty" :disabled="loading" @click="refresh(true)">
      <i class="bi" :class="loading ? 'bi-arrow-repeat usage-spin' : 'bi-speedometer2'" />
      <span>{{ loading ? '读取额度…' : (kimiError || codexError || '点击读取额度') }}</span>
    </button>

    <div v-if="detailsAccount" class="usage-overlay" @click="closeDetails"></div>
    <aside v-if="detailsAccount" class="usage-details" aria-label="额度详情">
      <div class="usage-details-head">
        <strong>{{ shortAccountName(detailsAccount) }}</strong>
        <span v-if="detailsAccount.plan" class="usage-plan">{{ detailsAccount.plan }}</span>
        <button type="button" title="刷新全部额度" :disabled="loading" @click="refresh(true)"><i class="bi" :class="loading ? 'bi-arrow-repeat usage-spin' : 'bi-arrow-clockwise'" /></button>
        <button type="button" title="关闭详情" @click="closeDetails"><i class="bi bi-x-lg" /></button>
      </div>

      <label v-if="detailsAccount.provider === 'kimi' && kimiAccounts.length > 1" class="usage-profile">
        <span>显示账号</span>
        <select v-model="kimiPreference" @change="setKimiPreference">
          <option value="active">跟随当前</option>
          <option v-for="account in kimiAccounts" :key="account.id" :value="account.id">{{ shortAccountName(account) }}</option>
        </select>
      </label>

      <div v-if="detailsAccount.error" class="usage-detail-error">{{ detailsAccount.error }}</div>
      <div v-if="detailsAccount.provider === 'kimi' && kimiError" class="usage-detail-error">{{ kimiError }}</div>
      <div v-if="detailsAccount.provider === 'codex' && codexError" class="usage-detail-error">{{ codexError }}</div>

      <template v-if="accountWindow(detailsAccount, '7d')">
        <div class="usage-detail-metrics">
          <span>本周已用 {{ Math.round(accountWindow(detailsAccount, '7d')!.used_percent) }}%</span>
          <span v-if="paceFor(accountWindow(detailsAccount, '7d'))">时间已过 {{ Math.round(paceFor(accountWindow(detailsAccount, '7d'))!.elapsedPercent) }}%</span>
        </div>
        <div v-if="paceFor(accountWindow(detailsAccount, '7d'))" class="usage-track usage-track-detail" :class="`usage-${paceFor(accountWindow(detailsAccount, '7d'))!.tone}`" role="img" :aria-label="trackTitle(accountWindow(detailsAccount, '7d')!)">
          <span class="usage-track-fill" :style="{ width: `${accountWindow(detailsAccount, '7d')!.used_percent}%` }"></span>
          <span class="usage-track-time" :style="{ left: `${paceFor(accountWindow(detailsAccount, '7d'))!.elapsedPercent}%` }"></span>
        </div>
        <div v-if="paceFor(accountWindow(detailsAccount, '7d'))" class="usage-pace-explanation" :class="`usage-text-${paceFor(accountWindow(detailsAccount, '7d'))!.tone}`">
          {{ paceLabel(paceFor(accountWindow(detailsAccount, '7d'))!) }} · {{ paceExplanation(paceFor(accountWindow(detailsAccount, '7d'))!) }}
        </div>
        <div class="usage-detail-line">剩余 {{ remainingPercent(accountWindow(detailsAccount, '7d')!) }}% · {{ formatCountdown(accountWindow(detailsAccount, '7d')!.reset_at, now) }}</div>
        <div class="usage-exact">{{ exactReset(accountWindow(detailsAccount, '7d')) }}</div>
      </template>

      <div v-if="accountWindow(detailsAccount, '5h')" class="usage-short-detail" :class="{ 'usage-short-exhausted': accountWindow(detailsAccount, '5h')!.used_percent >= 100 }">
        <span>5 小时额度</span>
        <strong>{{ accountWindow(detailsAccount, '5h')!.used_percent >= 100 ? '已用尽' : `剩余 ${remainingPercent(accountWindow(detailsAccount, '5h')!)}%` }}</strong>
        <span>{{ formatCountdown(accountWindow(detailsAccount, '5h')!.reset_at, now) }}</span>
        <div class="usage-exact">{{ exactReset(accountWindow(detailsAccount, '5h')) }}</div>
      </div>
      <div v-if="mainWindow(detailsAccount) === null" class="usage-detail-error">暂无可显示的额度窗口</div>
      <div class="usage-fetched">更新于 {{ new Date(detailsAccount.fetched_at).toLocaleTimeString() }}</div>
    </aside>
  </section>
</template>

<style scoped>
.usage-dock {
  border-top: 1px solid var(--color-border);
  flex: 0 0 auto;
  padding: 5px 6px 5px 4px;
  position: relative;
  width: calc(100% - 6px);
}
.usage-account {
  background: transparent;
  border: 0;
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding: 5px 5px 6px;
  text-align: left;
  width: 100%;
}
.usage-account:hover,
.usage-account-open { background: var(--color-surface-hover); }
.usage-heading { align-items: center; display: flex; gap: 6px; width: 100%; }
.usage-name { color: var(--color-text); font-size: var(--font-size-ui); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.usage-remaining { flex: 0 0 auto; font-size: var(--font-size-ui-small); margin-left: auto; }
.usage-track { background: var(--color-surface-selected); border-radius: 2px; display: block; height: 5px; overflow: visible; position: relative; width: 100%; }
.usage-track-fill { background: var(--color-text-subtle); border-radius: 2px; bottom: 0; left: 0; position: absolute; top: 0; }
.usage-track-time { background: var(--color-text); bottom: -2px; position: absolute; top: -2px; width: 1px; }
.usage-calm .usage-track-fill { background: var(--color-info); }
.usage-fast .usage-track-fill { background: var(--color-warning); }
.usage-exhausted .usage-track-fill { background: var(--color-danger); }
.usage-text-calm { color: var(--color-info); }
.usage-text-fast { color: var(--color-warning); }
.usage-text-exhausted { color: var(--color-danger); }
.usage-text-balanced,
.usage-text-neutral { color: var(--color-text-muted); }
.usage-empty { align-items: center; background: transparent; border: 0; color: var(--color-text-muted); display: flex; font-size: var(--font-size-ui-small); gap: 6px; padding: 6px; width: 100%; }
.usage-overlay { inset: 0; position: fixed; z-index: 44; }
.usage-details { background: var(--color-surface); border: 1px solid var(--color-border); border-radius: var(--radius-md); bottom: 0; color: var(--color-text); left: calc(100% + 12px); min-width: 300px; padding: 10px 12px; position: absolute; width: 320px; z-index: 45; }
.usage-details-head { align-items: center; display: flex; gap: 7px; margin-bottom: 8px; }
.usage-details-head strong { font-size: var(--font-size-ui); }
.usage-details-head button { background: transparent; border: 0; color: var(--color-text-muted); font-size: var(--font-size-ui-small); margin-left: auto; padding: 2px 3px; }
.usage-details-head button + button { margin-left: 0; }
.usage-plan { color: var(--color-text-subtle); font-size: var(--font-size-ui-small); }
.usage-profile { align-items: center; color: var(--color-text-muted); display: flex; font-size: var(--font-size-ui-small); gap: 8px; margin-bottom: 10px; }
.usage-profile select { background: var(--color-surface); border: 1px solid var(--color-border); border-radius: var(--radius-sm); color: var(--color-text); font-size: var(--font-size-ui-small); margin-left: auto; max-width: 170px; padding: 2px 5px; }
.usage-detail-metrics { color: var(--color-text-muted); display: flex; font-size: var(--font-size-ui-small); justify-content: space-between; margin-bottom: 5px; }
.usage-track-detail { height: 7px; margin: 9px 0; }
.usage-pace-explanation { font-size: var(--font-size-ui-small); margin: 6px 0; }
.usage-detail-line { font-size: var(--font-size-ui); margin-top: 7px; }
.usage-exact { color: var(--color-text-subtle); font-size: var(--font-size-ui-small); margin-top: 2px; }
.usage-short-detail { border-top: 1px solid var(--color-border); display: grid; font-size: var(--font-size-ui-small); gap: 4px 8px; grid-template-columns: 1fr auto; margin-top: 10px; padding-top: 8px; }
.usage-short-detail > span:last-of-type { grid-column: 1 / -1; }
.usage-short-detail .usage-exact { grid-column: 1 / -1; }
.usage-short-exhausted strong { color: var(--color-danger); }
.usage-detail-error { color: var(--color-warning); font-size: var(--font-size-ui-small); margin: 6px 0; overflow-wrap: anywhere; }
.usage-fetched { color: var(--color-text-subtle); font-size: calc(var(--font-size-ui) * 5 / 6); margin-top: 9px; }
.usage-spin { animation: usage-spin 1s linear infinite; display: inline-block; }
@keyframes usage-spin { to { transform: rotate(360deg); } }
</style>
