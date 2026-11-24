import test from 'node:test';
import assert from 'node:assert/strict';
import { scaleCursor, densityCursor } from './cursor-image.js';

test('downsampling alpha does not turn antialiased white edges gray or opaque', () => {
  // VMware cursors are plain arrays in the pinned noVNC; RichCursor uses typed arrays.
  for (const pixels of [Array, Uint8Array]) {
    const data = [255, 255, 255, 255, 255, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0];
    const image = { w: 2, h: 2, hotx: 1, hoty: 1,
      rgbaPixels: pixels === Array ? data : new pixels(data) };
    const scaled = scaleCursor(image, 0.5);
    assert.deepEqual([...scaled.rgbaPixels], [255, 255, 255, 64]);
    assert.equal(scaled.hotx, 0);
    assert.equal(scaled.hoty, 0);
    assert.equal(scaleCursor(image, 1), image);
  }
});

test('enlargement interpolates coverage while retaining straight color', () => {
  const image = { w: 2, h: 1, hotx: 0, hoty: 0,
    rgbaPixels: [255, 255, 255, 255, 0, 0, 0, 0] };
  const scaled = scaleCursor(image, 2);
  assert.deepEqual([...scaled.rgbaPixels.slice(0, 16)],
    [255, 255, 255, 255, 255, 255, 255, 191, 255, 255, 255, 64, 0, 0, 0, 0]);
});

test('Retina cursor keeps image pixels separate from the CSS click hotspot', () => {
  const image = { w: 4, h: 4, rgbaPixels: new Uint8Array(64) };
  const canvas = { getContext: () => ({ createImageData: () => ({ data: new Uint8Array(64) }), putImageData() {} }),
    toDataURL: () => 'data:image/png;base64,test' };
  const document = { createElement: () => canvas };
  const css = densityCursor(image, 2, { hotx: 1, hoty: 0 }, document, () => true);
  assert.equal(canvas.width, 4);
  assert.match(css, /2x\) 1 0, default$/);
  assert.equal(densityCursor(image, 2, { hotx: 1, hoty: 0 }, document, () => false), null);
});
