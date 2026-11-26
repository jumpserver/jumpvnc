import { PerformanceWindow } from './performance.js';

const encodingNames = new Map([
  [0, 'Raw'], [1, 'CopyRect'], [2, 'RRE'], [5, 'Hextile'], [6, 'Zlib'],
  [7, 'Tight'], [16, 'ZRLE'], [-260, 'TightPNG'], [21, 'JPEG'], [50, 'H.264'],
]);
export const captureStatusEncoding = 0x4a564350; // JumpVNC "JVCP"

// Adapter for the pinned noVNC 1.7.0 internals. It observes the decoder and
// Canvas calls without changing protocol parsing, update requests or rendering.
export function measureRFB(BaseRFB, clock = () => performance.now()) {
  return class MeasuredRFB extends BaseRFB {
    constructor(...args) {
      super(...args);
      this.metrics = new PerformanceWindow(clock);
      this.hadDataUpdate = false;
      const display = this._display;
      const flip = display.flip.bind(display);
      display.flip = (...args) => {
        const bounds = display._damageBounds;
        const paints = (args[0] || !display.pending()) &&
          bounds.right > bounds.left && bounds.bottom > bounds.top;
        const started = clock();
        try { return flip(...args); }
        finally { if (paints) this.metrics.paint(clock() - started); }
      };
      const flush = display.flush.bind(display);
      const observed = new WeakSet();
      display.flush = () => {
        const pending = display.pending();
        const started = clock();
        const promise = flush();
        if (pending && !observed.has(promise)) {
          observed.add(promise);
          promise.then(() => this.metrics.wait(clock() - started), () => {});
        }
        return promise;
      };
    }

    _handleRect() {
      if (this._FBU.encoding !== captureStatusEncoding) return super._handleRect();
      if (this._sock.rQwait('JumpVNC capture status', 1)) return false;
      this.captureMethod = ['Unavailable', 'DXGI', 'GDI'][this._sock.rQshift8()] || 'Not reported';
      return true;
    }

    _handleDataRect() {
      const encoding = this._FBU.encoding;
      const started = clock();
      // Encoding 50 starts with a 32-bit length and big-endian flags. Peek before
      // noVNC consumes them, and commit only after the full rectangle arrives.
      let encoderMode = null;
      if (encoding === 50 && this._sock.rQlen() >= 8) {
        const flags = this._sock.rQpeekBytes(8, false)[6] & 3;
        encoderMode = flags === 2 ? 'Hardware' : flags === 1 ? 'Software' : 'Not reported';
      }
      let complete = false;
      try {
        complete = super._handleDataRect();
        return complete;
      } finally {
        this.metrics.decode(encodingNames.get(encoding) || String(encoding), clock() - started, complete);
        if (complete) {
          this.hadDataUpdate = true;
          if (encoding !== 1) this.encoderMode = encoding === 50 ? encoderMode : 'Inactive';
        }
        this.metrics.queue(this._display._renderQ.length);
      }
    }

    _framebufferUpdate() {
      const complete = super._framebufferUpdate();
      if (complete) {
        if (this.hadDataUpdate) this.metrics.update();
        this.hadDataUpdate = false;
      }
      return complete;
    }

    _fail(details) {
      this.failureReason = details;
      return super._fail(details);
    }

    _updateCursor(...args) {
      super._updateCursor(...args);
      this.cursorShapeUpdates = (this.cursorShapeUpdates || 0) + 1;
    }

    performanceSnapshot() {
      return this.metrics.sample({
        captureMethod: this.captureMethod || 'Not reported',
        encoderMode: this.encoderMode || 'Not reported',
        width: this._fbWidth, height: this._fbHeight,
        queue: this._display._renderQ.length, receiveBacklog: this._sock.rQlen(),
        cursorShapeUpdates: this.cursorShapeUpdates || 0,
        cursorWidth: this._cursorImage?.w || 0, cursorHeight: this._cursorImage?.h || 0,
      });
    }
  };
}
