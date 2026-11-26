export const qualityProfiles = Object.freeze({
  video: { label: '动态画面', quality: 3, compression: 1, video: true },
  sharp: { label: '清晰文字', quality: 8, compression: 2 },
});

export function applyQuality(rfb, name) {
  const profile = qualityProfiles[name] || qualityProfiles.video;
  rfb.qualityLevel = profile.quality;
  rfb.compressionLevel = profile.compression;
  rfb.preferVideo = Boolean(profile.video);
  return profile;
}

function emptyWindow() {
  return { updates: 0, decodeMs: 0, rectangles: 0, paintMs: 0, paints: 0,
    waitMs: 0, waits: 0, peakQueue: 0, encodings: new Map() };
}

export class PerformanceWindow {
  constructor(clock = () => performance.now()) {
    this.clock = clock;
    this.started = clock();
    this.window = emptyWindow();
    this.partialDecodeMs = 0;
    this.lastEncoding = '';
  }

  decode(name, elapsed, complete) {
    this.partialDecodeMs += elapsed;
    if (!complete) return;
    this.window.decodeMs += this.partialDecodeMs;
    this.partialDecodeMs = 0;
    this.window.rectangles++;
    this.window.encodings.set(name, (this.window.encodings.get(name) || 0) + 1);
    this.lastEncoding = name;
  }

  update() { this.window.updates++; }
  paint(elapsed) { this.window.paintMs += elapsed; this.window.paints++; }
  wait(elapsed) { this.window.waitMs += elapsed; this.window.waits++; }
  queue(depth) { this.window.peakQueue = Math.max(this.window.peakQueue, depth); }

  sample(state = {}) {
    const now = this.clock();
    const seconds = (now - this.started) / 1000;
    if (seconds <= 0) return null;
    const w = this.window;
    this.window = emptyWindow();
    this.started = now;
    return {
      ...state, seconds, updatesPerSecond: w.updates / seconds,
      decodeMillis: w.rectangles ? w.decodeMs / w.rectangles : null,
      paintMillis: w.paints ? w.paintMs / w.paints : null,
      waitMillis: w.waits ? w.waitMs / w.waits : 0,
      peakQueue: Math.max(w.peakQueue, state.queue || 0),
      encodings: [...w.encodings].sort((a, b) => b[1] - a[1]),
      lastEncoding: this.lastEncoding,
    };
  }
}

export class NetworkWindow {
  constructor() { this.previous = null; }

  sample(current) {
    const previous = this.previous;
    this.previous = current;
    if (!previous || current.id !== previous.id) return null;
    const seconds = current.elapsedSeconds - previous.elapsedSeconds;
    const bytes = current.receivedBytes - previous.receivedBytes;
    if (seconds <= 0 || bytes < 0) return null;
    return { receiveMbps: bytes * 8 / seconds / 1e6 };
  }
}

export function formatReceiveRate(mbps) {
  if (mbps == null) return { value: '—', unit: '' };
  return mbps < 1
    ? { value: (mbps * 1000).toFixed(1), unit: 'kbps' }
    : { value: mbps.toFixed(1), unit: 'Mbps' };
}
