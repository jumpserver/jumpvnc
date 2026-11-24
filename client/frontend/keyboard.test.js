import test from 'node:test';
import assert from 'node:assert/strict';
import { nativeRFB } from './native-rfb.js';

// Exercise the pinned noVNC keyboard translator, not just our event adapter.
globalThis.window = { console, addEventListener() {}, removeEventListener() {} };
Object.defineProperty(globalThis, 'navigator', { configurable: true,
  value: { platform: 'MacIntel', userAgent: 'Safari', maxTouchPoints: 0 } });
globalThis.document = { documentElement: {}, body: { appendChild() {} },
  createElement: () => ({ style: {}, offsetWidth: 100, appendChild() {}, parentNode: { removeChild() {} } }) };
globalThis.MutationObserver = class { observe() {} disconnect() {} };
const { default: Keyboard } = await import('./node_modules/@novnc/novnc/core/input/keyboard.js');

function fixture() {
  const sent = [];
  class Base {
    constructor() {
      this._canvas = { addEventListener() {} };
      this._keyboard = new Keyboard(this._canvas);
      this._keyboard.onkeyevent = (...args) => this._handleKeyEvent(...args);
    }
    _handleKeyEvent(keysym, code, down) { sent.push([keysym, code, down]); }
  }
  const client = new (nativeRFB(Base))();
  client.commandAsControl = true;
  const event = (key, code, extra = {}) => ({ key, code, location: 0,
    ctrlKey: false, altKey: false, metaKey: false, shiftKey: false,
    getModifierState: () => false, stopPropagation() {}, preventDefault() {}, ...extra });
  return { client, sent, event };
}

test('Mac control keys produce proper RFB keysyms and paired releases', () => {
  const { client, sent, event } = fixture();
  const keys = [['Backspace', 0xff08], ['Delete', 0xffff], ['Enter', 0xff0d],
    ['Tab', 0xff09], ['Escape', 0xff1b], ['Insert', 0xff63],
    ['ArrowLeft', 0xff51], ['ArrowUp', 0xff52], ['ArrowRight', 0xff53], ['ArrowDown', 0xff54],
    ['Home', 0xff50], ['End', 0xff57], ['PageUp', 0xff55], ['PageDown', 0xff56]];
  for (const [key, keysym] of keys) {
    client._keyboard._handleKeyDown(event(key, key));
    client._keyboard._handleKeyUp(event(key, key));
    assert.deepEqual(sent.splice(0), [[keysym, key, true], [keysym, key, false]], key);
  }
  client._keyboard._handleKeyDown(event('Enter', 'NumpadEnter', { location: 3 }));
  client._keyboard._handleKeyUp(event('Enter', 'NumpadEnter', { location: 3 }));
  assert.deepEqual(sent, [[0xff8d, 'NumpadEnter', true], [0xff8d, 'NumpadEnter', false]]);
});

test('Backspace repeats and focus loss release the key; Option is sent as Alt', () => {
  const { client, sent, event } = fixture();
  client._keyboard._handleKeyDown(event('Backspace', 'Backspace'));
  client._keyboard._handleKeyDown(event('Backspace', 'Backspace', { repeat: true }));
  client._keyboard._allKeysUp();
  assert.deepEqual(sent.splice(0), [[0xff08, 'Backspace', true], [0xff08, 'Backspace', true], [0xff08, 'Backspace', false]]);
  client._keyboard._handleKeyDown(event('Alt', 'AltLeft', { altKey: true, location: 1 }));
  client._keyboard._handleKeyUp(event('Alt', 'AltLeft', { location: 1 }));
  assert.deepEqual(sent, [[0xffe9, 'AltLeft', true], [0xffe9, 'AltLeft', false]]);
});
