import test from 'node:test';
import assert from 'node:assert/strict';
import { nativeRFB } from './native-rfb.js';

function fixture({ notify = false, provide = true, text = true, viewOnly = false, connected = true } = {}) {
  const sent = [];
  class Base {
    static messages = { extendedClipboardProvide: (...args) => sent.push(['utf8', ...args]) };
    clipboardPasteFrom(value) { sent.push(['base', value]); }
  }
  const client = new (nativeRFB(Base))();
  Object.assign(client, {
    _sock: 'socket', _rfbConnectionState: connected ? 'connected' : 'disconnected', _viewOnly: viewOnly,
    _clipboardServerCapabilitiesFormats: { 1: text },
    _clipboardServerCapabilitiesActions: { [1 << 27]: notify, [1 << 28]: provide },
  });
  return { client, sent };
}

test('LibVNCServer Provide-only clipboard keeps Chinese text', () => {
  const { client, sent } = fixture();
  client.clipboardPasteFrom('中文剪贴板');
  assert.deepEqual(sent, [['utf8', 'socket', [1], ['中文剪贴板']]]);
});

test('normal negotiation, old servers and input policy keep base behavior', () => {
  for (const options of [{ notify: true }, { provide: false }, { text: false }, { viewOnly: true }, { connected: false }]) {
    const { client, sent } = fixture(options);
    client.clipboardPasteFrom('text');
    assert.deepEqual(sent, [['base', 'text']]);
  }
});

test('native mouse press with an empty buttons mask preserves the changed button', () => {
  class Base { _handleMouse(event) { return event.buttons; } }
  const client = new (nativeRFB(Base))();
  for (const [button, mask] of [[0, 1], [1, 4], [2, 2], [3, 8], [4, 16]]) {
    assert.equal(client._handleMouse({ type: 'mousedown', button, buttons: 0 }), mask);
  }
  assert.equal(client._handleMouse({ type: 'mousedown', button: 0, buttons: 3 }), 3);
  assert.equal(client._handleMouse({ type: 'mouseup', button: 0, buttons: 0 }), 0);
  assert.equal(client._handleMouse({ type: 'mousemove', button: 0, buttons: 0 }), 0);
});

test('Mac Command maps to Control and paste waits for clipboard sync before key events', async () => {
  const events = [];
  class Base { _handleKeyEvent(...args) { events.push(args); } }
  const client = new (nativeRFB(Base))();
  client.commandAsControl = true;
  let ready;
  client.beforePaste = () => new Promise(resolve => { ready = resolve; });
  client._handleKeyEvent(0xffeb, 'MetaLeft', true);
  client._handleKeyEvent(0x76, 'KeyV', true);
  client._handleKeyEvent(0x76, 'KeyV', false);
  client._handleKeyEvent(0xffeb, 'MetaLeft', false);
  assert.deepEqual(events, [[0xffe3, 'ControlLeft', true]]);
  await Promise.resolve();
  ready();
  await client._nativeKeyQueue;
  assert.deepEqual(events, [[0xffe3, 'ControlLeft', true], [0x76, 'KeyV', true],
    [0x76, 'KeyV', false], [0xffe3, 'ControlLeft', false]]);
});

test('failed clipboard sync releases modifiers and permits the next paste', async () => {
  const events = [], errors = [];
  class Base { _handleKeyEvent(...args) { events.push(args); } }
  const client = new (nativeRFB(Base))();
  client.beforePaste = async () => { throw new Error('clipboard unavailable'); };
  client.onClipboardError = error => errors.push(error.message);
  client._handleKeyEvent(0xffe3, 'ControlLeft', true);
  client._handleKeyEvent(0x76, 'KeyV', true);
  client._handleKeyEvent(0x76, 'KeyV', false);
  client._handleKeyEvent(0xffe3, 'ControlLeft', false);
  await client._nativeKeyQueue;
  assert.deepEqual(errors, ['clipboard unavailable']);
  assert.equal(events.at(-1)[2], false);
  assert.equal(events.filter(([key, , down]) => key === 0x76 && down).length, 0);
});

test('native modifier flags without individual keydown events reach the remote desktop', () => {
  const listeners = {}, events = [];
  class Base {
    constructor() {
      this._canvas = { addEventListener: (name, fn) => { listeners[name] = fn; } };
      this._keyboard = { _keyDownList: {}, _sendKeyEvent: (sym, code, down) => {
        if (down) this._keyboard._keyDownList[code] = sym;
        else delete this._keyboard._keyDownList[code];
        this._handleKeyEvent(sym, code, down);
      } };
    }
    _handleKeyEvent(...args) { events.push(args); }
  }
  const client = new (nativeRFB(Base))();
  client.commandAsControl = true;
  listeners.keydown({ code: 'KeyV', metaKey: true, shiftKey: true, ctrlKey: false });
  assert.deepEqual(events, [[0xffe1, 'ShiftLeft', true], [0xffe3, 'ControlLeft', true]]);
  listeners.keyup({ code: 'KeyV', metaKey: false, shiftKey: false, ctrlKey: false });
  assert.deepEqual(events.slice(2), [[0xffe1, 'ShiftLeft', false], [0xffe3, 'ControlLeft', false]]);
  assert.deepEqual(client._keyboard._keyDownList, {});
});

test('switching Command paste to Control shortcut releases the old modifier first', () => {
  const listeners = {}, events = [];
  class Base {
    constructor() {
      this._canvas = { addEventListener: (name, fn) => { listeners[name] = fn; } };
      this._keyboard = { _keyDownList: {}, _sendKeyEvent: (sym, code, down) => {
        if (down) this._keyboard._keyDownList[code] = sym;
        else delete this._keyboard._keyDownList[code];
        this._handleKeyEvent(sym, code, down);
      } };
    }
    _handleKeyEvent(...args) { events.push(args); }
  }
  const client = new (nativeRFB(Base))();
  client.commandAsControl = true;
  listeners.keydown({ code: 'KeyV', metaKey: true, shiftKey: true, ctrlKey: false });
  listeners.keydown({ code: 'KeyU', metaKey: false, shiftKey: false, ctrlKey: true });
  assert.deepEqual(events.slice(2), [[0xffe1, 'ShiftLeft', false],
    [0xffe3, 'ControlLeft', false], [0xffe3, 'ControlLeft', true]]);
});
