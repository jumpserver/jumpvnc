import test from 'node:test';
import assert from 'node:assert/strict';
import { installH264AccessUnits } from './h264-access-units.js';

// noVNC's logging module expects a browser global even for its pure parser.
globalThis.window = { console };
const { H264Parser } = await import('./node_modules/@novnc/novnc/core/decoders/h264.js');

installH264AccessUnits(H264Parser);
const nal = (type, ...bytes) => [0, 0, 0, 1, type, ...bytes];

test('Microsoft multi-slice output yields one complete picture per access unit', () => {
  const picture = [...nal(9, 0xf0), ...nal(7, 66, 192, 50), ...nal(8, 0xc0),
    ...nal(5, 0x80, 3), ...nal(5, 0x20, 4), ...nal(5, 0x08, 5)];
  const next = [...nal(9, 0xf0), ...nal(1, 0x80, 6), ...nal(1, 0x20, 7)];
  const parser = new H264Parser(new Uint8Array([...picture, ...next]));
  const first = parser.parse();
  assert.equal(first.key, true);
  assert.deepEqual([...first.frame], picture);
  assert.equal(parser.profileIdc, 66);
  assert.equal(parser.constraintSet, 192);
  assert.equal(parser.levelIdc, 50);
  const second = parser.parse();
  assert.equal(second.key, false);
  assert.deepEqual([...second.frame], next);
  assert.equal(parser.parse(), null);
});

test('single-slice streams without AUD retain frame boundaries', () => {
  const first = nal(5, 0x80, 2);
  const next = nal(1, 0x80, 3);
  const parser = new H264Parser(new Uint8Array([...first, ...next]));
  assert.deepEqual([...parser.parse().frame], first);
  assert.deepEqual([...parser.parse().frame], next);
  assert.equal(parser.parse(), null);
});

test('parameter-only data does not enqueue a picture that can never be displayed', () => {
  const parser = new H264Parser(new Uint8Array(nal(7, 66, 192, 50)));
  assert.equal(parser.parse(), null);
  assert.equal(new H264Parser(new Uint8Array()).parse(), null);
  assert.throws(() => new H264Parser(new Uint8Array([0, 0, 0, 1])).parse());
});
