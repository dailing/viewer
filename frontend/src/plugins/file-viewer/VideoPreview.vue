<script setup lang="ts">
/** Browser-native local video playback with compact, explicit controls. */
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";

import { clampVideoTime, formatVideoTime, VIDEO_SKIP_SECONDS, videoErrorMessage } from "./videoPlayer";

const props = defineProps<{
  src: string;
  label: string;
}>();
const emit = defineEmits<{ retry: [] }>();

const rootRef = ref<HTMLElement | null>(null);
const videoRef = ref<HTMLVideoElement | null>(null);
const currentTime = ref(0);
const duration = ref(0);
const playing = ref(false);
const buffering = ref(false);
const muted = ref(false);
const volume = ref(1);
const playbackRate = ref(1);
const fullscreen = ref(false);
const error = ref("");
const actionError = ref("");

const seekable = computed(() => Number.isFinite(duration.value) && duration.value > 0 && error.value === "");

function syncMetadata(): void {
  const video = videoRef.value;
  if (video === null) return;
  duration.value = Number.isFinite(video.duration) ? video.duration : 0;
  currentTime.value = video.currentTime;
  muted.value = video.muted;
  volume.value = video.volume;
  playbackRate.value = video.playbackRate;
  error.value = "";
}

function syncTime(): void {
  currentTime.value = videoRef.value?.currentTime ?? 0;
}

function syncVolume(): void {
  const video = videoRef.value;
  if (video === null) return;
  muted.value = video.muted;
  volume.value = video.volume;
}

function handleError(): void {
  error.value = videoErrorMessage(videoRef.value?.error?.code);
  playing.value = false;
  buffering.value = false;
}

async function togglePlayback(): Promise<void> {
  const video = videoRef.value;
  if (video === null || error.value !== "") return;
  if (!video.paused) {
    video.pause();
    return;
  }
  actionError.value = "";
  if (video.ended) video.currentTime = 0;
  try {
    await video.play();
  } catch (cause) {
    playing.value = false;
    buffering.value = false;
    actionError.value = cause instanceof DOMException && cause.name === "NotAllowedError"
      ? "浏览器阻止了播放，请再次点击播放。"
      : "视频播放失败，请重试。";
  }
}

function seekTo(value: number): void {
  const video = videoRef.value;
  if (video === null || !seekable.value) return;
  const target = clampVideoTime(value, video.duration);
  video.currentTime = target;
  currentTime.value = target;
}

function seekBy(delta: number): void {
  seekTo((videoRef.value?.currentTime ?? currentTime.value) + delta);
}

function seekFromInput(event: Event): void {
  seekTo(Number((event.target as HTMLInputElement).value));
}

function setVolume(value: number): void {
  const video = videoRef.value;
  if (video === null) return;
  video.volume = clampVideoTime(value, 1);
  if (video.volume > 0) video.muted = false;
  syncVolume();
}

function volumeFromInput(event: Event): void {
  setVolume(Number((event.target as HTMLInputElement).value));
}

function toggleMute(): void {
  const video = videoRef.value;
  if (video === null) return;
  video.muted = !video.muted;
  syncVolume();
}

function changePlaybackRate(event: Event): void {
  const video = videoRef.value;
  if (video === null) return;
  video.playbackRate = Number((event.target as HTMLSelectElement).value);
  playbackRate.value = video.playbackRate;
}

function syncFullscreen(): void {
  fullscreen.value = document.fullscreenElement === rootRef.value;
}

async function toggleFullscreen(): Promise<void> {
  try {
    if (document.fullscreenElement !== null) {
      await document.exitFullscreen();
    } else {
      await rootRef.value?.requestFullscreen();
    }
  } catch {
    actionError.value = "无法切换全屏模式，请检查浏览器权限。";
  }
}

function focusRoot(): void {
  rootRef.value?.focus({ preventScroll: true });
}

function handleKeydown(event: KeyboardEvent): void {
  const target = event.target as HTMLElement | null;
  if (target?.closest("button, input, select") !== null && target !== rootRef.value) return;
  switch (event.key.toLowerCase()) {
    case " ":
    case "k":
      event.preventDefault();
      void togglePlayback();
      break;
    case "arrowleft":
      event.preventDefault();
      seekBy(-VIDEO_SKIP_SECONDS);
      break;
    case "arrowright":
      event.preventDefault();
      seekBy(VIDEO_SKIP_SECONDS);
      break;
    case "arrowup":
      event.preventDefault();
      setVolume((videoRef.value?.volume ?? volume.value) + 0.1);
      break;
    case "arrowdown":
      event.preventDefault();
      setVolume((videoRef.value?.volume ?? volume.value) - 0.1);
      break;
    case "m":
      event.preventDefault();
      toggleMute();
      break;
    case "f":
      event.preventDefault();
      void toggleFullscreen();
      break;
  }
}

watch(() => props.src, async () => {
  currentTime.value = 0;
  duration.value = 0;
  playing.value = false;
  buffering.value = false;
  error.value = "";
  actionError.value = "";
  await nextTick();
  videoRef.value?.load();
});

onMounted(() => document.addEventListener("fullscreenchange", syncFullscreen));
onUnmounted(() => {
  document.removeEventListener("fullscreenchange", syncFullscreen);
  const video = videoRef.value;
  if (video !== null) {
    video.pause();
    video.removeAttribute("src");
    video.load();
  }
});
</script>

<template>
  <div
    ref="rootRef"
    class="video-preview"
    tabindex="0"
    aria-label="视频播放器：空格播放或暂停，左右方向键前后 10 秒，M 静音，F 全屏"
    @keydown="handleKeydown"
  >
    <div class="video-stage">
      <video
        ref="videoRef"
        class="video-element"
        :src="src"
        :aria-label="label"
        preload="metadata"
        playsinline
        @click="focusRoot(); void togglePlayback()"
        @loadedmetadata="syncMetadata"
        @durationchange="syncMetadata"
        @timeupdate="syncTime"
        @seeked="syncTime(); buffering = false"
        @play="playing = true; actionError = ''"
        @pause="playing = false; buffering = false"
        @ended="playing = false; buffering = false"
        @waiting="buffering = true"
        @stalled="buffering = true"
        @playing="buffering = false"
        @canplay="buffering = false"
        @volumechange="syncVolume"
        @ratechange="playbackRate = videoRef?.playbackRate ?? 1"
        @error="handleError"
      ></video>
      <div v-if="buffering && !error" class="video-status" aria-live="polite">
        <i class="bi bi-arrow-repeat spin" aria-hidden="true"></i>
      </div>
      <div v-if="error" class="video-error" role="alert">
        <i class="bi bi-exclamation-triangle" aria-hidden="true"></i>
        <div>{{ error }}</div>
        <button type="button" class="video-retry" @click="emit('retry')">
          <i class="bi bi-arrow-clockwise" aria-hidden="true"></i> 重新加载
        </button>
      </div>
    </div>

    <div class="video-controls">
      <div v-if="actionError" class="video-action-error" role="alert">{{ actionError }}</div>
      <div class="video-timeline">
        <input
          class="video-seek"
          type="range"
          min="0"
          :max="duration || 0"
          step="0.1"
          :value="currentTime"
          :disabled="!seekable"
          aria-label="播放进度"
          :aria-valuetext="`${formatVideoTime(currentTime)} / ${formatVideoTime(duration)}`"
          @input="seekFromInput"
        />
        <span class="video-time">{{ formatVideoTime(currentTime) }} / {{ formatVideoTime(duration) }}</span>
      </div>
      <div class="video-buttons">
        <button type="button" class="video-button video-play" :title="playing ? '暂停（空格）' : '播放（空格）'" :disabled="!!error" @click="void togglePlayback()">
          <i :class="`bi ${playing ? 'bi-pause-fill' : 'bi-play-fill'}`" aria-hidden="true"></i>
          <span class="visually-hidden">{{ playing ? "暂停" : "播放" }}</span>
        </button>
        <button type="button" class="video-button video-skip" title="后退 10 秒（←）" :disabled="!seekable" @click="seekBy(-VIDEO_SKIP_SECONDS)">
          <i class="bi bi-arrow-counterclockwise" aria-hidden="true"></i><span>10</span>
        </button>
        <button type="button" class="video-button video-skip" title="前进 10 秒（→）" :disabled="!seekable" @click="seekBy(VIDEO_SKIP_SECONDS)">
          <i class="bi bi-arrow-clockwise" aria-hidden="true"></i><span>10</span>
        </button>
        <span class="video-spacer"></span>
        <button type="button" class="video-button" :title="muted || volume === 0 ? '取消静音（M）' : '静音（M）'" @click="toggleMute">
          <i :class="`bi ${muted || volume === 0 ? 'bi-volume-mute-fill' : 'bi-volume-up-fill'}`" aria-hidden="true"></i>
          <span class="visually-hidden">{{ muted || volume === 0 ? "取消静音" : "静音" }}</span>
        </button>
        <input
          class="video-volume"
          type="range"
          min="0"
          max="1"
          step="0.05"
          :value="muted ? 0 : volume"
          aria-label="音量"
          title="音量（↑ / ↓）"
          @input="volumeFromInput"
        />
        <select class="video-rate" :value="playbackRate" title="播放速度" aria-label="播放速度" @change="changePlaybackRate">
          <option :value="0.5">0.5×</option>
          <option :value="0.75">0.75×</option>
          <option :value="1">1×</option>
          <option :value="1.25">1.25×</option>
          <option :value="1.5">1.5×</option>
          <option :value="2">2×</option>
        </select>
        <button type="button" class="video-button" :title="fullscreen ? '退出全屏（F）' : '全屏（F）'" @click="void toggleFullscreen()">
          <i :class="`bi ${fullscreen ? 'bi-fullscreen-exit' : 'bi-arrows-fullscreen'}`" aria-hidden="true"></i>
          <span class="visually-hidden">{{ fullscreen ? "退出全屏" : "全屏" }}</span>
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.video-preview {
  background: #111;
  color: #f4f4f4;
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  min-width: 0;
  outline: none;
}
.video-preview:focus-visible { box-shadow: inset 0 0 0 1px var(--color-accent); }
.video-stage {
  align-items: center;
  display: flex;
  flex: 1;
  justify-content: center;
  min-height: 0;
  overflow: hidden;
  position: relative;
}
.video-element {
  cursor: pointer;
  display: block;
  height: 100%;
  max-height: 100%;
  max-width: 100%;
  object-fit: contain;
  width: 100%;
}
.video-status {
  font-size: 28px;
  left: 50%;
  pointer-events: none;
  position: absolute;
  top: 50%;
  transform: translate(-50%, -50%);
}
.video-error {
  align-items: center;
  background: rgb(17 17 17 / 88%);
  display: flex;
  flex-direction: column;
  font-size: var(--font-size-ui);
  gap: 10px;
  inset: 0;
  justify-content: center;
  padding: 24px;
  position: absolute;
  text-align: center;
}
.video-error > i { font-size: 24px; }
.video-retry {
  background: transparent;
  border: 1px solid #777;
  border-radius: var(--radius-sm);
  color: inherit;
  padding: 4px 10px;
}
.video-retry:hover { background: rgb(255 255 255 / 10%); }
.video-controls {
  background: #1b1b1b;
  display: flex;
  flex: 0 0 auto;
  flex-direction: column;
  gap: 3px;
  padding: 5px 8px 6px;
}
.video-timeline { align-items: center; display: flex; gap: 8px; min-width: 0; }
.video-action-error { color: #ffb4a8; font-size: var(--font-size-ui-small); line-height: 1.3; }
.video-seek { flex: 1; height: 14px; margin: 0; min-width: 40px; }
.video-time { flex: 0 0 auto; font-family: var(--font-family-mono, monospace); font-size: var(--font-size-ui-small); white-space: nowrap; }
.video-buttons { align-items: center; display: flex; gap: 3px; min-width: 0; }
.video-button {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: var(--radius-sm);
  color: inherit;
  display: inline-flex;
  flex: 0 0 auto;
  height: 26px;
  justify-content: center;
  min-width: 28px;
  padding: 0 5px;
}
.video-button:hover:not(:disabled), .video-rate:hover { background: rgb(255 255 255 / 10%); }
.video-button:disabled { cursor: default; opacity: 0.4; }
.video-play { font-size: 19px; }
.video-skip { gap: 1px; }
.video-skip span { font-size: 10px; }
.video-spacer { flex: 1; }
.video-volume { flex: 0 1 72px; height: 16px; margin: 0; min-width: 36px; }
.video-rate {
  background: transparent;
  border: 0;
  border-radius: var(--radius-sm);
  color: inherit;
  flex: 0 0 auto;
  font-size: var(--font-size-ui-small);
  height: 26px;
  padding: 0 2px;
}
.video-rate option { background: #1b1b1b; color: #f4f4f4; }
.video-preview:fullscreen { height: 100vh; width: 100vw; }
.spin { animation: video-spin 1s linear infinite; display: inline-block; }
@keyframes video-spin { to { transform: rotate(360deg); } }
@media (max-width: 420px) {
  .video-volume { flex-basis: 48px; }
  .video-buttons { gap: 1px; }
}
</style>
