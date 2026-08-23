/**
 * Browser SpeechSynthesis wrapper: single-slot speech queue where the newest
 * utterance wins (cancel-then-speak). Used by voice command mode to read
 * results aloud; deliberately standalone so other plugins can reuse it.
 */

export function speechSupported(): boolean {
  return typeof window !== "undefined" && "speechSynthesis" in window;
}

let voicesCache: SpeechSynthesisVoice[] = [];
if (speechSupported()) {
  // getVoices() is empty until the voiceschanged event on some browsers.
  voicesCache = window.speechSynthesis.getVoices();
  window.speechSynthesis.addEventListener("voiceschanged", () => {
    voicesCache = window.speechSynthesis.getVoices();
  });
}

function pickVoice(): SpeechSynthesisVoice | null {
  const voices = voicesCache.length > 0 ? voicesCache : window.speechSynthesis.getVoices();
  return (
    voices.find((voice) => voice.lang.toLowerCase().startsWith("zh")) ??
    voices.find((voice) => voice.default) ??
    null
  );
}

/** Speak text aloud, replacing anything currently playing. */
export function speakText(text: string, onDone?: () => void): void {
  if (!speechSupported() || text.trim() === "") {
    onDone?.();
    return;
  }
  window.speechSynthesis.cancel();
  const utterance = new SpeechSynthesisUtterance(text);
  const voice = pickVoice();
  if (voice !== null) utterance.voice = voice;
  utterance.rate = 1.15;
  if (onDone !== undefined) {
    utterance.onend = onDone;
    utterance.onerror = onDone;
  }
  window.speechSynthesis.speak(utterance);
}

export function stopSpeaking(): void {
  if (speechSupported()) window.speechSynthesis.cancel();
}
