import test from 'node:test';
import assert from 'node:assert/strict';
import { formatReceiveRate, NetworkWindow, PerformanceWindow } from './performance.js';
import { captureStatusEncoding, measureRFB } from './rfb-performance.js';

test('capture and encoder diagnostics follow server reports, including fallback and idle periods', () => {
  class Base {
    constructor() {
      this._display = { _renderQ: [], flip() {}, flush() {} };
      this._FBU = {};
    }
    _handleDataRect() { return this.complete; }
  }
  let now = 0;
  const viewer = new (measureRFB(Base, () => ++now))();
  let byte = 1, available = true;
  viewer._sock = { rQwait: () => !available, rQshift8: () => byte, rQlen: () => 8,
    rQpeekBytes: () => Uint8Array.of(0, 0, 0, 0, 0, 0, byte, 0) };
  assert.equal(viewer.performanceSnapshot().captureMethod, 'Not reported');
  viewer._FBU.encoding = captureStatusEncoding;
  for (const [code, method] of [[1, 'DXGI'], [0, 'Unavailable'], [2, 'GDI']]) {
    byte = code;
    assert.equal(viewer._handleRect(), true);
    assert.equal(viewer.performanceSnapshot().captureMethod, method);
  }
  available = false;
  assert.equal(viewer._handleRect(), false);
  viewer._FBU.encoding = 50;
  viewer.complete = true;
  for (const [code, mode] of [[0, 'Not reported'], [1, 'Software'], [2, 'Hardware']]) {
    byte = code;
    viewer._handleDataRect();
    assert.equal(viewer.performanceSnapshot().encoderMode, mode);
  }
  viewer.complete = false;
  byte = 1;
  viewer._handleDataRect();
  assert.equal(viewer.performanceSnapshot().encoderMode, 'Hardware');
  viewer.complete = true;
  viewer._FBU.encoding = 7;
  viewer._handleDataRect();
  assert.equal(viewer.performanceSnapshot().encoderMode, 'Inactive');
  assert.equal(viewer.performanceSnapshot().captureMethod, 'GDI');
});

test('receive throughput distinguishes missing and idle samples and switches at 1 Mbps', () => {
  assert.deepEqual(formatReceiveRate(null), { value: '—', unit: '' });
  assert.deepEqual(formatReceiveRate(0), { value: '0.0', unit: 'kbps' });
  assert.deepEqual(formatReceiveRate(0.3404), { value: '340.4', unit: 'kbps' });
  assert.deepEqual(formatReceiveRate(1), { value: '1.0', unit: 'Mbps' });
});

test('network rates use elapsed time and reset on reconnect or counter reset', () => {
  const metrics = new NetworkWindow();
  const sample = (id, elapsedSeconds, receivedBytes) => metrics.sample({ id, elapsedSeconds, receivedBytes });
  assert.equal(sample('a', 1, 100), null);
  assert.equal(sample('a', 3.5, 3125100).receiveMbps, 10);
  assert.equal(sample('a', 13.5, 15625100).receiveMbps, 10);
  assert.equal(sample('b', 0.2, 200), null);
  assert.equal(sample('b', 1.2, 1250200).receiveMbps, 10);
  assert.equal(sample('b', 2, 0), null);
  assert.equal(sample('b', 2, 0), null);
});

test('partial rectangles accumulate decode cost without counting fragments as updates', () => {
  let now = 0;
  class FakeRFB {
    constructor() {
      this._fbWidth = 1920;
      this._fbHeight = 1080;
      this._sock = { rQlen: () => 8 };
      this._FBU = { encoding: 7 };
      this._display = {
        _renderQ: [], _damageBounds: { left: 0, top: 0, right: 0, bottom: 0 },
        flip() {}, pending() { return this._renderQ.length > 0; }, flush() { return Promise.resolve(); },
      };
    }
    _handleDataRect() { now += this.cost; return this.complete; }
    _framebufferUpdate() { return this.metadata ? true : this._handleDataRect(); }
    _updateCursor(rgba, hotx, hoty, w, h) { this._cursorImage = { rgbaPixels: rgba, hotx, hoty, w, h }; }
  }
  const RFB = measureRFB(FakeRFB, () => now);
  const rfb = new RFB();
  rfb.cost = 2;
  rfb.complete = false;
  assert.equal(rfb._framebufferUpdate(), false);
  rfb.cost = 3;
  rfb.complete = true;
  assert.equal(rfb._framebufferUpdate(), true);
  rfb.metadata = true;
  assert.equal(rfb._framebufferUpdate(), true);
  rfb.metadata = false;
  rfb._FBU.encoding = -260;
  rfb.cost = 1;
  rfb._display._renderQ = [1, 2];
  rfb._framebufferUpdate();
  now = 1000;
  const sample = rfb.performanceSnapshot();
  assert.equal(sample.updatesPerSecond, 2);
  assert.equal(sample.decodeMillis, 3);
  assert.deepEqual(sample.encodings, [['Tight', 1], ['TightPNG', 1]]);
  assert.equal(sample.receiveBacklog, 8);
  assert.equal(sample.peakQueue, 2);
  assert.equal(sample.cursorShapeUpdates, 0);
  rfb._updateCursor(new Uint8Array(32 * 32 * 4), 1, 2, 32, 32);
  now = 2000;
  const idle = rfb.performanceSnapshot();
  assert.equal(idle.updatesPerSecond, 0);
  assert.equal(idle.decodeMillis, null);
  assert.deepEqual(idle.encodings, []);
  assert.equal(idle.lastEncoding, 'TightPNG');
  assert.equal(idle.cursorShapeUpdates, 1);
  assert.equal(idle.cursorWidth, 32);
  assert.equal(idle.cursorHeight, 32);
  rfb._updateCursor(new Uint8Array(), 0, 0, 0, 0);
  now = 3000;
  const hidden = rfb.performanceSnapshot();
  assert.equal(hidden.cursorShapeUpdates, 2);
  assert.equal(hidden.cursorWidth, 0);
});

test('render waiting observes a flush promise once and preserves its identity', async () => {
  let now = 0;
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  class FakeRFB {
    constructor() {
      this._display = {
        _renderQ: [1], _damageBounds: { left: 0, top: 0, right: 10, bottom: 10 },
        pending() { return this._renderQ.length > 0; },
        flip() { now += 4; this._damageBounds.right = 0; },
        flush() { return promise; },
      };
    }
  }
  const RFB = measureRFB(FakeRFB, () => now);
  const rfb = new RFB();
  assert.equal(rfb._display.flush(), promise);
  now = 1;
  assert.equal(rfb._display.flush(), promise);
  now = 5;
  resolve();
  await promise;
  rfb._display._renderQ = [];
  rfb._display.flip();
  rfb._display.flip();
  now = 1000;
  const sample = rfb.metrics.sample();
  assert.equal(sample.waitMillis, 5);
  assert.equal(sample.paintMillis, 4);
});

test('sampling retains an unfinished rectangle across windows', () => {
  let now = 0;
  const metrics = new PerformanceWindow(() => now);
  metrics.decode('Tight', 2, false);
  now = 1000;
  assert.equal(metrics.sample().decodeMillis, null);
  metrics.decode('Tight', 3, true);
  now = 2000;
  assert.equal(metrics.sample().decodeMillis, 5);
});
