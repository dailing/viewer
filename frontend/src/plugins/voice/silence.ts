/**
 * Trailing-silence detector on a live mic MediaStream: an AnalyserNode
 * measures RMS energy per animation frame; once speech has been heard for at
 * least minSpeechMs in total, silenceMs of continuous low energy fires
 * onSilence exactly once. The energy threshold adapts to the running peak so
 * both quiet and hot mics work. Voice command mode uses this to end an
 * utterance hands-free instead of matching in-band stop keywords (which
 * can't be told apart from dictated content).
 */

export interface SilenceWatchHandle {
  stop: () => void;
}

export interface SilenceWatchOptions {
  /** Continuous quiet time that ends the utterance. */
  silenceMs: number;
  /** Minimum accumulated speech before silence detection arms (default 300). */
  minSpeechMs?: number;
  onSilence: () => void;
}

export function watchTrailingSilence(
  stream: MediaStream,
  options: SilenceWatchOptions,
): SilenceWatchHandle {
  const audioContext = new AudioContext();
  const source = audioContext.createMediaStreamSource(stream);
  const analyser = audioContext.createAnalyser();
  analyser.fftSize = 2048;
  source.connect(analyser);
  const buffer = new Float32Array(analyser.fftSize);

  const minSpeechMs = options.minSpeechMs ?? 300;
  let peak = 0.02;
  let speechMs = 0;
  let lastSpeechAt = performance.now();
  let lastTick = performance.now();
  let fired = false;
  let stopped = false;
  let frame = 0;

  const tick = (): void => {
    if (stopped) return;
    const now = performance.now();
    const elapsed = now - lastTick;
    lastTick = now;
    analyser.getFloatTimeDomainData(buffer);
    let sum = 0;
    for (let index = 0; index < buffer.length; index += 1) {
      sum += buffer[index] * buffer[index];
    }
    const rms = Math.sqrt(sum / buffer.length);
    // Slow-decay peak: threshold follows the loudness of this utterance,
    // floored so a silent room doesn't drive it to zero.
    peak = Math.max(peak * 0.995, rms, 0.01);
    if (rms > peak * 0.25) {
      speechMs += elapsed;
      lastSpeechAt = now;
    }
    if (!fired && speechMs >= minSpeechMs && now - lastSpeechAt >= options.silenceMs) {
      fired = true;
      options.onSilence();
    }
    if (!stopped) frame = requestAnimationFrame(tick);
  };
  frame = requestAnimationFrame(tick);

  return {
    stop: () => {
      if (stopped) return;
      stopped = true;
      cancelAnimationFrame(frame);
      source.disconnect();
      void audioContext.close();
    },
  };
}
