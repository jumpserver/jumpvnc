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
