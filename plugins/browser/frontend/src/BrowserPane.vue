<script setup lang="ts">
import { computed, inject, nextTick, onMounted, ref, watchEffect } from "vue";
import type { PluginCtx } from "@viewer/bus-sdk";

interface Viewport {
  css_width: number;
  css_height: number;
  pixel_width: number;
  pixel_height: number;
  page_zoom: number;
  render_scale: number;
}
interface VideoSettings { fps: number; bitrate_kbps: number }
interface BrowserPage {
  page_id: string;
  title: string;
  url: string;
  online: boolean;
  busy: boolean;
  control_owner: string;
  viewport: Viewport;
  video: VideoSettings;
  watchers: number;
  last_error?: string;
}
interface BrowserState { backend: string; pages: BrowserPage[] }
interface WatchResult {
  page: BrowserPage;
  viewport_owner: boolean;
  stream: { stream_id: string; ticket: string };
}
interface BrowserInput {
  type: string;
  x?: number; y?: number; button?: string; buttons?: number;
  deltaX?: number; deltaY?: number; key?: string; code?: string;
  keyCode?: number; modifiers?: number; text?: string;
}

const injected = inject<PluginCtx>("pluginCtx");
if (injected === undefined) throw new Error("BrowserPane requires pluginCtx");
const ctx: PluginCtx = injected;
const pageID = ctx.instanceId;
const viewerID = crypto.randomUUID();
const stage = ref<HTMLDivElement>();
const video = ref<HTMLVideoElement>();
const keyboard = ref<HTMLTextAreaElement>();
const state = ref<BrowserState>({ backend: "managed_chrome", pages: [] });
const page = computed(() => state.value.pages.find((item) => item.page_id === pageID));
const viewport = ref<Viewport>({ css_width: 1280, css_height: 720, pixel_width: 1280, pixel_height: 720, page_zoom: 1, render_scale: 1 });
const pageZoom = ref(ctx.storage.get("page_zoom", "1"));
const renderScale = ref(ctx.storage.get("render_scale", "1"));
const fps = ref(ctx.storage.get("fps", "15"));
const bitrate = ref(ctx.storage.get("bitrate_kbps", "4000"));
const address = ref("");
const copied = ref(false);
const error = ref("");
const streamStatus = ref("连接中");
const viewportOwner = ref(true);
const humanLease = ref("");
const controlBusy = ref(false);
const videoReady = ref(false);
let peer: RTCPeerConnection | undefined;
let peerStreamID = "";
let rtcStarting = false;
let rtcGeneration = 0;
let watchInFlight = false;
let watchQueued = false;
let heartbeat = 0;
let resizeTimer: ReturnType<typeof setTimeout> | undefined;
let observer: ResizeObserver | undefined;
let moveTimer: ReturnType<typeof setTimeout> | undefined;
let pendingMove: PointerEvent | undefined;
let wheelTimer: ReturnType<typeof setTimeout> | undefined;
let wheelX = 0;
let wheelY = 0;
let wheelDeltaX = 0;
let wheelDeltaY = 0;
let composing = false;

function syncState(next: BrowserState): void {
  state.value = next;
  const current = page.value;
  if (current) {
    viewport.value = current.viewport;
    if (document.activeElement !== undefined && document.activeElement?.id !== "browser-address") address.value = current.url;
  }
}

async function ensureControl(takeover = false): Promise<boolean> {
  if (humanLease.value && !takeover) return true;
  if (controlBusy.value) return false;
  controlBusy.value = true;
  try {
    const result = await ctx.bus.request<{ lease_id: string }>("viewer.browser:_:control", { page_id: pageID, viewer_id: viewerID, takeover });
    humanLease.value = result.lease_id;
    error.value = "";
    return true;
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : String(caught);
    return false;
  } finally {
    controlBusy.value = false;
  }
}

async function takeover(): Promise<void> {
  if (await ensureControl(true)) keyboard.value?.focus();
}

function scheduleWatch(delay = 200): void {
  if (resizeTimer) clearTimeout(resizeTimer);
  resizeTimer = setTimeout(() => { void watchPage(); }, delay);
}

async function watchPage(): Promise<void> {
  if (watchInFlight) {
    watchQueued = true;
    return;
  }
  if (!stage.value || stage.value.clientWidth < 20 || stage.value.clientHeight < 20) return;
  watchInFlight = true;
  try {
    const result = await ctx.bus.request<WatchResult>("viewer.browser:_:watch", {
      page_id: pageID,
      viewer_id: viewerID,
      pane_width: Math.round(stage.value.clientWidth),
      pane_height: Math.round(stage.value.clientHeight),
      page_zoom: Number(pageZoom.value),
      render_scale: Number(renderScale.value),
      fps: Number(fps.value),
      bitrate_kbps: Number(bitrate.value),
    });
    viewport.value = result.page.viewport;
    viewportOwner.value = result.viewport_owner;
    streamStatus.value = "视频协商中";
    error.value = "";
    if (!peer || peerStreamID !== result.stream.stream_id || peer.connectionState === "failed" || peer.connectionState === "closed") {
      void startWebRTC(result.stream.stream_id, result.stream.ticket);
    }
  } catch (caught) {
    streamStatus.value = "视频不可用";
    error.value = caught instanceof Error ? caught.message : String(caught);
  } finally {
    watchInFlight = false;
    if (watchQueued) {
      watchQueued = false;
      scheduleWatch(0);
    }
  }
}

async function startWebRTC(streamID: string, ticket: string): Promise<void> {
  if (rtcStarting) return;
  rtcStarting = true;
  const generation = ++rtcGeneration;
  try {
    peer?.close();
    const next = new RTCPeerConnection();
    peer = next;
    peerStreamID = streamID;
    next.ontrack = (event) => {
      if (generation !== rtcGeneration || !video.value) return;
      video.value.srcObject = event.streams[0] ?? new MediaStream([event.track]);
      videoReady.value = true;
      streamStatus.value = "实时视频";
      void video.value.play().catch(() => undefined);
    };
    next.onconnectionstatechange = () => {
      if (generation !== rtcGeneration) return;
      if (next.connectionState === "failed" || next.connectionState === "closed") {
        streamStatus.value = "视频重连中";
        videoReady.value = false;
        peer = undefined;
        scheduleWatch(500);
      }
    };
    next.addTransceiver("video", { direction: "recvonly" });
    const offer = await next.createOffer();
    await next.setLocalDescription(offer);
    await new Promise<void>((resolve, reject) => {
      if (next.iceGatheringState === "complete") return resolve();
      const timer = setTimeout(() => reject(new Error("WebRTC ICE gathering timed out")), 8000);
      next.addEventListener("icegatheringstatechange", () => {
        if (next.iceGatheringState === "complete") {
          clearTimeout(timer);
          resolve();
        }
      });
    });
    const response = await fetch("/api/media/webrtc", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ stream_id: streamID, ticket, offer: next.localDescription }),
    });
    if (!response.ok) throw new Error(await response.text());
    const signal = await response.json() as { answer: RTCSessionDescriptionInit };
    if (generation !== rtcGeneration) return;
    await next.setRemoteDescription(signal.answer);
  } catch (caught) {
    if (generation === rtcGeneration) {
      streamStatus.value = "视频连接失败";
      error.value = caught instanceof Error ? caught.message : String(caught);
      peer?.close();
      peer = undefined;
    }
  } finally {
    rtcStarting = false;
  }
}

function changeSetting(key: "page_zoom" | "render_scale" | "fps" | "bitrate_kbps", value: string): void {
  ctx.storage.set(key, value);
  if (key === "page_zoom") pageZoom.value = value;
  if (key === "render_scale") renderScale.value = value;
  if (key === "fps") fps.value = value;
  if (key === "bitrate_kbps") bitrate.value = value;
  scheduleWatch(0);
}

async function copyID(): Promise<void> {
  try { await navigator.clipboard.writeText(pageID); } catch {
    const input = document.createElement("textarea");
    input.value = pageID;
    document.body.appendChild(input);
    input.select();
    document.execCommand("copy");
    input.remove();
  }
  copied.value = true;
  setTimeout(() => { copied.value = false; }, 1000);
}

async function closePage(): Promise<void> {
  if (!window.confirm(`永久关闭浏览器页面 ${pageID}？仅关闭 Viewer pane 不会关闭页面。`)) return;
  await ctx.bus.request("viewer.browser:_:close", { page_id: pageID });
}

async function navigate(): Promise<void> {
  const raw = address.value.trim();
  if (!raw || !(await ensureControl())) return;
  try {
    await ctx.bus.request("viewer.browser:_:navigate", { page_id: pageID, viewer_id: viewerID, url: raw });
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : String(caught);
  }
}

function point(event: PointerEvent | WheelEvent): { x: number; y: number } {
  const rect = video.value?.getBoundingClientRect();
  if (!rect || rect.width <= 0 || rect.height <= 0) return { x: 0, y: 0 };
  return {
    x: Math.max(0, Math.min(viewport.value.css_width, (event.clientX - rect.left) * viewport.value.css_width / rect.width)),
    y: Math.max(0, Math.min(viewport.value.css_height, (event.clientY - rect.top) * viewport.value.css_height / rect.height)),
  };
}

async function sendInput(event: BrowserInput): Promise<void> {
  try {
    await ctx.bus.request("viewer.browser:_:input", { page_id: pageID, viewer_id: viewerID, event }, { timeout: 5000 });
    error.value = "";
  } catch (caught) {
    humanLease.value = "";
    error.value = caught instanceof Error ? caught.message : String(caught);
  }
}

function buttonName(value: number): string {
  return value === 1 ? "middle" : value === 2 ? "right" : "left";
}

async function pointerDown(event: PointerEvent): Promise<void> {
  if (event.button > 2) return;
  event.preventDefault();
  keyboard.value?.focus({ preventScroll: true });
  video.value?.setPointerCapture(event.pointerId);
  if (!(await ensureControl())) return;
  const p = point(event);
  await sendInput({ type: "mousePressed", ...p, button: buttonName(event.button), buttons: event.buttons });
}

function pointerMove(event: PointerEvent): void {
  if (!humanLease.value) return;
  pendingMove = event;
  if (moveTimer) return;
  moveTimer = setTimeout(() => {
    const latest = pendingMove;
    moveTimer = undefined;
    if (!latest) return;
    const p = point(latest);
    void sendInput({ type: "mouseMoved", ...p, button: latest.buttons ? buttonName(latest.button) : "none", buttons: latest.buttons });
  }, 33);
}

async function pointerUp(event: PointerEvent): Promise<void> {
  if (!humanLease.value) return;
  const p = point(event);
  await sendInput({ type: "mouseReleased", ...p, button: buttonName(event.button), buttons: event.buttons });
}

function modifiers(event: KeyboardEvent): number {
  return (event.altKey ? 1 : 0) | (event.ctrlKey ? 2 : 0) | (event.metaKey ? 4 : 0) | (event.shiftKey ? 8 : 0);
}

function wheel(event: WheelEvent): void {
  event.preventDefault();
  if (!humanLease.value) return;
  const multiplier = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? viewport.value.css_height : 1;
  wheelDeltaX += event.deltaX * multiplier;
  wheelDeltaY += event.deltaY * multiplier;
  wheelX = event.clientX;
  wheelY = event.clientY;
  if (wheelTimer) return;
  wheelTimer = setTimeout(() => {
    const p = point({ clientX: wheelX, clientY: wheelY } as WheelEvent);
    const deltaX = wheelDeltaX;
    const deltaY = wheelDeltaY;
    wheelDeltaX = wheelDeltaY = 0;
    wheelTimer = undefined;
    void sendInput({ type: "mouseWheel", ...p, deltaX, deltaY });
  }, 40);
}

async function keyDown(event: KeyboardEvent): Promise<void> {
  if (composing || (event.key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey)) return;
  event.preventDefault();
  if (!(await ensureControl())) return;
  await sendInput({ type: "keyDown", key: event.key, code: event.code, keyCode: event.keyCode, modifiers: modifiers(event) });
}

async function keyUp(event: KeyboardEvent): Promise<void> {
  if (composing || (event.key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey)) return;
  event.preventDefault();
  if (!humanLease.value) return;
  await sendInput({ type: "keyUp", key: event.key, code: event.code, keyCode: event.keyCode, modifiers: modifiers(event) });
}

async function insertText(): Promise<void> {
  if (composing || !keyboard.value) return;
  const text = keyboard.value.value;
  keyboard.value.value = "";
  if (text && humanLease.value) await sendInput({ type: "insertText", text });
}

function compositionEnd(): void {
  composing = false;
  void insertText();
}

watchEffect(() => {
  const current = page.value;
  ctx.setChrome({
    title: current?.title || "Browser",
    status: `${current?.control_owner === "agent" ? "Agent 控制中" : current?.control_owner === "human" ? "人工控制" : "空闲"} · ${streamStatus.value} · ${viewport.value.pixel_width}×${viewport.value.pixel_height}`,
    actions: [
      { id: "copy", title: copied.value ? "已复制页面 ID" : `复制 ${pageID}`, icon: copied.value ? "bi-check2" : "bi-clipboard", run: copyID },
      { id: "takeover", title: "接管页面（撤销其他控制 lease）", icon: "bi-hand-index", run: takeover },
      { id: "refresh", title: "重新连接视频", icon: "bi-arrow-clockwise", run: () => { peer?.close(); peer = undefined; return watchPage(); } },
      { id: "close", title: "永久关闭后台浏览器页面", icon: "bi-x-lg", variant: "danger", run: closePage },
    ],
    controls: [
      { kind: "select", id: "zoom", title: "页面缩放：改变后端 CSS viewport", value: pageZoom.value, options: ["0.5", "0.75", "1", "1.25", "1.5", "2"], size: "compact", onChange: (value) => changeSetting("page_zoom", value) },
      { kind: "select", id: "scale", title: "render_scale：只改变视频像素分辨率", value: renderScale.value, options: ["0.5", "0.75", "1", "1.25", "1.5", "2"], size: "compact", onChange: (value) => changeSetting("render_scale", value) },
      { kind: "select", id: "fps", title: "FPS：采集与编码帧率", value: fps.value, options: ["1", "2", "5", "10", "15", "24", "30", "60"], size: "compact", onChange: (value) => changeSetting("fps", value) },
      { kind: "select", id: "bitrate", title: "bitrate：H.264 码率（kbps）", value: bitrate.value, options: ["500", "1000", "2000", "4000", "8000", "12000", "20000"], size: "compact", onChange: (value) => changeSetting("bitrate_kbps", value) },
    ],
  });
});

onMounted(() => {
  ctx.bus.subscribe("viewer.browser:_:state", (frame) => syncState(frame.value as BrowserState));
  void ctx.bus.request<BrowserState>("viewer.browser:_:state").then(syncState).catch(() => undefined);
  observer = new ResizeObserver(() => scheduleWatch());
  if (stage.value) observer.observe(stage.value);
  void nextTick(() => watchPage());
  heartbeat = window.setInterval(() => void watchPage(), 10000);
  ctx.onDispose(() => {
    if (observer) observer.disconnect();
    if (resizeTimer) clearTimeout(resizeTimer);
    if (moveTimer) clearTimeout(moveTimer);
    if (wheelTimer) clearTimeout(wheelTimer);
    window.clearInterval(heartbeat);
    rtcGeneration++;
    peer?.close();
    void ctx.bus.request("viewer.browser:_:unwatch", { page_id: pageID, viewer_id: viewerID }, { timeout: 2000 }).catch(() => undefined);
  });
});
</script>

<template>
  <div class="browser-page">
    <div class="address-row">
      <i class="bi bi-lock"></i>
      <input id="browser-address" v-model="address" aria-label="页面地址" spellcheck="false" @keyup.enter="navigate" />
      <button type="button" title="导航" @click="navigate"><i class="bi bi-arrow-right"></i></button>
      <code :title="pageID">{{ pageID }}</code>
    </div>
    <div v-if="error" class="browser-error">{{ error }}</div>
    <div ref="stage" class="video-stage" @wheel="wheel" @contextmenu.prevent>
      <video
        ref="video"
        autoplay
        muted
        playsinline
        :class="{ ready: videoReady }"
        @pointerdown="pointerDown"
        @pointermove="pointerMove"
        @pointerup="pointerUp"
        @pointercancel="pointerUp"
      ></video>
      <div v-if="!videoReady" class="video-placeholder">
        <i class="bi bi-window-desktop"></i>
        <span>{{ page?.online === false ? (page.last_error || "后台 Chrome 页面离线") : streamStatus }}</span>
      </div>
      <div v-if="page?.control_owner === 'agent' && !humanLease" class="control-hint">Agent 控制中 · 点击顶部接管按钮可切换人工控制</div>
    </div>
    <textarea
      ref="keyboard"
      class="keyboard-input"
      aria-label="浏览器键盘输入"
      autocomplete="off"
      autocapitalize="off"
      autocorrect="off"
      spellcheck="false"
      @keydown="keyDown"
      @keyup="keyUp"
      @input="insertText"
      @compositionstart="composing = true"
      @compositionend="compositionEnd"
    ></textarea>
  </div>
</template>

<style scoped>
.browser-page { height: 100%; min-height: 0; display: flex; flex-direction: column; position: relative; background: #111; color: var(--color-text); overflow: hidden; }
.address-row { min-height: 30px; display: flex; align-items: center; gap: 6px; padding: 3px 7px; background: var(--color-canvas); color: var(--color-text-muted); font-size: var(--font-size-ui); }
.address-row input { flex: 1; min-width: 0; border: 0; outline: 0; background: transparent; color: var(--color-text); font: inherit; }
.address-row button { border: 0; background: transparent; color: var(--color-text-muted); cursor: pointer; padding: 2px 5px; }
.address-row button:hover { color: var(--color-text); }
.address-row code { max-width: 230px; overflow: hidden; text-overflow: ellipsis; color: var(--color-text-muted); user-select: all; }
.browser-error { padding: 4px 8px; background: var(--color-canvas); color: var(--color-danger); font-size: calc(var(--font-size-ui) * 11 / 12); overflow-wrap: anywhere; }
.video-stage { flex: 1; min-height: 0; position: relative; overflow: hidden; background: #111; }
.video-stage video { display: block; width: 100%; height: 100%; object-fit: fill; opacity: 0; touch-action: none; user-select: none; cursor: default; }
.video-stage video.ready { opacity: 1; }
.video-placeholder { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; flex-direction: column; gap: 10px; color: #888; pointer-events: none; }
.video-placeholder i { font-size: 30px; }
.control-hint { position: absolute; left: 8px; bottom: 8px; padding: 3px 6px; color: #ddd; background: rgb(0 0 0 / 55%); font-size: 11px; pointer-events: none; }
.keyboard-input { position: absolute; width: 2px; height: 2px; left: 0; bottom: 0; opacity: 0; border: 0; resize: none; pointer-events: none; }
@media (max-width: 700px) { .address-row code { display: none; } }
</style>
